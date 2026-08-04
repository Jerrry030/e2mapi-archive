package contracts

import "time"

// UserRole is the console RBAC role. A user may hold multiple business roles on
// their own account, e.g. both client and supplier.
type UserRole string

const (
	// UserRoleAdmin is the E2M operator: full access, user management.
	UserRoleAdmin UserRole = "admin"
	// UserRoleClient manages the client-side gateway instances and onboarding loop.
	UserRoleClient UserRole = "client"
	// UserRoleSupplier manages upstream supply offers for its own account.
	UserRoleSupplier UserRole = "supplier"

	// Deprecated aliases retained for code paths that still use the old names
	// for owner-side resource concepts rather than persisted role values.
	UserRolePlatformAdmin = UserRoleAdmin
	UserRoleOwner         = UserRoleClient
)

// UserDeactivationStatus is the durable two-phase client-service teardown
// state. Login/client authorization is removed before gateway routes are
// drained; Connector identity is revoked only after every binding receipt is
// confirmed revoked.
type UserDeactivationStatus string

const (
	UserDeactivationNone      UserDeactivationStatus = "none"
	UserDeactivationDraining  UserDeactivationStatus = "draining"
	UserDeactivationFailed    UserDeactivationStatus = "failed"
	UserDeactivationCompleted UserDeactivationStatus = "completed"
)

func (s UserDeactivationStatus) InProgress() bool {
	return s == UserDeactivationDraining || s == UserDeactivationFailed
}

// User is a console login. PasswordHash is bcrypt and never serialized.
type User struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name,omitempty"`
	PasswordHash string     `json:"-"`
	Roles        []UserRole `json:"roles"`
	// PlatformConcurrency caps the user's concurrently reserved platform
	// requests; PlatformRPM caps reservations per rolling minute. Zero means
	// unlimited. Both apply to the E2M platform data plane only.
	PlatformConcurrency     int                    `json:"platform_concurrency,omitempty"`
	PlatformRPM             int                    `json:"platform_rpm,omitempty"`
	Enabled                 bool                   `json:"enabled"`
	DeactivationStatus      UserDeactivationStatus `json:"deactivation_status"`
	DeactivationErrorCode   string                 `json:"deactivation_error_code,omitempty"`
	DeactivationRequestedAt *time.Time             `json:"deactivation_requested_at,omitempty"`
	DeactivationCompletedAt *time.Time             `json:"deactivation_completed_at,omitempty"`
	CreatedAt               time.Time              `json:"created_at"`
	UpdatedAt               time.Time              `json:"updated_at"`
}

// UpdateUserRequest is the platform-admin payload for replacing a user's
// editable identity, role, and login-state fields. Password changes use the
// dedicated reset endpoint so credential material is never returned alongside
// ordinary profile updates.
type UpdateUserRequest struct {
	Email             string     `json:"email"`
	DisplayName       string     `json:"display_name"`
	Roles             []UserRole `json:"roles"`
	Enabled           bool       `json:"enabled"`
	ExpectedUpdatedAt time.Time  `json:"expected_updated_at"`
	// Nil keeps the current limit; zero clears it (unlimited).
	PlatformConcurrency *int `json:"platform_concurrency,omitempty"`
	PlatformRPM         *int `json:"platform_rpm,omitempty"`
}

// ResetUserPasswordRequest replaces a user's password and revokes all of that
// user's existing sessions.
type ResetUserPasswordRequest struct {
	Password string `json:"password"`
}

// Session is one issued login token. Only the SHA-256 hash of the token is
// stored, so a database leak does not leak usable sessions.
type Session struct {
	TokenHash string    `json:"-"`
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// AuthPublicConfig is safe to expose on public login/register pages. It must
// never include Turnstile secrets or other private operator configuration.
type AuthPublicConfig struct {
	RegistrationEnabled              bool     `json:"registration_enabled"`
	RegistrationDefaultRole          UserRole `json:"registration_default_role"`
	RegistrationEmailSuffixWhitelist []string `json:"registration_email_suffix_whitelist"`
	InvitationRequired               bool     `json:"invitation_required"`
	TurnstileEnabled                 bool     `json:"turnstile_enabled"`
	TurnstileSiteKey                 string   `json:"turnstile_site_key"`
}

// AuthRegisterRequest is the public client self-registration payload. The server
// ignores any role-like input; self-registration always creates one client-role
// account whose ID becomes the resource owner key.
type AuthRegisterRequest struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	DisplayName    string `json:"display_name,omitempty"`
	TurnstileToken string `json:"turnstile_token,omitempty"`
	InvitationCode string `json:"invitation_code,omitempty"`
}

// AuthRegisterResponse mirrors the login response.
type AuthRegisterResponse struct {
	Token     string    `json:"token"`
	User      User      `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthSystemSettings is the platform-admin view of login/registration settings.
// The Turnstile secret is carried only inside the backend/store boundary and is
// never serialized to clients.
type AuthSystemSettings struct {
	RegistrationEnabled              bool      `json:"registration_enabled"`
	RegistrationEmailSuffixWhitelist []string  `json:"registration_email_suffix_whitelist"`
	InvitationRequired               bool      `json:"invitation_required"`
	TurnstileEnabled                 bool      `json:"turnstile_enabled"`
	TurnstileSiteKey                 string    `json:"turnstile_site_key"`
	TurnstileSecretConfigured        bool      `json:"turnstile_secret_configured"`
	TurnstileSecretKey               string    `json:"-"`
	UpdatedAt                        time.Time `json:"updated_at,omitempty"`
}

// UpdateAuthSystemSettingsRequest updates the public registration and Turnstile
// policy. Omitted/empty turnstile_secret_key keeps the existing secret unless
// clear_turnstile_secret is true.
type UpdateAuthSystemSettingsRequest struct {
	RegistrationEnabled              bool     `json:"registration_enabled"`
	RegistrationEmailSuffixWhitelist []string `json:"registration_email_suffix_whitelist"`
	InvitationRequired               bool     `json:"invitation_required"`
	TurnstileEnabled                 bool     `json:"turnstile_enabled"`
	TurnstileSiteKey                 string   `json:"turnstile_site_key"`
	TurnstileSecretKey               *string  `json:"turnstile_secret_key,omitempty"`
	ClearTurnstileSecret             bool     `json:"clear_turnstile_secret,omitempty"`
}
