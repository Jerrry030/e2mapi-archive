package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	UpstreamIntelligenceSchemaVersion    = 1
	UpstreamDecimalMaxPrecision          = 38
	UpstreamDecimalMaxScale              = 18
	UpstreamDecimalMaxIntegerDigits      = UpstreamDecimalMaxPrecision - UpstreamDecimalMaxScale
	MaxUpstreamIntelligenceBatchFacts    = 500
	DefaultUpstreamIntelligenceListLimit = 50
	MaxUpstreamIntelligenceListLimit     = 200

	upstreamIntelligencePayloadHashDomain  = "e2m.upstream-intelligence.payload.v1"
	upstreamIntelligenceManifestHashDomain = "e2m.upstream-intelligence.manifest.v1"
)

var (
	canonicalDecimalPattern = regexp.MustCompile(`^(?:0|-?[1-9][0-9]*|-?(?:0\.[0-9]*[1-9]|[1-9][0-9]*\.[0-9]*[1-9]))$`)
	plainDecimalPattern     = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
	lowerHexSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CanonicalDecimal is the lossless JSON representation used for money,
// prices, rates and multipliers. Values are plain decimal strings: no
// exponent, leading plus, redundant zeroes, NaN, infinity or negative zero.
// The canonical domain matches PostgreSQL NUMERIC(38,18).
type CanonicalDecimal string

func ParseCanonicalDecimal(value string) (CanonicalDecimal, error) {
	if !canonicalDecimalPattern.MatchString(value) {
		return "", errors.New("decimal must be a canonical plain decimal string")
	}
	whole, fractional, _ := strings.Cut(strings.TrimPrefix(value, "-"), ".")
	if len(whole) > UpstreamDecimalMaxIntegerDigits || len(fractional) > UpstreamDecimalMaxScale || len(whole)+len(fractional) > UpstreamDecimalMaxPrecision {
		return "", fmt.Errorf("decimal exceeds NUMERIC(%d,%d)", UpstreamDecimalMaxPrecision, UpstreamDecimalMaxScale)
	}
	return CanonicalDecimal(value), nil
}

// CanonicalizeUpstreamDecimalText converts the plain decimal text returned by
// PostgreSQL NUMERIC into the canonical wire representation. PostgreSQL keeps
// a declared scale when rendering NUMERIC (for example, "1.000000000000000000");
// this helper removes only redundant leading/trailing zeroes. Exponents,
// whitespace, a leading plus, non-finite values and NUMERIC(38,18) overflow are
// rejected rather than guessed or rounded.
func CanonicalizeUpstreamDecimalText(value string) (CanonicalDecimal, error) {
	if !plainDecimalPattern.MatchString(value) {
		return "", errors.New("decimal database text must be plain decimal notation")
	}
	negative := strings.HasPrefix(value, "-")
	unsigned := strings.TrimPrefix(value, "-")
	whole, fractional, hasFraction := strings.Cut(unsigned, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if hasFraction {
		fractional = strings.TrimRight(fractional, "0")
	}
	if whole == "0" && fractional == "" {
		return "0", nil
	}
	canonical := whole
	if fractional != "" {
		canonical += "." + fractional
	}
	if negative {
		canonical = "-" + canonical
	}
	return ParseCanonicalDecimal(canonical)
}

// NormalizeUpstreamIntelligenceListLimit applies the shared bounded-list
// contract. Zero and negative values request the safe default; oversized
// values are capped so a missing API validation layer cannot create an
// unbounded store query.
func NormalizeUpstreamIntelligenceListLimit(limit int) int {
	if limit <= 0 {
		return DefaultUpstreamIntelligenceListLimit
	}
	if limit > MaxUpstreamIntelligenceListLimit {
		return MaxUpstreamIntelligenceListLimit
	}
	return limit
}

func (d CanonicalDecimal) Valid() bool {
	_, err := ParseCanonicalDecimal(string(d))
	return err == nil
}

func (d CanonicalDecimal) Rat() (*big.Rat, error) {
	if !d.Valid() {
		return nil, errors.New("invalid canonical decimal")
	}
	r, ok := new(big.Rat).SetString(string(d))
	if !ok {
		return nil, errors.New("invalid canonical decimal")
	}
	return r, nil
}

func (d CanonicalDecimal) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, errors.New("invalid canonical decimal")
	}
	return json.Marshal(string(d))
}

func (d *CanonicalDecimal) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("nil canonical decimal")
	}
	var raw string
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return errors.New("decimal must be encoded as a JSON string")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	parsed, err := ParseCanonicalDecimal(raw)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// QuantizeCanonicalDecimal rounds a rational to scale decimal places using
// round-half-to-even, then removes redundant trailing fractional zeroes.
func QuantizeCanonicalDecimal(value *big.Rat, scale int) (CanonicalDecimal, error) {
	if value == nil || scale < 0 || scale > UpstreamDecimalMaxScale {
		return "", errors.New("invalid decimal quantization")
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaledNum := new(big.Int).Mul(value.Num(), factor)
	den := new(big.Int).Set(value.Denom())
	absNum := new(big.Int).Abs(new(big.Int).Set(scaledNum))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(absNum, den, remainder)
	doubleRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	comparison := doubleRemainder.Cmp(den)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if scaledNum.Sign() < 0 {
		quotient.Neg(quotient)
	}
	result := scaledIntegerToDecimal(quotient, scale)
	return ParseCanonicalDecimal(result)
}

func scaledIntegerToDecimal(value *big.Int, scale int) string {
	if value.Sign() == 0 {
		return "0"
	}
	negative := value.Sign() < 0
	digits := new(big.Int).Abs(new(big.Int).Set(value)).String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		cut := len(digits) - scale
		digits = digits[:cut] + "." + digits[cut:]
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func CalculateEffectiveMultiplier(groupMultiplier, rechargeYield CanonicalDecimal) (CanonicalDecimal, error) {
	group, err := groupMultiplier.Rat()
	if err != nil || group.Sign() < 0 {
		return "", errors.New("group multiplier must be a non-negative canonical decimal")
	}
	yield, err := rechargeYield.Rat()
	if err != nil || yield.Sign() <= 0 {
		return "", errors.New("recharge yield must be a positive canonical decimal")
	}
	return QuantizeCanonicalDecimal(new(big.Rat).Quo(group, yield), UpstreamDecimalMaxScale)
}

func CalculateEffectiveUnitCost(publishedPrice, effectiveMultiplier CanonicalDecimal) (CanonicalDecimal, error) {
	price, err := publishedPrice.Rat()
	if err != nil || price.Sign() < 0 {
		return "", errors.New("published price must be a non-negative canonical decimal")
	}
	multiplier, err := effectiveMultiplier.Rat()
	if err != nil || multiplier.Sign() < 0 {
		return "", errors.New("effective multiplier must be a non-negative canonical decimal")
	}
	return QuantizeCanonicalDecimal(new(big.Rat).Mul(price, multiplier), UpstreamDecimalMaxScale)
}

type UpstreamIntelligenceSourceMode string
type UpstreamIntelligenceSourceStatus string
type UpstreamCollectionTrigger string
type UpstreamCollectionStatus string
type UpstreamEvidenceAccuracy string
type UpstreamEvidenceCoverage string
type UpstreamWalletUnitKind string
type UpstreamPriceDimension string
type UpstreamIntelligenceLinkScope string
type UpstreamIntelligenceLinkStatus string
type UpstreamChangeEventType string
type UpstreamChangeSeverity string

const (
	UpstreamSourceOwned    UpstreamIntelligenceSourceMode = "owned"
	UpstreamSourceExternal UpstreamIntelligenceSourceMode = "external"

	UpstreamSourceActive       UpstreamIntelligenceSourceStatus = "active"
	UpstreamSourcePaused       UpstreamIntelligenceSourceStatus = "paused"
	UpstreamSourceDisconnected UpstreamIntelligenceSourceStatus = "disconnected"

	UpstreamCollectionScheduled UpstreamCollectionTrigger = "scheduled"
	UpstreamCollectionManual    UpstreamCollectionTrigger = "manual"
	UpstreamCollectionTask      UpstreamCollectionTrigger = "task"

	UpstreamCollectionRunning   UpstreamCollectionStatus = "running"
	UpstreamCollectionSucceeded UpstreamCollectionStatus = "succeeded"
	UpstreamCollectionPartial   UpstreamCollectionStatus = "partial"
	UpstreamCollectionFailed    UpstreamCollectionStatus = "failed"

	UpstreamEvidenceExact        UpstreamEvidenceAccuracy = "exact"
	UpstreamEvidenceDerived      UpstreamEvidenceAccuracy = "derived"
	UpstreamEvidenceEstimated    UpstreamEvidenceAccuracy = "estimated"
	UpstreamEvidenceUnknown      UpstreamEvidenceAccuracy = "unknown"
	UpstreamEvidenceUnattributed UpstreamEvidenceAccuracy = "unattributed"

	UpstreamCoverageComplete    UpstreamEvidenceCoverage = "complete"
	UpstreamCoveragePartial     UpstreamEvidenceCoverage = "partial"
	UpstreamCoverageUnavailable UpstreamEvidenceCoverage = "unavailable"

	UpstreamWalletFiat    UpstreamWalletUnitKind = "fiat"
	UpstreamWalletCredit  UpstreamWalletUnitKind = "credit"
	UpstreamWalletUnknown UpstreamWalletUnitKind = "unknown"

	UpstreamPriceInput       UpstreamPriceDimension = "input"
	UpstreamPriceOutput      UpstreamPriceDimension = "output"
	UpstreamPriceCachedInput UpstreamPriceDimension = "cached_input"
	UpstreamPriceRequest     UpstreamPriceDimension = "request"

	UpstreamLinkSourceIdentity UpstreamIntelligenceLinkScope  = "source_identity"
	UpstreamLinkChannel        UpstreamIntelligenceLinkScope  = "channel"
	UpstreamLinkActive         UpstreamIntelligenceLinkStatus = "active"
	UpstreamLinkInactive       UpstreamIntelligenceLinkStatus = "inactive"

	UpstreamChangeBalanceLow       UpstreamChangeEventType = "balance_low"
	UpstreamChangeBalanceRecovered UpstreamChangeEventType = "balance_recovered"
	UpstreamChangeGroupAdded       UpstreamChangeEventType = "group_added"
	UpstreamChangeGroupChanged     UpstreamChangeEventType = "group_changed"
	UpstreamChangeGroupRemoved     UpstreamChangeEventType = "group_removed"
	UpstreamChangeModelAdded       UpstreamChangeEventType = "model_added"
	UpstreamChangePriceIncreased   UpstreamChangeEventType = "price_increased"
	UpstreamChangePriceDecreased   UpstreamChangeEventType = "price_decreased"
	UpstreamChangeModelRemoved     UpstreamChangeEventType = "model_removed"
	UpstreamChangeSourceStale      UpstreamChangeEventType = "source_stale"
	UpstreamChangeSourceRecovered  UpstreamChangeEventType = "source_recovered"

	UpstreamChangeInfo     UpstreamChangeSeverity = "info"
	UpstreamChangeWarning  UpstreamChangeSeverity = "warning"
	UpstreamChangeCritical UpstreamChangeSeverity = "critical"

	UpstreamCollectionErrorAuthFailed          = "auth_failed"
	UpstreamCollectionErrorRateLimited         = "rate_limited"
	UpstreamCollectionErrorSchemaUnsupported   = "schema_unsupported"
	UpstreamCollectionErrorResponseTooLarge    = "response_too_large"
	UpstreamCollectionErrorUpstreamUnavailable = "upstream_unavailable"
)

// IsUpstreamCollectionErrorCode keeps Connector-reported collection failures
// on a small, stable vocabulary. In particular, arbitrary upstream messages,
// URLs and credentials must never be persisted as an error code.
func IsUpstreamCollectionErrorCode(value string) bool {
	switch value {
	case UpstreamCollectionErrorAuthFailed,
		UpstreamCollectionErrorRateLimited,
		UpstreamCollectionErrorSchemaUnsupported,
		UpstreamCollectionErrorResponseTooLarge,
		UpstreamCollectionErrorUpstreamUnavailable:
		return true
	default:
		return false
	}
}

type UpstreamIntelligenceCapabilities struct {
	Balance bool `json:"balance"`
	Groups  bool `json:"groups"`
	Rates   bool `json:"rates"`
	Prices  bool `json:"prices"`
}

// UpstreamIntelligenceSource is deliberately sanitized. It contains opaque
// identity and display metadata, never an upstream URL or credential.
type UpstreamIntelligenceSource struct {
	ID                  string                           `json:"id"`
	UserID              int64                            `json:"user_id"`
	ConnectorID         string                           `json:"connector_id"`
	InstanceID          string                           `json:"instance_id"`
	LocalRef            string                           `json:"local_ref"`
	Mode                UpstreamIntelligenceSourceMode   `json:"mode"`
	Provider            string                           `json:"provider"`
	DisplayName         string                           `json:"display_name"`
	Currency            string                           `json:"currency,omitempty"`
	PollIntervalSeconds int                              `json:"poll_interval_seconds"`
	Status              UpstreamIntelligenceSourceStatus `json:"status"`
	Capabilities        UpstreamIntelligenceCapabilities `json:"capabilities"`
	LastRunAt           *time.Time                       `json:"last_run_at,omitempty"`
	LastSuccessAt       *time.Time                       `json:"last_success_at,omitempty"`
	NextPollAt          *time.Time                       `json:"next_poll_at,omitempty"`
	LastCoverage        UpstreamEvidenceCoverage         `json:"last_coverage,omitempty"`
	LastErrorCode       string                           `json:"last_error_code,omitempty"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
}

type UpstreamIntelligenceFactVersion struct {
	UserID      int64     `json:"user_id"`
	FactVersion int64     `json:"fact_version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type UpstreamCollectionRun struct {
	ID           string                    `json:"id"`
	UserID       int64                     `json:"user_id"`
	SourceID     string                    `json:"source_id"`
	ConnectorID  string                    `json:"connector_id"`
	Trigger      UpstreamCollectionTrigger `json:"trigger"`
	Status       UpstreamCollectionStatus  `json:"status"`
	Coverage     UpstreamEvidenceCoverage  `json:"coverage"`
	StartedAt    time.Time                 `json:"started_at"`
	ObservedAt   time.Time                 `json:"observed_at"`
	ReceivedAt   time.Time                 `json:"received_at"`
	CompletedAt  *time.Time                `json:"completed_at,omitempty"`
	SnapshotHash string                    `json:"snapshot_hash,omitempty"`
	ManifestHash string                    `json:"manifest_hash,omitempty"`
	BatchCount   int                       `json:"batch_count"`
	FactCount    int                       `json:"fact_count"`
	PageCount    int                       `json:"page_count"`
	ErrorCode    string                    `json:"error_code,omitempty"`
	Retryable    bool                      `json:"retryable"`
	// FinalizedFactVersion is assigned by Core after manifest validation. It is
	// never trusted from Connector ingest and makes finalization replay-safe.
	FinalizedFactVersion int64     `json:"finalized_fact_version,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type UpstreamWalletObservation struct {
	ID            string                   `json:"id"`
	RunID         string                   `json:"run_id"`
	UserID        int64                    `json:"user_id"`
	SourceID      string                   `json:"source_id"`
	BalanceAmount *CanonicalDecimal        `json:"balance_amount,omitempty"`
	UnitKind      UpstreamWalletUnitKind   `json:"unit_kind"`
	Currency      string                   `json:"currency,omitempty"`
	Accuracy      UpstreamEvidenceAccuracy `json:"accuracy"`
	Coverage      UpstreamEvidenceCoverage `json:"coverage"`
	Confidence    *CanonicalDecimal        `json:"confidence,omitempty"`
	ObservedAt    time.Time                `json:"observed_at"`
	ReceivedAt    time.Time                `json:"received_at"`
	FreshUntil    time.Time                `json:"fresh_until"`
	MissingFields []string                 `json:"missing_fields,omitempty"`
	ReasonCode    string                   `json:"reason_code,omitempty"`
}

type UpstreamOfferObservation struct {
	ID                   string                   `json:"id"`
	RunID                string                   `json:"run_id"`
	UserID               int64                    `json:"user_id"`
	SourceID             string                   `json:"source_id"`
	GroupKey             string                   `json:"group_key"`
	ModelKey             string                   `json:"model_key"`
	PriceDimension       UpstreamPriceDimension   `json:"price_dimension"`
	SettlementCurrency   string                   `json:"settlement_currency,omitempty"`
	GroupMultiplier      *CanonicalDecimal        `json:"group_multiplier,omitempty"`
	RechargeYield        *CanonicalDecimal        `json:"recharge_yield,omitempty"`
	PublishedUnitPrice   *CanonicalDecimal        `json:"published_unit_price,omitempty"`
	PerTokens            int64                    `json:"per_tokens"`
	EffectiveMultiplier  *CanonicalDecimal        `json:"effective_multiplier,omitempty"`
	EffectiveUnitCost    *CanonicalDecimal        `json:"effective_unit_cost,omitempty"`
	FormulaVersion       string                   `json:"formula_version,omitempty"`
	Accuracy             UpstreamEvidenceAccuracy `json:"accuracy"`
	Coverage             UpstreamEvidenceCoverage `json:"coverage"`
	Confidence           *CanonicalDecimal        `json:"confidence,omitempty"`
	ObservedAt           time.Time                `json:"observed_at"`
	EffectiveAt          time.Time                `json:"effective_at"`
	ReceivedAt           time.Time                `json:"received_at"`
	FreshUntil           time.Time                `json:"fresh_until"`
	ValidUntil           *time.Time               `json:"valid_until,omitempty"`
	MissingFields        []string                 `json:"missing_fields,omitempty"`
	ReasonCode           string                   `json:"reason_code,omitempty"`
	AdapterSchemaVersion int                      `json:"adapter_schema_version"`
	SourceRevision       string                   `json:"source_revision,omitempty"`
}

type UpstreamIntelligenceLink struct {
	ID                     string                         `json:"id"`
	UserID                 int64                          `json:"user_id"`
	IntelligenceSourceID   string                         `json:"intelligence_source_id"`
	Scope                  UpstreamIntelligenceLinkScope  `json:"link_scope"`
	UpstreamSourceIdentity string                         `json:"upstream_source_identity,omitempty"`
	ChannelID              string                         `json:"channel_id,omitempty"`
	PriceDimension         UpstreamPriceDimension         `json:"price_dimension,omitempty"`
	Status                 UpstreamIntelligenceLinkStatus `json:"status"`
	VerifiedAt             *time.Time                     `json:"verified_at,omitempty"`
	CreatedAt              time.Time                      `json:"created_at"`
	UpdatedAt              time.Time                      `json:"updated_at"`
}

type UpstreamChangeEvent struct {
	ID                  string                  `json:"id"`
	UserID              int64                   `json:"user_id"`
	SourceID            string                  `json:"source_id"`
	Type                UpstreamChangeEventType `json:"event_type"`
	Fingerprint         string                  `json:"event_fingerprint"`
	BeforeObservationID string                  `json:"before_observation_id,omitempty"`
	AfterObservationID  string                  `json:"after_observation_id,omitempty"`
	AbsoluteChange      *CanonicalDecimal       `json:"absolute_change,omitempty"`
	PercentageChange    *CanonicalDecimal       `json:"percentage_change,omitempty"`
	FirstDetectedAt     time.Time               `json:"first_detected_at"`
	ConfirmedAt         time.Time               `json:"confirmed_at"`
	Severity            UpstreamChangeSeverity  `json:"severity"`
	ImpactScope         map[string]string       `json:"impact_scope,omitempty"`
	GroupKey            string                  `json:"group_key,omitempty"`
	ModelKey            string                  `json:"model_key,omitempty"`
	PriceDimension      UpstreamPriceDimension  `json:"price_dimension,omitempty"`
	CreatedAt           time.Time               `json:"created_at"`
}

type UpstreamIngestBatchManifest struct {
	BatchCount   int    `json:"batch_count"`
	ManifestHash string `json:"manifest_hash"`
}

// UpstreamIntelligenceIngestSourceRegistration is the only source identity
// accepted from Connector ingest. LocalRef is an opaque Connector-local key;
// Core resolves it inside the authenticated Connector scope. The wire shape
// deliberately has no Core source, owner, Connector or instance identifier.
type UpstreamIntelligenceIngestSourceRegistration struct {
	LocalRef            string                           `json:"local_ref"`
	Mode                UpstreamIntelligenceSourceMode   `json:"mode"`
	Provider            string                           `json:"provider"`
	DisplayName         string                           `json:"display_name"`
	Currency            string                           `json:"currency,omitempty"`
	PollIntervalSeconds int                              `json:"poll_interval_seconds"`
	Status              UpstreamIntelligenceSourceStatus `json:"status"`
	Capabilities        UpstreamIntelligenceCapabilities `json:"capabilities"`
}

// UpstreamIntelligenceIngestRun contains only Connector-observed facts and
// opaque idempotency identity. Receipt times and finalization state belong to
// Core and therefore cannot be represented on this wire type.
type UpstreamIntelligenceIngestRun struct {
	ID           string                    `json:"id"`
	Trigger      UpstreamCollectionTrigger `json:"trigger"`
	Status       UpstreamCollectionStatus  `json:"status"`
	Coverage     UpstreamEvidenceCoverage  `json:"coverage"`
	StartedAt    time.Time                 `json:"started_at"`
	ObservedAt   time.Time                 `json:"observed_at"`
	CompletedAt  *time.Time                `json:"completed_at,omitempty"`
	SnapshotHash string                    `json:"snapshot_hash,omitempty"`
	BatchCount   int                       `json:"batch_count"`
	FactCount    int                       `json:"fact_count"`
	PageCount    int                       `json:"page_count"`
	ErrorCode    string                    `json:"error_code,omitempty"`
	Retryable    bool                      `json:"retryable"`
}

type UpstreamIntelligenceIngestWalletObservation struct {
	ID            string                   `json:"id"`
	RunID         string                   `json:"run_id"`
	BalanceAmount *CanonicalDecimal        `json:"balance_amount,omitempty"`
	UnitKind      UpstreamWalletUnitKind   `json:"unit_kind"`
	Currency      string                   `json:"currency,omitempty"`
	Accuracy      UpstreamEvidenceAccuracy `json:"accuracy"`
	Coverage      UpstreamEvidenceCoverage `json:"coverage"`
	Confidence    *CanonicalDecimal        `json:"confidence,omitempty"`
	ObservedAt    time.Time                `json:"observed_at"`
	FreshUntil    time.Time                `json:"fresh_until"`
	MissingFields []string                 `json:"missing_fields,omitempty"`
	ReasonCode    string                   `json:"reason_code,omitempty"`
}

// UpstreamIntelligenceIngestOfferObservation carries only published inputs.
// EffectiveMultiplier, EffectiveUnitCost and FormulaVersion are intentionally
// absent: Core is the single formula-v1 implementation and derives them after
// authenticating and validating this observation.
type UpstreamIntelligenceIngestOfferObservation struct {
	ID                   string                   `json:"id"`
	RunID                string                   `json:"run_id"`
	GroupKey             string                   `json:"group_key"`
	ModelKey             string                   `json:"model_key"`
	PriceDimension       UpstreamPriceDimension   `json:"price_dimension"`
	SettlementCurrency   string                   `json:"settlement_currency,omitempty"`
	GroupMultiplier      *CanonicalDecimal        `json:"group_multiplier,omitempty"`
	RechargeYield        *CanonicalDecimal        `json:"recharge_yield,omitempty"`
	PublishedUnitPrice   *CanonicalDecimal        `json:"published_unit_price,omitempty"`
	PerTokens            int64                    `json:"per_tokens"`
	Accuracy             UpstreamEvidenceAccuracy `json:"accuracy"`
	Coverage             UpstreamEvidenceCoverage `json:"coverage"`
	Confidence           *CanonicalDecimal        `json:"confidence,omitempty"`
	ObservedAt           time.Time                `json:"observed_at"`
	EffectiveAt          time.Time                `json:"effective_at"`
	FreshUntil           time.Time                `json:"fresh_until"`
	ValidUntil           *time.Time               `json:"valid_until,omitempty"`
	MissingFields        []string                 `json:"missing_fields,omitempty"`
	ReasonCode           string                   `json:"reason_code,omitempty"`
	AdapterSchemaVersion int                      `json:"adapter_schema_version"`
	SourceRevision       string                   `json:"source_revision,omitempty"`
}

type UpstreamIntelligenceIngestBatchRequest struct {
	SchemaVersion int                                           `json:"schema_version"`
	Source        UpstreamIntelligenceIngestSourceRegistration  `json:"source"`
	Run           UpstreamIntelligenceIngestRun                 `json:"run"`
	Manifest      UpstreamIngestBatchManifest                   `json:"manifest"`
	BatchNo       int                                           `json:"batch_no"`
	PayloadHash   string                                        `json:"payload_hash"`
	Wallets       []UpstreamIntelligenceIngestWalletObservation `json:"wallets,omitempty"`
	Offers        []UpstreamIntelligenceIngestOfferObservation  `json:"offers,omitempty"`
}

// UpstreamIntelligenceManifestBatch is one ordered leaf in a v1 manifest.
// BatchNo must form the exact sequence 0..N-1 and PayloadHash must be a
// lowercase SHA-256 hex digest.
type UpstreamIntelligenceManifestBatch struct {
	BatchNo     int    `json:"batch_no"`
	PayloadHash string `json:"payload_hash"`
}

func IsUpstreamIntelligenceSHA256(value string) bool {
	return lowerHexSHA256Pattern.MatchString(value)
}

type upstreamIntelligencePayloadV1 struct {
	Domain        string                                        `json:"domain"`
	SchemaVersion int                                           `json:"schema_version"`
	Source        UpstreamIntelligenceIngestSourceRegistration  `json:"source"`
	Run           UpstreamIntelligenceIngestRun                 `json:"run"`
	BatchNo       int                                           `json:"batch_no"`
	Wallets       []UpstreamIntelligenceIngestWalletObservation `json:"wallets"`
	Offers        []UpstreamIntelligenceIngestOfferObservation  `json:"offers"`
}

// CanonicalUpstreamIntelligenceBatchPayload returns the exact v1 payload
// covered by PayloadHash. Fact order and set-like missing_fields order are
// normalized, and all times are converted to UTC before JSON encoding.
func CanonicalUpstreamIntelligenceBatchPayload(request UpstreamIntelligenceIngestBatchRequest) ([]byte, error) {
	if request.SchemaVersion != UpstreamIntelligenceSchemaVersion || strings.TrimSpace(request.Source.LocalRef) == "" ||
		strings.TrimSpace(request.Run.ID) == "" || request.BatchNo < 0 || request.Run.BatchCount <= request.BatchNo ||
		request.Manifest.BatchCount != request.Run.BatchCount || len(request.Wallets)+len(request.Offers) > MaxUpstreamIntelligenceBatchFacts {
		return nil, errors.New("invalid upstream intelligence batch identity")
	}
	wallets := append([]UpstreamIntelligenceIngestWalletObservation(nil), request.Wallets...)
	offers := append([]UpstreamIntelligenceIngestOfferObservation(nil), request.Offers...)
	seenWallets := make(map[string]struct{}, len(wallets))
	for index := range wallets {
		if strings.TrimSpace(wallets[index].ID) == "" || wallets[index].RunID != request.Run.ID {
			return nil, errors.New("wallet observation has invalid run identity")
		}
		if _, duplicate := seenWallets[wallets[index].ID]; duplicate {
			return nil, errors.New("duplicate wallet observation id")
		}
		seenWallets[wallets[index].ID] = struct{}{}
		normalizeIngestWallet(&wallets[index])
	}
	seenOffers := make(map[string]struct{}, len(offers))
	for index := range offers {
		if strings.TrimSpace(offers[index].ID) == "" || offers[index].RunID != request.Run.ID {
			return nil, errors.New("offer observation has invalid run identity")
		}
		if _, duplicate := seenOffers[offers[index].ID]; duplicate {
			return nil, errors.New("duplicate offer observation id")
		}
		seenOffers[offers[index].ID] = struct{}{}
		normalizeIngestOffer(&offers[index])
	}
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].ID < wallets[j].ID })
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	if wallets == nil {
		wallets = make([]UpstreamIntelligenceIngestWalletObservation, 0)
	}
	if offers == nil {
		offers = make([]UpstreamIntelligenceIngestOfferObservation, 0)
	}
	source, run := request.Source, request.Run
	normalizeIngestRun(&run)
	payload := upstreamIntelligencePayloadV1{
		Domain: upstreamIntelligencePayloadHashDomain, SchemaVersion: request.SchemaVersion,
		Source: source, Run: run, BatchNo: request.BatchNo, Wallets: wallets, Offers: offers,
	}
	return json.Marshal(payload)
}

func normalizeIngestRun(run *UpstreamIntelligenceIngestRun) {
	run.StartedAt = canonicalHashTime(run.StartedAt)
	run.ObservedAt = canonicalHashTime(run.ObservedAt)
	run.CompletedAt = canonicalHashTimePtr(run.CompletedAt)
}

func normalizeIngestWallet(observation *UpstreamIntelligenceIngestWalletObservation) {
	observation.ObservedAt = canonicalHashTime(observation.ObservedAt)
	observation.FreshUntil = canonicalHashTime(observation.FreshUntil)
	observation.MissingFields = canonicalStringList(observation.MissingFields)
}

func normalizeIngestOffer(observation *UpstreamIntelligenceIngestOfferObservation) {
	observation.ObservedAt = canonicalHashTime(observation.ObservedAt)
	observation.EffectiveAt = canonicalHashTime(observation.EffectiveAt)
	observation.FreshUntil = canonicalHashTime(observation.FreshUntil)
	observation.ValidUntil = canonicalHashTimePtr(observation.ValidUntil)
	observation.MissingFields = canonicalStringList(observation.MissingFields)
}

func canonicalHashTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.Round(0).UTC()
}

func canonicalHashTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := canonicalHashTime(*value)
	return &normalized
}

func canonicalStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func CalculateUpstreamIntelligencePayloadHash(request UpstreamIntelligenceIngestBatchRequest) (string, error) {
	payload, err := CanonicalUpstreamIntelligenceBatchPayload(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:]), nil
}

// CalculateUpstreamIntelligenceManifestHash commits to an exact, contiguous
// ordered list of v1 payload hashes. The input order is intentionally ignored;
// BatchNo supplies the canonical order and duplicate/gapped sequences fail.
func CalculateUpstreamIntelligenceManifestHash(batches []UpstreamIntelligenceManifestBatch) (string, error) {
	if len(batches) == 0 {
		return "", errors.New("manifest must contain at least one batch")
	}
	ordered := append([]UpstreamIntelligenceManifestBatch(nil), batches...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].BatchNo < ordered[j].BatchNo })
	for index, batch := range ordered {
		if batch.BatchNo != index || !IsUpstreamIntelligenceSHA256(batch.PayloadHash) {
			return "", errors.New("manifest batches must be contiguous lowercase SHA-256 leaves")
		}
	}
	canonical := struct {
		Domain        string                              `json:"domain"`
		SchemaVersion int                                 `json:"schema_version"`
		BatchCount    int                                 `json:"batch_count"`
		Batches       []UpstreamIntelligenceManifestBatch `json:"batches"`
	}{
		Domain: upstreamIntelligenceManifestHashDomain, SchemaVersion: UpstreamIntelligenceSchemaVersion,
		BatchCount: len(ordered), Batches: ordered,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:]), nil
}

type UpstreamIntelligenceIngestBatchResponse struct {
	// SourceID is the Core-owned identity resolved from the authenticated
	// Connector scope plus Source.LocalRef. It is a binding receipt, never a
	// caller-selected identity.
	SourceID  string `json:"source_id"`
	Accepted  int    `json:"accepted"`
	Duplicate int    `json:"duplicate"`
	Rejected  int    `json:"rejected"`
	Finalized bool   `json:"finalized"`
	ErrorCode string `json:"error_code,omitempty"`
}

type UpstreamIntelligenceSourceFilter struct {
	UserID      int64
	ConnectorID string
	InstanceID  string
	Status      UpstreamIntelligenceSourceStatus
	Limit       int
}

type UpstreamCollectionRunFilter struct {
	UserID   int64
	SourceID string
	Status   UpstreamCollectionStatus
	Since    time.Time
	Limit    int
}

type UpstreamOfferObservationFilter struct {
	UserID         int64
	SourceID       string
	GroupKey       string
	ModelKey       string
	PriceDimension UpstreamPriceDimension
	Since          time.Time
	Limit          int
}

type UpstreamWalletObservationFilter struct {
	UserID   int64
	SourceID string
	Since    time.Time
	Limit    int
}

type UpstreamChangeEventFilter struct {
	UserID   int64
	SourceID string
	Type     UpstreamChangeEventType
	Since    time.Time
	Limit    int
}

type UpstreamIntelligenceLinkFilter struct {
	UserID               int64
	IntelligenceSourceID string
	Scope                UpstreamIntelligenceLinkScope
	Status               UpstreamIntelligenceLinkStatus
	Limit                int
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
