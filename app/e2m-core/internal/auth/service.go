// Package auth implements console login, bearer-token sessions, and RBAC for
// the E2M Core API. Passwords are bcrypt-hashed; session tokens are random
// 256-bit values of which only the SHA-256 hash is stored. Authorization is
// fail-closed: non-admin users can only see and act on resources owned by their
// own user ID.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// SessionTTL is how long a login token stays valid.
const SessionTTL = 7 * 24 * time.Hour

var (
	// ErrInvalidCredentials covers unknown email, wrong password, disabled user.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrUnauthorized means no valid session was presented.
	ErrUnauthorized = errors.New("auth: unauthorized")
	// ErrForbidden means the session is valid but the role/user scope does not
	// allow the requested action.
	ErrForbidden = errors.New("auth: forbidden")
	// ErrSelfAdminLockout prevents an authenticated administrator from using
	// their own request to immediately remove their access.
	ErrSelfAdminLockout = errors.New("auth: administrators cannot disable or demote their own account")
)

// Service owns users and sessions.
type Service struct {
	store             store.Store
	now               func() time.Time
	registrationMu    sync.RWMutex
	registration      RegistrationConfig
	turnstileVerifier TurnstileVerifier
}

func NewService(st store.Store) *Service {
	return &Service{store: st, now: time.Now, turnstileVerifier: HTTPDefaultTurnstileVerifier{}}
}

// Bootstrap creates the initial platform admin when no users exist. Email and
// password come from E2M_ADMIN_EMAIL / E2M_ADMIN_PASSWORD; both must be set on
// first boot of an empty store, otherwise login is impossible.
func (s *Service) Bootstrap(ctx context.Context, email, password string) error {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("auth: count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	if strings.TrimSpace(email) == "" || password == "" {
		log.Printf("auth: no users exist and E2M_ADMIN_EMAIL/E2M_ADMIN_PASSWORD not set — console login unavailable")
		return nil
	}
	_, err = s.CreateUser(ctx, email, password, "初始管理员", []contracts.UserRole{contracts.UserRolePlatformAdmin})
	if err != nil {
		return fmt.Errorf("auth: bootstrap admin: %w", err)
	}
	log.Printf("auth: bootstrap platform admin %s created", email)
	return nil
}

// CreateUser hashes the password and stores the user. Tests and seed paths may
// pass an explicit numeric ID; normal callers let the store assign one.
func (s *Service) CreateUser(ctx context.Context, email, password, displayName string, roles []contracts.UserRole, idOverride ...int64) (contracts.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return contracts.User{}, fmt.Errorf("auth: valid email is required")
	}
	if len(password) < 8 {
		return contracts.User{}, fmt.Errorf("auth: password must be at least 8 characters")
	}
	roles, err := NormalizeRoles(roles)
	if err != nil {
		return contracts.User{}, err
	}
	if hasRole(roles, contracts.UserRoleAdmin) && len(roles) > 1 {
		return contracts.User{}, fmt.Errorf("auth: admin cannot be combined with business roles")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return contracts.User{}, err
	}
	input := contracts.User{
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Roles:        roles,
		Enabled:      true,
	}
	if len(idOverride) > 0 {
		input.ID = idOverride[0]
	}
	return s.store.CreateUser(ctx, input)
}

// UpdateUser replaces the editable fields of a console user. Store
// implementations enforce the final-enabled-admin invariant atomically.
func (s *Service) UpdateUser(ctx context.Context, actorUserID, targetUserID int64, input contracts.UpdateUserRequest) (contracts.User, error) {
	if input.ExpectedUpdatedAt.IsZero() {
		return contracts.User{}, fmt.Errorf("auth: expected_updated_at is required")
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" || !strings.Contains(email, "@") {
		return contracts.User{}, fmt.Errorf("auth: valid email is required")
	}
	roles, err := NormalizeRoles(input.Roles)
	if err != nil {
		return contracts.User{}, err
	}
	if hasRole(roles, contracts.UserRoleAdmin) && len(roles) > 1 {
		return contracts.User{}, fmt.Errorf("auth: admin cannot be combined with business roles")
	}
	if actorUserID == targetUserID && (!input.Enabled || !hasRole(roles, contracts.UserRoleAdmin)) {
		return contracts.User{}, ErrSelfAdminLockout
	}
	current, err := s.store.GetUser(ctx, targetUserID)
	if err != nil {
		return contracts.User{}, err
	}
	platformConcurrency, platformRPM := current.PlatformConcurrency, current.PlatformRPM
	if input.PlatformConcurrency != nil {
		if *input.PlatformConcurrency < 0 || *input.PlatformConcurrency > 1_000_000 {
			return contracts.User{}, fmt.Errorf("auth: platform_concurrency must be between 0 and 1000000")
		}
		platformConcurrency = *input.PlatformConcurrency
	}
	if input.PlatformRPM != nil {
		if *input.PlatformRPM < 0 || *input.PlatformRPM > 1_000_000 {
			return contracts.User{}, fmt.Errorf("auth: platform_rpm must be between 0 and 1000000")
		}
		platformRPM = *input.PlatformRPM
	}

	return s.store.UpdateUser(ctx, contracts.User{
		ID:                  targetUserID,
		Email:               email,
		DisplayName:         strings.TrimSpace(input.DisplayName),
		Roles:               roles,
		Enabled:             input.Enabled,
		PlatformConcurrency: platformConcurrency,
		PlatformRPM:         platformRPM,
		UpdatedAt:           input.ExpectedUpdatedAt,
	})
}

// ResetUserPassword hashes the replacement password and atomically revokes all
// existing sessions for the target user.
func (s *Service) ResetUserPassword(ctx context.Context, targetUserID int64, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("auth: password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.store.UpdateUserPasswordHash(ctx, targetUserID, string(hash))
}

func NormalizeRoles(roles []contracts.UserRole) ([]contracts.UserRole, error) {
	seen := map[contracts.UserRole]bool{}
	out := make([]contracts.UserRole, 0, len(roles))
	for _, role := range roles {
		normalized, ok := NormalizeRole(role)
		if !ok {
			return nil, fmt.Errorf("auth: role must be admin, client, or supplier")
		}
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("auth: at least one role is required")
	}
	return out, nil
}

func NormalizeRole(role contracts.UserRole) (contracts.UserRole, bool) {
	switch role {
	case contracts.UserRoleAdmin, contracts.UserRole("platform_admin"):
		return contracts.UserRoleAdmin, true
	case contracts.UserRoleClient, contracts.UserRole("owner"):
		return contracts.UserRoleClient, true
	case contracts.UserRoleSupplier:
		return contracts.UserRoleSupplier, true
	default:
		return "", false
	}
}

func NormalizeRoleStrings(roles []string) []contracts.UserRole {
	out := make([]contracts.UserRole, 0, len(roles))
	seen := map[contracts.UserRole]bool{}
	for _, role := range roles {
		if normalized, ok := NormalizeRole(contracts.UserRole(role)); ok {
			if !seen[normalized] {
				seen[normalized] = true
				out = append(out, normalized)
			}
		}
	}
	return out
}

func hasRole(roles []contracts.UserRole, role contracts.UserRole) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// Login verifies credentials and issues a bearer token. The returned token is
// shown to the client once; only its hash is persisted.
func (s *Service) Login(ctx context.Context, email, password string) (string, contracts.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Equalize timing between unknown-email and wrong-password paths.
			_ = bcrypt.CompareHashAndPassword(
				[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(password))
			return "", contracts.User{}, ErrInvalidCredentials
		}
		return "", contracts.User{}, err
	}
	if !user.Enabled {
		return "", contracts.User{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", contracts.User{}, ErrInvalidCredentials
	}

	token, _, err := s.issueSession(ctx, user)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "", contracts.User{}, ErrInvalidCredentials
		}
		return "", contracts.User{}, err
	}
	return token, user, nil
}

// VerifyPassword re-authenticates one already-identified user without issuing
// or extending a session. Sensitive one-shot operations use it immediately
// before returning protected material.
func (s *Service) VerifyPassword(ctx context.Context, userID int64, password string) error {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = bcrypt.CompareHashAndPassword(
				[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(password))
			return ErrInvalidCredentials
		}
		return err
	}
	if !user.Enabled || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return ErrInvalidCredentials
	}
	return nil
}

func (s *Service) issueSession(ctx context.Context, user contracts.User) (string, time.Time, error) {
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.now().UTC().Add(SessionTTL)
	sess := contracts.Session{
		TokenHash: hashToken(token),
		UserID:    user.ID,
		ExpiresAt: expiresAt,
	}
	if err := s.store.CreateSession(ctx, sess, user); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Logout revokes the presented token. Unknown tokens are a no-op.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashToken(token))
}

// Authenticate resolves a bearer token to its user. Expired sessions are
// deleted on sight.
func (s *Service) Authenticate(ctx context.Context, token string) (contracts.User, error) {
	if token == "" {
		return contracts.User{}, ErrUnauthorized
	}
	sess, err := s.store.GetSession(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return contracts.User{}, ErrUnauthorized
		}
		return contracts.User{}, err
	}
	if s.now().UTC().After(sess.ExpiresAt) {
		_ = s.store.DeleteSession(ctx, sess.TokenHash)
		return contracts.User{}, ErrUnauthorized
	}
	user, err := s.store.GetUser(ctx, sess.UserID)
	if err != nil {
		return contracts.User{}, ErrUnauthorized
	}
	if !user.Enabled {
		return contracts.User{}, ErrUnauthorized
	}
	return user, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "e2m_" + hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
