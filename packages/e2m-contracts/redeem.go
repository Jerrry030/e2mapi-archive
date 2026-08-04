package contracts

import "time"

// RedeemCodeType enumerates what a code grants. Balance codes credit the
// platform wallet; invitation codes gate self-serve registration.
type RedeemCodeType string

const (
	RedeemCodeBalance    RedeemCodeType = "balance"
	RedeemCodeInvitation RedeemCodeType = "invitation"
)

func (t RedeemCodeType) Valid() bool {
	return t == RedeemCodeBalance || t == RedeemCodeInvitation
}

type RedeemCodeStatus string

const (
	RedeemCodeUnused   RedeemCodeStatus = "unused"
	RedeemCodeUsed     RedeemCodeStatus = "used"
	RedeemCodeDisabled RedeemCodeStatus = "disabled"
	RedeemCodeExpired  RedeemCodeStatus = "expired"
)

func (s RedeemCodeStatus) Valid() bool {
	switch s {
	case RedeemCodeUnused, RedeemCodeUsed, RedeemCodeDisabled, RedeemCodeExpired:
		return true
	default:
		return false
	}
}

// RedeemCode is a bearer instrument. The database keeps only the SHA-256 hash
// of the plaintext code plus a short display prefix; the plaintext exists in
// exactly one place — the generation response.
type RedeemCode struct {
	ID           string           `json:"id"`
	Type         RedeemCodeType   `json:"type"`
	CodeHash     string           `json:"-"`
	CodePrefix   string           `json:"code_prefix"`
	Currency     string           `json:"currency"`
	AmountMicros int64            `json:"amount_micros"`
	Status       RedeemCodeStatus `json:"status"`
	BatchID      string           `json:"batch_id"`
	Notes        string           `json:"notes,omitempty"`
	ExpiresAt    *time.Time       `json:"expires_at,omitempty"`
	UsedBy       int64            `json:"used_by,omitempty"`
	UsedAt       *time.Time       `json:"used_at,omitempty"`
	CreatedBy    int64            `json:"created_by"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type RedeemCodeFilter struct {
	Type     RedeemCodeType
	Status   RedeemCodeStatus
	BatchID  string
	Page     int
	PageSize int
}

type RedeemCodePage struct {
	Items    []RedeemCode `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

// GenerateRedeemCodesRequest is the admin batch-generation input.
type GenerateRedeemCodesRequest struct {
	Type      RedeemCodeType `json:"type"`
	Count     int            `json:"count"`
	Amount    string         `json:"amount,omitempty"`
	Currency  string         `json:"currency,omitempty"`
	Notes     string         `json:"notes,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
}

// GenerateRedeemCodesResponse returns the plaintext codes exactly once.
type GenerateRedeemCodesResponse struct {
	BatchID string       `json:"batch_id"`
	Codes   []string     `json:"codes"`
	Items   []RedeemCode `json:"items"`
}

type RedeemRequest struct {
	Code string `json:"code"`
}

type RedeemResponse struct {
	Type         RedeemCodeType `json:"type"`
	AmountMicros int64          `json:"amount_micros"`
	Currency     string         `json:"currency"`
	Wallet       Wallet         `json:"wallet"`
}

// CreateAndRedeemRequest lets an external fulfillment system (or an operator
// tool) atomically mint one balance code and redeem it to a user. Replays with
// the same Idempotency-Key return the original result.
type CreateAndRedeemRequest struct {
	UserID   int64  `json:"user_id"`
	Amount   string `json:"amount"`
	Currency string `json:"currency,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

type CreateAndRedeemResponse struct {
	Code   RedeemCode `json:"code"`
	Wallet Wallet     `json:"wallet"`
	Replay bool       `json:"replay"`
}
