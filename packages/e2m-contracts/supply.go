package contracts

import "time"

// SupplyOfferKind classifies what an upstream channel supplies. It also selects
// the pool link: subscription OAuth accounts must be delivered per-account with
// a bound proxy and must never traverse a central egress, whereas API keys are
// written into an owner instance channel.
type SupplyOfferKind string

const (
	// SupplyOfferOAuthSubscription is a subscription-type OAuth account
	// (Claude / Codex / Grok) that requires one-account-one-IP consistency.
	SupplyOfferOAuthSubscription SupplyOfferKind = "oauth_subscription"
	// SupplyOfferAPIKey is a plain API key resource.
	SupplyOfferAPIKey SupplyOfferKind = "api_key"
)

// SupplyOfferStatus tracks an offer through its lifecycle.
type SupplyOfferStatus string

const (
	SupplyOfferStatusPending   SupplyOfferStatus = "pending"
	SupplyOfferStatusActive    SupplyOfferStatus = "active"
	SupplyOfferStatusExhausted SupplyOfferStatus = "exhausted"
	SupplyOfferStatusRevoked   SupplyOfferStatus = "revoked"
)

// SupplyLedgerEntryStatus tracks one allocation through its lifecycle.
type SupplyLedgerEntryStatus string

const (
	SupplyLedgerAllocated SupplyLedgerEntryStatus = "allocated"
	SupplyLedgerRevoked   SupplyLedgerEntryStatus = "revoked"
)

// SupplyLedgerEntry records one allocation of a supply offer to an owner
// instance — the two-sided marketplace's paper trail. Configuration delivery
// only: the credential itself moves via the gateway admin API, never through
// this record (which carries refs only).
type SupplyLedgerEntry struct {
	ID             string                  `json:"id"`
	OfferID        string                  `json:"offer_id"`
	SupplierUserID int64                   `json:"supplier_user_id"`
	UserID         int64                   `json:"user_id"`     // owner side
	InstanceID     string                  `json:"instance_id"` // target instance
	Status         SupplyLedgerEntryStatus `json:"status"`
	Note           string                  `json:"note,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
}

// SupplyOffer is one registered supply from a supplier account. The plaintext
// credential is never stored here; CredentialRef points into the vault, and
// ProxyRef points to the paired egress proxy for subscription accounts.
type SupplyOffer struct {
	ID             string            `json:"id"`
	SupplierUserID int64             `json:"supplier_user_id"`
	Kind           SupplyOfferKind   `json:"kind"`
	Provider       string            `json:"provider,omitempty"`
	CredentialRef  string            `json:"credential_ref"`
	ProxyRef       string            `json:"proxy_ref,omitempty"`
	Status         SupplyOfferStatus `json:"status"`
	Quota          int64             `json:"quota,omitempty"`
	UnitPrice      string            `json:"unit_price,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}
