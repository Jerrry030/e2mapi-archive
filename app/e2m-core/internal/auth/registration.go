package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

var (
	ErrRegistrationDisabled        = errors.New("auth: registration disabled")
	ErrEmailSuffixNotAllowed       = errors.New("auth: email suffix not allowed")
	ErrInvitationRequired          = errors.New("auth: invitation code required")
	ErrInvitationCodeInvalid       = errors.New("auth: invitation code invalid")
	ErrTurnstileRequired           = errors.New("auth: turnstile token required")
	ErrTurnstileVerificationFailed = errors.New("auth: turnstile verification failed")
)

// RegistrationConfig controls public client self-registration. Defaults are
// closed: self-registration and Turnstile are both disabled unless explicitly
// enabled by the operator.
type RegistrationConfig struct {
	Enabled                bool
	EmailSuffixWhitelist   []string
	InvitationRequired     bool
	TurnstileEnabled       bool
	TurnstileSiteKey       string
	TurnstileSecretKey     string
	TurnstileSiteVerifyURL string
}

// RegistrationResult contains the newly created client-role user and the freshly
// issued login token.
type RegistrationResult struct {
	Token     string
	User      contracts.User
	ExpiresAt time.Time
}

// TurnstileVerifier verifies Cloudflare Turnstile tokens. Tests inject a stub;
// production uses HTTPDefaultTurnstileVerifier.
type TurnstileVerifier interface {
	Verify(ctx context.Context, secret, token, remoteIP, siteVerifyURL string) error
}

type HTTPDefaultTurnstileVerifier struct {
	Client *http.Client
}

func (v HTTPDefaultTurnstileVerifier) Verify(ctx context.Context, secret, token, remoteIP, siteVerifyURL string) error {
	endpoint := strings.TrimSpace(siteVerifyURL)
	if endpoint == "" {
		endpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	}
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrTurnstileVerificationFailed
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.Success {
		return ErrTurnstileVerificationFailed
	}
	return nil
}

func (s *Service) ConfigureRegistration(cfg RegistrationConfig) {
	cfg.EmailSuffixWhitelist = normalizeEmailSuffixes(cfg.EmailSuffixWhitelist)
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	s.registration = cfg
}

func (s *Service) RegistrationConfig() RegistrationConfig {
	s.registrationMu.RLock()
	defer s.registrationMu.RUnlock()
	cfg := s.registration
	cfg.EmailSuffixWhitelist = append([]string(nil), cfg.EmailSuffixWhitelist...)
	return cfg
}

func (s *Service) SetTurnstileVerifier(verifier TurnstileVerifier) {
	if verifier == nil {
		s.turnstileVerifier = HTTPDefaultTurnstileVerifier{}
		return
	}
	s.turnstileVerifier = verifier
}

func (s *Service) PublicConfig() contracts.AuthPublicConfig {
	cfg := s.RegistrationConfig()
	suffixes := append([]string{}, cfg.EmailSuffixWhitelist...)
	return contracts.AuthPublicConfig{
		RegistrationEnabled:              cfg.Enabled,
		RegistrationDefaultRole:          contracts.UserRoleOwner,
		RegistrationEmailSuffixWhitelist: suffixes,
		InvitationRequired:               cfg.InvitationRequired,
		TurnstileEnabled:                 cfg.TurnstileEnabled,
		TurnstileSiteKey:                 cfg.TurnstileSiteKey,
	}
}

func (s *Service) RegisterOwner(ctx context.Context, req contracts.AuthRegisterRequest, remoteIP string) (RegistrationResult, error) {
	cfg := s.RegistrationConfig()
	if !cfg.Enabled {
		return RegistrationResult{}, ErrRegistrationDisabled
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return RegistrationResult{}, fmt.Errorf("auth: valid email is required")
	}
	if len(req.Password) < 8 {
		return RegistrationResult{}, fmt.Errorf("auth: password must be at least 8 characters")
	}
	if !emailSuffixAllowed(email, cfg.EmailSuffixWhitelist) {
		return RegistrationResult{}, ErrEmailSuffixNotAllowed
	}
	if cfg.TurnstileEnabled {
		token := strings.TrimSpace(req.TurnstileToken)
		if token == "" {
			return RegistrationResult{}, ErrTurnstileRequired
		}
		if strings.TrimSpace(cfg.TurnstileSecretKey) == "" {
			return RegistrationResult{}, ErrTurnstileVerificationFailed
		}
		verifier := s.turnstileVerifier
		if verifier == nil {
			verifier = HTTPDefaultTurnstileVerifier{}
		}
		if err := verifier.Verify(ctx, cfg.TurnstileSecretKey, token, remoteIP, cfg.TurnstileSiteVerifyURL); err != nil {
			if errors.Is(err, ErrTurnstileVerificationFailed) {
				return RegistrationResult{}, ErrTurnstileVerificationFailed
			}
			return RegistrationResult{}, ErrTurnstileVerificationFailed
		}
	}

	invitationHash := ""
	if cfg.InvitationRequired {
		code := strings.TrimSpace(req.InvitationCode)
		if code == "" {
			return RegistrationResult{}, ErrInvitationRequired
		}
		invitationHash = contracts.HashRedeemCode(code)
		// Fail early so an invalid code never creates an account. The
		// authoritative consumption below still guards the race where two
		// registrations present the same code concurrently.
		existing, lookupErr := s.store.GetRedeemCodeByHash(ctx, invitationHash)
		if lookupErr != nil || existing.Type != contracts.RedeemCodeInvitation ||
			existing.Status != contracts.RedeemCodeUnused ||
			existing.ExpiresAt != nil && !existing.ExpiresAt.After(time.Now().UTC()) {
			return RegistrationResult{}, ErrInvitationCodeInvalid
		}
	}

	if _, err := s.store.GetUserByEmail(ctx, email); err == nil {
		return RegistrationResult{}, store.ErrDuplicate
	} else if !errors.Is(err, store.ErrNotFound) {
		return RegistrationResult{}, err
	}

	user, err := s.CreateUser(ctx, email, req.Password, strings.TrimSpace(req.DisplayName), []contracts.UserRole{contracts.UserRoleOwner})
	if err != nil {
		return RegistrationResult{}, err
	}
	if invitationHash != "" {
		if _, consumeErr := s.store.ConsumeInvitationCode(ctx, invitationHash, user.ID); consumeErr != nil {
			// The code was stolen between the precheck and consumption. The
			// account must not stay usable without a valid invitation.
			user.Enabled = false
			_, _ = s.store.UpdateUser(ctx, user)
			return RegistrationResult{}, ErrInvitationCodeInvalid
		}
	}
	token, expiresAt, err := s.issueSession(ctx, user)
	if err != nil {
		return RegistrationResult{}, err
	}
	return RegistrationResult{Token: token, User: user, ExpiresAt: expiresAt}, nil
}

func ParseRegistrationConfigFromEnv(lookup func(string) string) RegistrationConfig {
	return RegistrationConfig{
		Enabled:                parseBool(lookup("E2M_AUTH_REGISTRATION_ENABLED")),
		EmailSuffixWhitelist:   splitSuffixList(lookup("E2M_AUTH_REGISTRATION_EMAIL_SUFFIXES")),
		InvitationRequired:     parseBool(lookup("E2M_AUTH_INVITATION_REQUIRED")),
		TurnstileEnabled:       parseBool(lookup("E2M_AUTH_TURNSTILE_ENABLED")),
		TurnstileSiteKey:       strings.TrimSpace(lookup("E2M_AUTH_TURNSTILE_SITE_KEY")),
		TurnstileSecretKey:     strings.TrimSpace(lookup("E2M_AUTH_TURNSTILE_SECRET_KEY")),
		TurnstileSiteVerifyURL: strings.TrimSpace(lookup("E2M_AUTH_TURNSTILE_SITEVERIFY_URL")),
	}
}

func SystemSettingsFromRegistrationConfig(cfg RegistrationConfig) contracts.AuthSystemSettings {
	return contracts.AuthSystemSettings{
		RegistrationEnabled:              cfg.Enabled,
		RegistrationEmailSuffixWhitelist: normalizeEmailSuffixes(cfg.EmailSuffixWhitelist),
		InvitationRequired:               cfg.InvitationRequired,
		TurnstileEnabled:                 cfg.TurnstileEnabled,
		TurnstileSiteKey:                 strings.TrimSpace(cfg.TurnstileSiteKey),
		TurnstileSecretConfigured:        strings.TrimSpace(cfg.TurnstileSecretKey) != "",
		TurnstileSecretKey:               strings.TrimSpace(cfg.TurnstileSecretKey),
	}
}

func RegistrationConfigFromSystemSettings(settings contracts.AuthSystemSettings, base RegistrationConfig) RegistrationConfig {
	return RegistrationConfig{
		Enabled:                settings.RegistrationEnabled,
		EmailSuffixWhitelist:   normalizeEmailSuffixes(settings.RegistrationEmailSuffixWhitelist),
		InvitationRequired:     settings.InvitationRequired,
		TurnstileEnabled:       settings.TurnstileEnabled,
		TurnstileSiteKey:       strings.TrimSpace(settings.TurnstileSiteKey),
		TurnstileSecretKey:     strings.TrimSpace(settings.TurnstileSecretKey),
		TurnstileSiteVerifyURL: base.TurnstileSiteVerifyURL,
	}
}

func NormalizeEmailSuffixes(values []string) []string {
	return normalizeEmailSuffixes(values)
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func splitSuffixList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func normalizeEmailSuffixes(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		suffix := strings.ToLower(strings.TrimSpace(v))
		if suffix == "" {
			continue
		}
		if strings.HasPrefix(suffix, "@") || strings.HasPrefix(suffix, "*.") {
			// already normalized enough for our supported forms
		} else {
			suffix = "@" + suffix
		}
		if !seen[suffix] {
			seen[suffix] = true
			out = append(out, suffix)
		}
	}
	return out
}

func emailSuffixAllowed(email string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	_, domain, ok := strings.Cut(email, "@")
	if !ok || domain == "" {
		return false
	}
	for _, suffix := range whitelist {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		if strings.HasPrefix(suffix, "@") {
			if domain == strings.TrimPrefix(suffix, "@") {
				return true
			}
			continue
		}
		if strings.HasPrefix(suffix, "*.") {
			base := strings.TrimPrefix(suffix, "*.")
			if strings.HasSuffix(domain, "."+base) && domain != base {
				return true
			}
			continue
		}
		if domain == strings.TrimPrefix(suffix, "@") {
			return true
		}
	}
	return false
}

func RemoteIPFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}
