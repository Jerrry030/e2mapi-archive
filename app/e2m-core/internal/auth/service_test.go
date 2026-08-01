package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func newSvc(t *testing.T) (*Service, store.Store) {
	t.Helper()
	st := store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	return NewService(st), st
}

func TestBootstrapCreatesFirstAdminOnce(t *testing.T) {
	ctx := context.Background()
	svc, st := newSvc(t)

	if err := svc.Bootstrap(ctx, "admin@e2m.local", "changeme123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	n, _ := st.CountUsers(ctx)
	if n != 1 {
		t.Fatalf("expected 1 user after bootstrap, got %d", n)
	}
	u, err := st.GetUserByEmail(ctx, "admin@e2m.local")
	if err != nil || !HasRole(u, contracts.UserRolePlatformAdmin) {
		t.Fatalf("expected platform_admin, got %+v err=%v", u, err)
	}

	// Second bootstrap is a no-op even with different credentials.
	if err := svc.Bootstrap(ctx, "other@e2m.local", "changeme123"); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	n, _ = st.CountUsers(ctx)
	if n != 1 {
		t.Fatalf("bootstrap must not create a second user, got %d", n)
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	if _, err := svc.CreateUser(ctx, "op@e2m.local", "password123", "Op", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, user, err := svc.Login(ctx, "op@e2m.local", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token == "" || user.Email != "op@e2m.local" {
		t.Fatalf("unexpected login result token=%q user=%+v", token, user)
	}

	got, err := svc.Authenticate(ctx, token)
	if err != nil || got.ID != user.ID {
		t.Fatalf("authenticate: %v got=%+v", err, got)
	}

	// Wrong password fails.
	if _, _, err := svc.Login(ctx, "op@e2m.local", "wrong-password"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	// Unknown email fails identically.
	if _, _, err := svc.Login(ctx, "ghost@e2m.local", "password123"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for unknown email, got %v", err)
	}

	// Logout revokes the session.
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized after logout, got %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	if _, err := svc.CreateUser(ctx, "op@e2m.local", "password123", "Op", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, _, err := svc.Login(ctx, "op@e2m.local", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// Move the clock past the TTL.
	svc.now = func() time.Time { return time.Now().Add(SessionTTL + time.Hour) }
	if _, err := svc.Authenticate(ctx, token); err != ErrUnauthorized {
		t.Fatalf("expected expiry, got %v", err)
	}
}

func TestDuplicateEmailRejected(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	if _, err := svc.CreateUser(ctx, "dup@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.CreateUser(ctx, "dup@e2m.local", "password456", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err == nil {
		t.Fatal("expected duplicate email to fail")
	}
}

func TestCreateUserRejectsRemovedViewerRole(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	if _, err := svc.CreateUser(ctx, "viewer@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRole("viewer")}); err == nil {
		t.Fatal("expected viewer role to be rejected")
	}
}

func TestCreateUserCanSetExplicitUserID(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	user, err := svc.CreateUser(ctx, "owner@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRoleOwner}, 101)
	if err != nil {
		t.Fatalf("create explicit-id user: %v", err)
	}
	if user.ID != 101 {
		t.Fatalf("expected explicit id 101, got %d", user.ID)
	}
}

func TestUpdateUserAndResetPasswordSecurity(t *testing.T) {
	ctx := context.Background()
	svc, st := newSvc(t)
	admin, err := svc.CreateUser(ctx, "admin@example.com", "password123", "Admin", []contracts.UserRole{contracts.UserRoleAdmin})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	target, err := svc.CreateUser(ctx, "target@example.com", "password123", "Target", []contracts.UserRole{contracts.UserRoleClient})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	token, _, err := svc.Login(ctx, target.Email, "password123")
	if err != nil {
		t.Fatalf("target login: %v", err)
	}

	updated, err := svc.UpdateUser(ctx, admin.ID, target.ID, contracts.UpdateUserRequest{
		Email: " TARGET.NEW@Example.COM ", DisplayName: " New Name ",
		Roles: []contracts.UserRole{contracts.UserRoleSupplier}, Enabled: true,
		ExpectedUpdatedAt: target.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("update target: %v", err)
	}
	if updated.Email != "target.new@example.com" || updated.DisplayName != "New Name" ||
		!HasRole(updated, contracts.UserRoleSupplier) {
		t.Fatalf("unexpected updated user: %+v", updated)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("role change must revoke session, got %v", err)
	}

	token, _, err = svc.Login(ctx, updated.Email, "password123")
	if err != nil {
		t.Fatalf("login before reset: %v", err)
	}
	if err := svc.ResetUserPassword(ctx, target.ID, "replacement123"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("password reset must revoke session, got %v", err)
	}
	if _, _, err := svc.Login(ctx, updated.Email, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password login error=%v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, updated.Email, "replacement123"); err != nil {
		t.Fatalf("new password login: %v", err)
	}

	if _, err := svc.UpdateUser(ctx, admin.ID, admin.ID, contracts.UpdateUserRequest{
		Email: admin.Email, DisplayName: admin.DisplayName,
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
		ExpectedUpdatedAt: admin.UpdatedAt,
	}); !errors.Is(err, ErrSelfAdminLockout) {
		t.Fatalf("self demotion error=%v, want ErrSelfAdminLockout", err)
	}
	storedAdmin, err := st.GetUser(ctx, admin.ID)
	if err != nil || !HasRole(storedAdmin, contracts.UserRoleAdmin) {
		t.Fatalf("self-demotion changed admin: %+v err=%v", storedAdmin, err)
	}
}

func TestScopeUser(t *testing.T) {
	platform := contracts.User{Roles: []contracts.UserRole{contracts.UserRolePlatformAdmin}}
	user := contracts.User{ID: 101, Roles: []contracts.UserRole{contracts.UserRoleOwner}}

	if got, err := ScopeUser(platform, 909); err != nil || got != 909 {
		t.Fatalf("platform passthrough: got %d err=%v", got, err)
	}
	if got, err := ScopeUser(user, 0); err != nil || got != 101 {
		t.Fatalf("user default pin: got %d err=%v", got, err)
	}
	if got, err := ScopeUser(user, 101); err != nil || got != 101 {
		t.Fatalf("same-user scope: got %d err=%v", got, err)
	}
	if _, err := ScopeUser(user, 102); err != ErrForbidden {
		t.Fatalf("cross-user must be forbidden, got %v", err)
	}
	if _, err := ScopeUser(contracts.User{Roles: []contracts.UserRole{contracts.UserRoleOwner}}, 0); err != ErrForbidden {
		t.Fatalf("business role without user id must be forbidden, got %v", err)
	}
}

func TestBusinessRoleAccess(t *testing.T) {
	platform := contracts.User{Roles: []contracts.UserRole{contracts.UserRolePlatformAdmin}}
	owner := contracts.User{ID: 101, Roles: []contracts.UserRole{contracts.UserRoleOwner}}
	supplier := contracts.User{ID: 101, Roles: []contracts.UserRole{contracts.UserRoleSupplier}}
	both := contracts.User{ID: 101, Roles: []contracts.UserRole{contracts.UserRoleOwner, contracts.UserRoleSupplier}}

	if !CanWriteOwnerUser(platform, 909) || !CanWriteSupplierUser(platform, 909) {
		t.Fatal("platform admin must write anywhere")
	}
	if !CanWriteOwnerUser(owner, 101) || CanWriteOwnerUser(owner, 102) || CanWriteOwnerUser(owner, 0) {
		t.Fatal("owner writes only owner resources inside own account")
	}
	if CanWriteSupplierUser(owner, 101) {
		t.Fatal("owner role must not imply supplier write")
	}
	if !CanWriteSupplierUser(supplier, 101) || CanWriteSupplierUser(supplier, 102) || CanWriteOwnerUser(supplier, 101) {
		t.Fatal("supplier writes only supplier resources inside own account")
	}
	if !CanWriteOwnerUser(both, 101) || !CanWriteSupplierUser(both, 101) {
		t.Fatal("owner+supplier account should access both business surfaces")
	}
	if _, err := ScopeOwnerUser(supplier, 0); err != ErrForbidden {
		t.Fatalf("supplier-only user must not scope owner resources, got %v", err)
	}
	if got, err := ScopeSupplierUser(both, 0); err != nil || got != 101 {
		t.Fatalf("multi-role supplier scope: got %d err=%v", got, err)
	}
}

type stubTurnstileVerifier struct {
	err    error
	calls  int
	secret string
	token  string
	ip     string
}

func (v *stubTurnstileVerifier) Verify(_ context.Context, secret, token, remoteIP, _ string) error {
	v.calls++
	v.secret = secret
	v.token = token
	v.ip = remoteIP
	return v.err
}

func TestPublicConfigDefaultsClosed(t *testing.T) {
	svc, _ := newSvc(t)
	cfg := svc.PublicConfig()
	if cfg.RegistrationEnabled || cfg.TurnstileEnabled || cfg.TurnstileSiteKey != "" {
		t.Fatalf("registration must default closed and secretless, got %+v", cfg)
	}
	if cfg.RegistrationDefaultRole != contracts.UserRoleOwner {
		t.Fatalf("default role must be owner, got %q", cfg.RegistrationDefaultRole)
	}
}

func TestRegisterOwnerDisabled(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.RegisterOwner(context.Background(), contracts.AuthRegisterRequest{
		Email:       "owner@example.com",
		Password:    "password123",
		DisplayName: "Owner",
	}, "")
	if err != ErrRegistrationDisabled {
		t.Fatalf("expected ErrRegistrationDisabled, got %v", err)
	}
}

func TestRegisterOwnerSuccessCreatesOwnerRoleSession(t *testing.T) {
	ctx := context.Background()
	svc, st := newSvc(t)
	svc.ConfigureRegistration(RegistrationConfig{Enabled: true})
	res, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{
		Email:       " Owner@Example.COM ",
		Password:    "password123",
		DisplayName: "Owner",
	}, "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if res.Token == "" || res.ExpiresAt.IsZero() {
		t.Fatalf("expected issued session, got token=%q expires=%v", res.Token, res.ExpiresAt)
	}
	if res.User.Email != "owner@example.com" || res.User.DisplayName != "Owner" || !HasRole(res.User, contracts.UserRoleOwner) || res.User.ID == 0 {
		t.Fatalf("unexpected user: %+v", res.User)
	}
	got, err := svc.Authenticate(ctx, res.Token)
	if err != nil || got.ID != res.User.ID {
		t.Fatalf("registered token must authenticate: got=%+v err=%v", got, err)
	}
	users, _ := st.ListUsers(ctx)
	found := false
	for _, user := range users {
		if user.ID == res.User.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created user not persisted: %+v", res.User)
	}
}

func TestRegisterOwnerDuplicateEmailRejected(t *testing.T) {
	ctx := context.Background()
	svc, st := newSvc(t)
	svc.ConfigureRegistration(RegistrationConfig{Enabled: true})
	if _, err := svc.CreateUser(ctx, "dup@example.com", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	before, _ := st.ListUsers(ctx)
	_, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{Email: "dup@example.com", Password: "password123", DisplayName: "Dup"}, "")
	if err != store.ErrDuplicate {
		t.Fatalf("expected duplicate, got %v", err)
	}
	after, _ := st.ListUsers(ctx)
	if len(after) != len(before) {
		t.Fatalf("duplicate registration must not create user: before=%d after=%d", len(before), len(after))
	}
}

func TestRegisterOwnerEmailSuffixWhitelist(t *testing.T) {
	svc, _ := newSvc(t)
	svc.ConfigureRegistration(RegistrationConfig{Enabled: true, EmailSuffixWhitelist: []string{"example.com", "*.edu.cn"}})
	cfg := svc.PublicConfig()
	want := []string{"@example.com", "*.edu.cn"}
	if len(cfg.RegistrationEmailSuffixWhitelist) != len(want) {
		t.Fatalf("unexpected whitelist: %+v", cfg.RegistrationEmailSuffixWhitelist)
	}
	for i := range want {
		if cfg.RegistrationEmailSuffixWhitelist[i] != want[i] {
			t.Fatalf("unexpected whitelist: %+v", cfg.RegistrationEmailSuffixWhitelist)
		}
	}
	if !emailSuffixAllowed("a@example.com", cfg.RegistrationEmailSuffixWhitelist) {
		t.Fatal("expected exact domain suffix to pass")
	}
	if !emailSuffixAllowed("a@school.edu.cn", cfg.RegistrationEmailSuffixWhitelist) {
		t.Fatal("expected wildcard subdomain suffix to pass")
	}
	if emailSuffixAllowed("a@edu.cn", cfg.RegistrationEmailSuffixWhitelist) {
		t.Fatal("wildcard must not match bare domain")
	}
	_, err := svc.RegisterOwner(context.Background(), contracts.AuthRegisterRequest{Email: "a@blocked.test", Password: "password123", DisplayName: "Blocked"}, "")
	if err != ErrEmailSuffixNotAllowed {
		t.Fatalf("expected suffix rejection, got %v", err)
	}
}

func TestRegisterOwnerTurnstileSwitchAndFailure(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	verifier := &stubTurnstileVerifier{}
	svc.SetTurnstileVerifier(verifier)
	svc.ConfigureRegistration(RegistrationConfig{Enabled: true, TurnstileEnabled: false, TurnstileSecretKey: "secret"})
	if _, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{Email: "off@example.com", Password: "password123", DisplayName: "Off"}, "198.51.100.10"); err != nil {
		t.Fatalf("turnstile disabled register: %v", err)
	}
	if verifier.calls != 0 {
		t.Fatalf("turnstile verifier called while disabled")
	}

	svc.ConfigureRegistration(RegistrationConfig{Enabled: true, TurnstileEnabled: true, TurnstileSecretKey: "secret"})
	if _, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{Email: "missing@example.com", Password: "password123", DisplayName: "Missing"}, ""); err != ErrTurnstileRequired {
		t.Fatalf("expected missing token error, got %v", err)
	}
	verifier.err = ErrTurnstileVerificationFailed
	if _, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{Email: "fail@example.com", Password: "password123", DisplayName: "Fail", TurnstileToken: "bad-token"}, "198.51.100.10"); err != ErrTurnstileVerificationFailed {
		t.Fatalf("expected verification failure, got %v", err)
	}
	if verifier.calls != 1 || verifier.secret != "secret" || verifier.token != "bad-token" || verifier.ip != "198.51.100.10" {
		t.Fatalf("unexpected verifier call: %+v", verifier)
	}
	verifier.err = nil
	if _, err := svc.RegisterOwner(ctx, contracts.AuthRegisterRequest{Email: "ok@example.com", Password: "password123", DisplayName: "OK", TurnstileToken: "good-token"}, "198.51.100.11"); err != nil {
		t.Fatalf("expected turnstile success registration, got %v", err)
	}
	if verifier.calls != 2 || verifier.token != "good-token" || verifier.ip != "198.51.100.11" {
		t.Fatalf("unexpected successful verifier call: %+v", verifier)
	}
}

func TestParseRegistrationConfigFromEnv(t *testing.T) {
	cfg := ParseRegistrationConfigFromEnv(func(key string) string {
		values := map[string]string{
			"E2M_AUTH_REGISTRATION_ENABLED":        "true",
			"E2M_AUTH_REGISTRATION_EMAIL_SUFFIXES": "example.com, *.edu.cn",
			"E2M_AUTH_TURNSTILE_ENABLED":           "1",
			"E2M_AUTH_TURNSTILE_SITE_KEY":          "site-key",
			"E2M_AUTH_TURNSTILE_SECRET_KEY":        "secret-key",
		}
		return values[key]
	})
	if !cfg.Enabled || !cfg.TurnstileEnabled || cfg.TurnstileSiteKey != "site-key" || cfg.TurnstileSecretKey != "secret-key" {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
}
