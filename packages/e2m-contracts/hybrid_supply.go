package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ResourceClass is the stable three-resource boundary presented to an owner.
// Owner resources stay in the owner's Connector-managed gateway. Economy and
// stable resources are E2M-managed commercial supply served through the
// independent Supply Gateway.
type ResourceClass string

const (
	ResourceClassOwner   ResourceClass = "owner"
	ResourceClassEconomy ResourceClass = "economy"
	ResourceClassStable  ResourceClass = "stable"
)

func (c ResourceClass) Valid() bool {
	switch c {
	case ResourceClassOwner, ResourceClassEconomy, ResourceClassStable:
		return true
	default:
		return false
	}
}

// IsPlatformSupply reports whether the resource is owned and metered by E2M.
func (c ResourceClass) IsPlatformSupply() bool {
	return c == ResourceClassEconomy || c == ResourceClassStable
}

// NormalizePlatformResourceClass keeps pre-Hybrid-Supply pools compatible by
// treating their empty class as stable. Owner resources are never valid
// platform pools and therefore remain invalid after normalization.
func NormalizePlatformResourceClass(value ResourceClass) ResourceClass {
	if value == "" {
		return ResourceClassStable
	}
	return value
}

const HybridAllocationBasisRequests = "requests"

// HybridAllocationRule is a request-count allocation. Burst maxima are
// owner-approved elastic ceilings, not targets. A zero maximum is normalized
// to its matching target so old clients can opt out of elasticity by omission.
type HybridAllocationRule struct {
	OwnerPercent    int `json:"owner_percent"`
	EconomyPercent  int `json:"economy_percent"`
	StablePercent   int `json:"stable_percent"`
	OwnerBurstMax   int `json:"owner_burst_max"`
	EconomyBurstMax int `json:"economy_burst_max"`
	StableBurstMax  int `json:"stable_burst_max"`
}

func (r HybridAllocationRule) Normalize() HybridAllocationRule {
	if r.OwnerBurstMax == 0 {
		r.OwnerBurstMax = r.OwnerPercent
	}
	if r.EconomyBurstMax == 0 {
		r.EconomyBurstMax = r.EconomyPercent
	}
	if r.StableBurstMax == 0 {
		r.StableBurstMax = r.StablePercent
	}
	return r
}

func (r HybridAllocationRule) Valid() bool {
	r = r.Normalize()
	return r.OwnerPercent >= 0 && r.EconomyPercent >= 0 && r.StablePercent >= 0 &&
		r.OwnerPercent+r.EconomyPercent+r.StablePercent == 100 &&
		r.OwnerBurstMax >= r.OwnerPercent && r.OwnerBurstMax <= 100 &&
		r.EconomyBurstMax >= r.EconomyPercent && r.EconomyBurstMax <= 100 &&
		r.StableBurstMax >= r.StablePercent && r.StableBurstMax <= 100
}

// HybridModelAllocation overrides only the base allocation for one exact model.
// Price/speed/success ranking remains a future overlay and is deliberately not
// encoded here.
type HybridModelAllocation struct {
	Model string               `json:"model"`
	Rule  HybridAllocationRule `json:"rule"`
}

// HybridAllocation is the owner's base traffic intent for one managed
// instance. V1 measures allocation by request count. DailyBudgetMicros and
// MaxUnitPriceMicros cap only E2M economy/stable traffic; owner traffic is
// never charged by E2M.
type HybridAllocation struct {
	UserID             int64                   `json:"user_id"`
	InstanceID         string                  `json:"instance_id"`
	Basis              string                  `json:"basis"`
	DefaultRule        HybridAllocationRule    `json:"default_rule"`
	ModelOverrides     []HybridModelAllocation `json:"model_overrides"`
	DailyBudgetMicros  int64                   `json:"daily_budget_micros"`
	MaxUnitPriceMicros int64                   `json:"max_unit_price_micros"`
	// RoutingGeneration is the current execution generation for this instance.
	// Owner allocation updates preserve it; only the routing execution store may
	// advance it after checking that no remote mutation permit is executing.
	RoutingGeneration int64     `json:"routing_generation"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (a HybridAllocation) Normalize() HybridAllocation {
	if a.Basis == "" {
		a.Basis = HybridAllocationBasisRequests
	}
	a.DefaultRule = a.DefaultRule.Normalize()
	for index := range a.ModelOverrides {
		a.ModelOverrides[index].Model = strings.TrimSpace(a.ModelOverrides[index].Model)
		a.ModelOverrides[index].Rule = a.ModelOverrides[index].Rule.Normalize()
	}
	return a
}

// ValidHybridAllocation enforces exact totals, bounded elastic ceilings,
// non-negative commercial constraints, and unique safe model overrides.
func ValidHybridAllocation(a HybridAllocation) bool {
	a = a.Normalize()
	if a.UserID <= 0 || strings.TrimSpace(a.InstanceID) == "" ||
		a.Basis != HybridAllocationBasisRequests || !a.DefaultRule.Valid() ||
		a.DailyBudgetMicros < 0 || a.MaxUnitPriceMicros < 0 || a.RoutingGeneration < 0 || len(a.ModelOverrides) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(a.ModelOverrides))
	for _, override := range a.ModelOverrides {
		if !validHybridModel(override.Model) || !override.Rule.Valid() {
			return false
		}
		key := strings.ToLower(override.Model)
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validHybridModel(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

// ValidHybridRoutingModel accepts the empty instance-default scope or one
// exact, bounded model identifier.
func ValidHybridRoutingModel(value string) bool {
	return value == "" || validHybridModel(value)
}

// RuleForModel resolves an exact case-insensitive model override before the
// instance default. The returned rule is normalized.
func (a HybridAllocation) RuleForModel(model string) HybridAllocationRule {
	model = strings.TrimSpace(model)
	for _, override := range a.ModelOverrides {
		if strings.EqualFold(model, override.Model) {
			return override.Rule.Normalize()
		}
	}
	return a.DefaultRule.Normalize()
}

type HybridAllocationRequest struct {
	Basis              string                  `json:"basis"`
	DefaultRule        HybridAllocationRule    `json:"default_rule"`
	ModelOverrides     []HybridModelAllocation `json:"model_overrides"`
	DailyBudgetMicros  int64                   `json:"daily_budget_micros"`
	MaxUnitPriceMicros int64                   `json:"max_unit_price_micros"`
	ExpectedVersion    int64                   `json:"expected_version"`
}

type AllocationResourceState struct {
	Class      ResourceClass `json:"class"`
	Available  bool          `json:"available"`
	Capacity   int           `json:"capacity"` // maximum request-percent currently admitted
	ReasonCode string        `json:"reason_code,omitempty"`
}

type EffectiveAllocation struct {
	Target          map[ResourceClass]int `json:"target"`
	Effective       map[ResourceClass]int `json:"effective"`
	Unallocated     int                   `json:"unallocated"`
	AdjustmentCodes []string              `json:"adjustment_codes"`
}

// CompileEffectiveAllocation is deterministic and spend-safe. It never
// exceeds an owner-approved burst ceiling. Unavailable shares move to stable,
// then owner, then economy; any remainder stays unallocated instead of
// silently widening spend. Callers encode balance, budget and price failures
// as unavailable resource states with a non-sensitive reason code.
func CompileEffectiveAllocation(rule HybridAllocationRule, states []AllocationResourceState) EffectiveAllocation {
	rule = rule.Normalize()
	target := map[ResourceClass]int{
		ResourceClassOwner: rule.OwnerPercent, ResourceClassEconomy: rule.EconomyPercent, ResourceClassStable: rule.StablePercent,
	}
	maximum := map[ResourceClass]int{
		ResourceClassOwner: rule.OwnerBurstMax, ResourceClassEconomy: rule.EconomyBurstMax, ResourceClassStable: rule.StableBurstMax,
	}
	available := map[ResourceClass]bool{}
	capacity := map[ResourceClass]int{}
	reasons := map[ResourceClass]string{}
	for _, state := range states {
		if !state.Class.Valid() {
			continue
		}
		available[state.Class] = state.Available
		if state.Capacity < 0 {
			state.Capacity = 0
		}
		if state.Capacity > 100 {
			state.Capacity = 100
		}
		capacity[state.Class] = state.Capacity
		reasons[state.Class] = strings.TrimSpace(state.ReasonCode)
	}
	effective := map[ResourceClass]int{ResourceClassOwner: 0, ResourceClassEconomy: 0, ResourceClassStable: 0}
	missing := 0
	codes := []string{}
	classes := []ResourceClass{ResourceClassOwner, ResourceClassEconomy, ResourceClassStable}
	for _, class := range classes {
		share := target[class]
		if available[class] && capacity[class] > 0 {
			if share > capacity[class] {
				effective[class] = capacity[class]
				missing += share - capacity[class]
				codes = append(codes, string(class)+"_capacity_limited")
			} else {
				effective[class] = share
			}
			continue
		}
		missing += share
		if share > 0 {
			code := reasons[class]
			if code == "" {
				code = string(class) + "_unavailable"
			}
			codes = append(codes, code)
		}
	}
	for _, class := range []ResourceClass{ResourceClassStable, ResourceClassOwner, ResourceClassEconomy} {
		if missing == 0 || !available[class] || capacity[class] <= effective[class] {
			continue
		}
		room := maximum[class] - effective[class]
		if capacity[class]-effective[class] < room {
			room = capacity[class] - effective[class]
		}
		if room > missing {
			room = missing
		}
		if room > 0 {
			effective[class] += room
			missing -= room
			codes = append(codes, string(class)+"_burst_used")
		}
	}
	return EffectiveAllocation{Target: target, Effective: effective, Unallocated: missing, AdjustmentCodes: codes}
}

type AllocationActual struct {
	InstanceID    string                  `json:"instance_id"`
	Model         string                  `json:"model,omitempty"`
	WindowStart   time.Time               `json:"window_start"`
	WindowEnd     time.Time               `json:"window_end"`
	RequestCounts map[ResourceClass]int64 `json:"request_counts"`
	Percent       map[ResourceClass]int   `json:"percent"`
}

type Wallet struct {
	UserID          int64     `json:"user_id"`
	Currency        string    `json:"currency"`
	AvailableMicros int64     `json:"available_micros"`
	ReservedMicros  int64     `json:"reserved_micros"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type WalletAccountCode string

const (
	WalletAccountPlatformCash    WalletAccountCode = "platform_cash"
	WalletAccountUserAvailable   WalletAccountCode = "user_available"
	WalletAccountUserReserved    WalletAccountCode = "user_reserved"
	WalletAccountPlatformRevenue WalletAccountCode = "platform_revenue"
	WalletAccountUpstreamPayable WalletAccountCode = "upstream_payable"
)

type WalletEntryDirection string

const (
	WalletEntryDebit  WalletEntryDirection = "debit"
	WalletEntryCredit WalletEntryDirection = "credit"
)

type WalletEntry struct {
	ID           string               `json:"id"`
	JournalID    string               `json:"journal_id"`
	Account      WalletAccountCode    `json:"account"`
	Direction    WalletEntryDirection `json:"direction"`
	AmountMicros int64                `json:"amount_micros"`
	Currency     string               `json:"currency"`
	CreatedAt    time.Time            `json:"created_at"`
}

type WalletJournalKind string

const (
	WalletJournalRecharge   WalletJournalKind = "recharge"
	WalletJournalAdjustment WalletJournalKind = "adjustment"
	WalletJournalRedeem     WalletJournalKind = "redeem"
	WalletJournalReserve    WalletJournalKind = "reserve"
	WalletJournalSettle     WalletJournalKind = "settle"
	WalletJournalRelease    WalletJournalKind = "release"
	WalletJournalRefund     WalletJournalKind = "refund"
)

type WalletJournal struct {
	ID             string            `json:"id"`
	UserID         int64             `json:"user_id"`
	Kind           WalletJournalKind `json:"kind"`
	Currency       string            `json:"currency"`
	AmountMicros   int64             `json:"amount_micros"`
	IdempotencyKey string            `json:"idempotency_key"`
	ReferenceType  string            `json:"reference_type"`
	ReferenceID    string            `json:"reference_id"`
	Entries        []WalletEntry     `json:"entries"`
	CreatedAt      time.Time         `json:"created_at"`
}

func (j WalletJournal) Balanced() bool {
	var debit, credit int64
	for _, entry := range j.Entries {
		if entry.AmountMicros <= 0 || entry.Currency != j.Currency || entry.JournalID != j.ID {
			return false
		}
		switch entry.Direction {
		case WalletEntryDebit:
			debit += entry.AmountMicros
		case WalletEntryCredit:
			credit += entry.AmountMicros
		default:
			return false
		}
	}
	return len(j.Entries) >= 2 && debit == credit && debit == j.AmountMicros
}

type WalletReservationStatus string

const (
	WalletReservationActive   WalletReservationStatus = "active"
	WalletReservationSettled  WalletReservationStatus = "settled"
	WalletReservationReleased WalletReservationStatus = "released"
)

type WalletReservation struct {
	ID             string                  `json:"id"`
	UserID         int64                   `json:"user_id"`
	VirtualKeyID   string                  `json:"virtual_key_id"`
	ChannelID      string                  `json:"channel_id"`
	RequestID      string                  `json:"request_id"`
	Currency       string                  `json:"currency"`
	ReservedMicros int64                   `json:"reserved_micros"`
	SettledMicros  int64                   `json:"settled_micros"`
	Status         WalletReservationStatus `json:"status"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
}

type RechargeOrderRequest struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	PaymentType string `json:"payment_type"`
	ReturnURL   string `json:"return_url,omitempty"`
}

type RechargeOrderResponse struct {
	Order       PaymentOrder `json:"order"`
	CheckoutURL string       `json:"checkout_url"`
}

type PaymentNotification struct {
	ProviderInstanceID string             `json:"provider_instance_id"`
	ProviderKey        PaymentProviderKey `json:"provider_key"`
	EventID            string             `json:"event_id"`
	OutTradeNo         string             `json:"out_trade_no"`
	PaymentTradeNo     string             `json:"payment_trade_no"`
	ProviderOrderID    string             `json:"provider_order_id"`
	PaidAmountMicros   int64              `json:"paid_amount_micros"`
	Currency           string             `json:"currency"`
	PaidAt             time.Time          `json:"paid_at"`
}

type PaymentCallbackEvent struct {
	ID                 string             `json:"id"`
	ProviderInstanceID string             `json:"provider_instance_id"`
	ProviderKey        PaymentProviderKey `json:"provider_key"`
	EventID            string             `json:"event_id"`
	OrderID            string             `json:"order_id,omitempty"`
	BodyHash           string             `json:"body_hash"`
	Accepted           bool               `json:"accepted"`
	ErrorCode          string             `json:"error_code,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
}

type VirtualKey struct {
	ID     string `json:"id"`
	UserID int64  `json:"user_id"`
	// GroupID is the native E2M distribution group selected by this key. New
	// platform keys use GroupID and do not belong to a Connector-managed
	// instance. InstanceID remains only for rows created by the retired hybrid
	// routing experiment.
	GroupID          string        `json:"group_id,omitempty"`
	InstanceID       string        `json:"instance_id,omitempty"`
	Name             string        `json:"name"`
	ResourceClass    ResourceClass `json:"resource_class"`
	Prefix           string        `json:"prefix"`
	TokenHash        string        `json:"-"`
	SecretRef        string        `json:"-"`
	KeyVersion       int64         `json:"key_version"`
	Enabled          bool          `json:"enabled"`
	Models           []string      `json:"models"`
	DailyLimitMicros int64         `json:"daily_limit_micros"`
	ExpiresAt        *time.Time    `json:"expires_at,omitempty"`
	LastUsedAt       *time.Time    `json:"last_used_at,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (k VirtualKey) Valid() bool {
	platformKey := strings.TrimSpace(k.GroupID) != "" && strings.TrimSpace(k.InstanceID) == ""
	legacyKey := strings.TrimSpace(k.GroupID) == "" && strings.TrimSpace(k.InstanceID) != ""
	if k.UserID <= 0 || (!platformKey && !legacyKey) || strings.TrimSpace(k.Name) == "" ||
		!k.ResourceClass.IsPlatformSupply() || k.KeyVersion < 0 || k.DailyLimitMicros < 0 || len(k.Models) > 100 {
		return false
	}
	for _, model := range k.Models {
		if !validHybridModel(model) {
			return false
		}
	}
	return true
}

type CreateVirtualKeyRequest struct {
	UserID           int64         `json:"user_id,omitempty"`
	GroupID          string        `json:"group_id,omitempty"`
	InstanceID       string        `json:"instance_id,omitempty"`
	Name             string        `json:"name"`
	ResourceClass    ResourceClass `json:"resource_class"`
	Models           []string      `json:"models"`
	DailyLimitMicros int64         `json:"daily_limit_micros"`
	ExpiresAt        *time.Time    `json:"expires_at,omitempty"`
}

type CreateVirtualKeyResponse struct {
	Key       VirtualKey `json:"key"`
	Plaintext string     `json:"plaintext"`
}

type UpdateVirtualKeyRequest struct {
	Name             *string     `json:"name,omitempty"`
	Models           *[]string   `json:"models,omitempty"`
	DailyLimitMicros *int64      `json:"daily_limit_micros,omitempty"`
	Enabled          *bool       `json:"enabled,omitempty"`
	ExpiresAt        **time.Time `json:"expires_at,omitempty"`
}

func HashVirtualKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type SupplyUsageStatus string

const (
	SupplyUsageReserved SupplyUsageStatus = "reserved"
	SupplyUsageSettled  SupplyUsageStatus = "settled"
	SupplyUsageReleased SupplyUsageStatus = "released"
)

type SupplyUsageRecord struct {
	InputPriceMicrosPerMillion     int64             `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion    int64             `json:"output_price_micros_per_million"`
	InputSupplierMicrosPerMillion  int64             `json:"input_supplier_micros_per_million"`
	OutputSupplierMicrosPerMillion int64             `json:"output_supplier_micros_per_million"`
	ID                             string            `json:"id"`
	RequestID                      string            `json:"request_id"`
	ReservationID                  string            `json:"reservation_id"`
	UserID                         int64             `json:"user_id"`
	GroupID                        string            `json:"group_id,omitempty"`
	InstanceID                     string            `json:"instance_id,omitempty"`
	VirtualKeyID                   string            `json:"virtual_key_id"`
	ResourceClass                  ResourceClass     `json:"resource_class"`
	ChannelID                      string            `json:"channel_id,omitempty"`
	Model                          string            `json:"model"`
	PromptTokens                   int64             `json:"prompt_tokens"`
	CompletionTokens               int64             `json:"completion_tokens"`
	ReservedMicros                 int64             `json:"reserved_micros"`
	SettledMicros                  int64             `json:"settled_micros"`
	Status                         SupplyUsageStatus `json:"status"`
	SettlementReason               string            `json:"settlement_reason,omitempty"`
	CreatedAt                      time.Time         `json:"created_at"`
	CompletedAt                    *time.Time        `json:"completed_at,omitempty"`
}

// SupplyDailyUsage is the authoritative UTC-day reservation total used by
// both routing admission and the request-time reservation transaction.
// InstanceReservedMicros enforces the allocation budget; KeyReservedMicros
// independently enforces the selected virtual key's narrower limit.
type SupplyDailyUsage struct {
	UserID                 int64     `json:"user_id"`
	InstanceID             string    `json:"instance_id"`
	VirtualKeyID           string    `json:"virtual_key_id"`
	Currency               string    `json:"currency"`
	DayStart               time.Time `json:"day_start"`
	InstanceReservedMicros int64     `json:"instance_reserved_micros"`
	KeyReservedMicros      int64     `json:"key_reserved_micros"`
}

// SupplyChannelEndpoint is platform-private runtime configuration for one
// centrally served upstream channel. SecretRef resolves the real upstream key
// only inside Supply Gateway and is never serialized. Retail prices determine
// the downstream debit; supplier costs determine the upstream payable. Their
// difference is E2M's gross margin.
type SupplyChannelEndpoint struct {
	ChannelID string `json:"channel_id"`
	BaseURL   string `json:"base_url"`
	// AllowInsecure is an explicit development-only exception for HTTP
	// upstreams. The public management API additionally requires the Core
	// process to opt in before it will persist this value.
	AllowInsecure                  bool      `json:"allow_insecure"`
	SecretRef                      string    `json:"-"`
	MaskedValue                    string    `json:"masked_value"`
	Currency                       string    `json:"currency"`
	InputPriceMicrosPerMillion     int64     `json:"input_price_micros_per_million"`
	OutputPriceMicrosPerMillion    int64     `json:"output_price_micros_per_million"`
	InputSupplierMicrosPerMillion  int64     `json:"input_supplier_micros_per_million"`
	OutputSupplierMicrosPerMillion int64     `json:"output_supplier_micros_per_million"`
	MaxRequestMicros               int64     `json:"max_request_micros"`
	MaxConcurrency                 int       `json:"max_concurrency"`
	CapacityPercent                int       `json:"capacity_percent"`
	Enabled                        bool      `json:"enabled"`
	CreatedAt                      time.Time `json:"created_at"`
	UpdatedAt                      time.Time `json:"updated_at"`
}

func (e SupplyChannelEndpoint) Valid() bool {
	return strings.TrimSpace(e.ChannelID) != "" && validSupplyBaseURL(e.BaseURL, e.AllowInsecure) &&
		validSupplyCurrency(e.Currency) && e.InputPriceMicrosPerMillion >= 0 && e.OutputPriceMicrosPerMillion >= 0 &&
		e.InputSupplierMicrosPerMillion >= 0 && e.OutputSupplierMicrosPerMillion >= 0 &&
		e.InputSupplierMicrosPerMillion <= e.InputPriceMicrosPerMillion &&
		e.OutputSupplierMicrosPerMillion <= e.OutputPriceMicrosPerMillion &&
		e.MaxRequestMicros > 0 && e.MaxConcurrency >= 0 && e.CapacityPercent >= 0 && e.CapacityPercent <= 100
}

func validSupplyCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 'A' || value[index] > 'Z' {
			return false
		}
	}
	return true
}

func validSupplyBaseURL(value string, allowInsecure bool) bool {
	value = strings.TrimSpace(value)
	if len(value) < len("http://a.b") || len(value) > 2048 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "https" || allowInsecure && parsed.Scheme == "http"
}

// SupplyUsageFilter scopes usage reads without exposing request bodies or
// upstream credentials. Zero values mean no filter; Limit is normalized by
// the store to a conservative maximum.
type SupplyUsageFilter struct {
	UserID       int64
	GroupID      string
	VirtualKeyID string
	Status       SupplyUsageStatus
	Limit        int
}

type SupplyCandidate struct {
	Pool     UpstreamPool          `json:"pool"`
	Channel  UpstreamChannel       `json:"channel"`
	Endpoint SupplyChannelEndpoint `json:"endpoint"`
	Active   int                   `json:"active_requests"`
}

// SupplyModelEntry is one callable model in a downstream key's catalog.
// CreatedAt is the earliest creation time among the channels currently able to
// serve the model, so the value is real provenance rather than a placeholder.
// Channels counts how many upstreams back the model right now, which is the
// depth of supply behind it.
type SupplyModelEntry struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Channels  int       `json:"channels"`
}

// SupplyModelCatalog is the set of models a virtual key can actually call at
// this moment, computed from the same eligibility predicate the scheduler uses
// to reserve. Advertising anything the scheduler would reject is the failure
// mode this type exists to prevent.
//
// Unenumerable reports the one configuration that genuinely cannot be listed:
// an eligible pool and channel that both declare an empty model array, which
// means "allow any model". The catalog then holds every model E2M can still
// name, and callers must not present it as exhaustive.
type SupplyModelCatalog struct {
	Models       []SupplyModelEntry `json:"models"`
	Unenumerable bool               `json:"unenumerable"`
}

type SupplyReservationResult struct {
	Key         VirtualKey        `json:"key"`
	Candidate   SupplyCandidate   `json:"candidate"`
	Wallet      Wallet            `json:"wallet"`
	Reservation WalletReservation `json:"reservation"`
	Usage       SupplyUsageRecord `json:"usage"`
}

type SupplySettlementResult struct {
	Wallet         Wallet            `json:"wallet"`
	Reservation    WalletReservation `json:"reservation"`
	Usage          SupplyUsageRecord `json:"usage"`
	ChargedMicros  int64             `json:"charged_micros"`
	SupplierMicros int64             `json:"supplier_micros"`
	ReleasedMicros int64             `json:"released_micros"`
}
