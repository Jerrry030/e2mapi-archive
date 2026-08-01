package store

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func validUpstreamCollectionRun(input contracts.UpstreamCollectionRun) bool {
	if strings.TrimSpace(input.ID) == "" || input.UserID <= 0 || strings.TrimSpace(input.SourceID) == "" || strings.TrimSpace(input.ConnectorID) == "" ||
		input.BatchCount < 0 || input.FactCount < 0 || input.PageCount < 0 || input.StartedAt.IsZero() || input.ObservedAt.IsZero() || input.ObservedAt.Before(input.StartedAt) {
		return false
	}
	if input.Trigger != contracts.UpstreamCollectionScheduled && input.Trigger != contracts.UpstreamCollectionManual && input.Trigger != contracts.UpstreamCollectionTask {
		return false
	}
	if input.Status != contracts.UpstreamCollectionSucceeded && input.Status != contracts.UpstreamCollectionPartial && input.Status != contracts.UpstreamCollectionFailed {
		return false
	}
	if input.CompletedAt == nil || input.CompletedAt.Before(input.ObservedAt) {
		return false
	}
	if input.Status == contracts.UpstreamCollectionSucceeded && input.Coverage != contracts.UpstreamCoverageComplete ||
		input.Status == contracts.UpstreamCollectionPartial && input.Coverage != contracts.UpstreamCoveragePartial ||
		input.Status == contracts.UpstreamCollectionFailed && input.Coverage != contracts.UpstreamCoverageUnavailable {
		return false
	}
	if input.Status == contracts.UpstreamCollectionFailed {
		if !contracts.IsUpstreamCollectionErrorCode(input.ErrorCode) {
			return false
		}
	} else if input.ErrorCode != "" || input.Retryable {
		return false
	}
	if input.FactCount == 0 && (input.BatchCount != 1 || input.PageCount > 1 ||
		(input.Status != contracts.UpstreamCollectionSucceeded && input.Status != contracts.UpstreamCollectionFailed)) {
		return false
	}
	return (input.SnapshotHash == "" || contracts.IsUpstreamIntelligenceSHA256(input.SnapshotHash)) &&
		(input.ManifestHash == "" || contracts.IsUpstreamIntelligenceSHA256(input.ManifestHash))
}

func validUpstreamWallet(input contracts.UpstreamWalletObservation) bool {
	if input.UserID <= 0 || strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.SourceID) == "" ||
		!validUpstreamAccuracy(input.Accuracy) || !validUpstreamCoverage(input.Coverage) || input.ObservedAt.IsZero() || input.FreshUntil.Before(input.ObservedAt) || len(input.ReasonCode) > 64 {
		return false
	}
	if input.UnitKind != contracts.UpstreamWalletFiat && input.UnitKind != contracts.UpstreamWalletCredit && input.UnitKind != contracts.UpstreamWalletUnknown {
		return false
	}
	if input.UnitKind == contracts.UpstreamWalletFiat {
		if !validUpstreamCurrency(input.Currency) {
			return false
		}
	} else if input.Currency != "" {
		return false
	}
	return validUpstreamDecimal(input.BalanceAmount, -1) && validUpstreamConfidence(input.Accuracy, input.Confidence) && validUnknownEvidence(input.Accuracy, input.MissingFields, input.ReasonCode)
}

func validUpstreamOffer(input contracts.UpstreamOfferObservation) bool {
	if input.UserID <= 0 || strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.SourceID) == "" ||
		strings.TrimSpace(input.GroupKey) == "" || len(input.GroupKey) > 128 || strings.TrimSpace(input.ModelKey) == "" || len(input.ModelKey) > 256 ||
		input.PerTokens <= 0 || input.AdapterSchemaVersion <= 0 || !validUpstreamAccuracy(input.Accuracy) || !validUpstreamCoverage(input.Coverage) ||
		input.ObservedAt.IsZero() || input.EffectiveAt.IsZero() || input.FreshUntil.Before(input.ObservedAt) ||
		input.ValidUntil != nil && !input.ValidUntil.After(input.EffectiveAt) || len(input.ReasonCode) > 64 {
		return false
	}
	if input.PriceDimension != contracts.UpstreamPriceInput && input.PriceDimension != contracts.UpstreamPriceOutput && input.PriceDimension != contracts.UpstreamPriceCachedInput && input.PriceDimension != contracts.UpstreamPriceRequest {
		return false
	}
	if input.SettlementCurrency != "" && !validUpstreamCurrency(input.SettlementCurrency) {
		return false
	}
	return validUpstreamDecimal(input.GroupMultiplier, 0) && validUpstreamDecimal(input.RechargeYield, 1) &&
		validUpstreamDecimal(input.PublishedUnitPrice, 0) && validUpstreamDecimal(input.EffectiveMultiplier, 0) &&
		validUpstreamDecimal(input.EffectiveUnitCost, 0) && validUpstreamConfidence(input.Accuracy, input.Confidence) &&
		validUnknownEvidence(input.Accuracy, input.MissingFields, input.ReasonCode)
}

// sign is -1 for any value, 0 for non-negative, and 1 for positive.
func validUpstreamDecimal(value *contracts.CanonicalDecimal, sign int) bool {
	if value == nil {
		return true
	}
	rat, err := value.Rat()
	if err != nil {
		return false
	}
	return sign < 0 || sign == 0 && rat.Sign() >= 0 || sign > 0 && rat.Sign() > 0
}

func validUpstreamConfidence(accuracy contracts.UpstreamEvidenceAccuracy, value *contracts.CanonicalDecimal) bool {
	if value == nil {
		return true
	}
	if accuracy != contracts.UpstreamEvidenceDerived && accuracy != contracts.UpstreamEvidenceEstimated {
		return false
	}
	rat, err := value.Rat()
	return err == nil && rat.Sign() >= 0 && rat.Cmp(newRatOne()) <= 0
}

func newRatOne() *big.Rat { return big.NewRat(1, 1) }

func validUpstreamAccuracy(value contracts.UpstreamEvidenceAccuracy) bool {
	return value == contracts.UpstreamEvidenceExact || value == contracts.UpstreamEvidenceDerived || value == contracts.UpstreamEvidenceEstimated || value == contracts.UpstreamEvidenceUnknown || value == contracts.UpstreamEvidenceUnattributed
}

func validUpstreamCoverage(value contracts.UpstreamEvidenceCoverage) bool {
	return value == contracts.UpstreamCoverageComplete || value == contracts.UpstreamCoveragePartial || value == contracts.UpstreamCoverageUnavailable
}

func validUnknownEvidence(accuracy contracts.UpstreamEvidenceAccuracy, missing []string, reason string) bool {
	return accuracy != contracts.UpstreamEvidenceUnknown || len(missing) > 0 || strings.TrimSpace(reason) != ""
}

func validUpstreamCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func normalizeUpstreamMissingFields(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// UpstreamIntelligenceIngestBatch is the durable receipt for a sanitized
// normalized batch. The payload itself is represented by facts in their
// domain tables; this receipt intentionally stores only hashes and counts.
type UpstreamIntelligenceIngestBatch struct {
	RunID        string
	UserID       int64
	SourceID     string
	BatchNo      int
	BatchCount   int
	PayloadHash  string
	ManifestHash string
	WalletCount  int
	OfferCount   int
	ReceivedAt   time.Time
}

// UpstreamIntelligenceIngest is one already authenticated and sanitized
// Connector batch. The store resolves Source's natural key and persists the
// source, run, facts, and receipt as one atomic unit. Server-owned timestamps
// and finalization fields on the input are ignored.
type UpstreamIntelligenceIngest struct {
	Source  contracts.UpstreamIntelligenceSource
	Run     contracts.UpstreamCollectionRun
	Batch   UpstreamIntelligenceIngestBatch
	Wallets []contracts.UpstreamWalletObservation
	Offers  []contracts.UpstreamOfferObservation
}

// UpstreamSnapshotAbsence records complete-snapshot absence state. UI-02 owns
// the comparison algorithm; this type makes its persistence rules explicit.
type UpstreamSnapshotAbsence struct {
	UserID                   int64
	SourceID                 string
	ComparisonKey            string
	ConsecutiveCompleteRuns  int
	LastPresentObservationID string
	LastPresentRunID         string
	FirstAbsentAt            *time.Time
	LastAbsentRunID          string
	UpdatedAt                time.Time
}

type memoryUpstreamFinalization struct {
	RunID       string
	UserID      int64
	FactVersion int64
}

// UpstreamIntelligenceStore is the deliberately narrow persistence boundary
// for sanitized upstream facts. Keeping it separate lets ingest/read services
// depend on this domain without inheriting the much larger Core Store surface.
// Every read requires an owner filter; a zero owner is invalid rather than an
// implicit cross-tenant wildcard.
type UpstreamIntelligenceStore interface {
	UpsertUpstreamIntelligenceSource(context.Context, contracts.UpstreamIntelligenceSource) (contracts.UpstreamIntelligenceSource, error)
	GetUpstreamIntelligenceSource(context.Context, int64, string) (contracts.UpstreamIntelligenceSource, error)
	ListUpstreamIntelligenceSources(context.Context, contracts.UpstreamIntelligenceSourceFilter) ([]contracts.UpstreamIntelligenceSource, error)

	CreateUpstreamCollectionRun(context.Context, contracts.UpstreamCollectionRun) (contracts.UpstreamCollectionRun, error)
	GetUpstreamCollectionRun(context.Context, int64, string) (contracts.UpstreamCollectionRun, error)
	ListUpstreamCollectionRuns(context.Context, contracts.UpstreamCollectionRunFilter) ([]contracts.UpstreamCollectionRun, error)
	UpsertUpstreamIntelligenceIngestBatch(context.Context, UpstreamIntelligenceIngestBatch) (UpstreamIntelligenceIngestBatch, bool, error)
	ListUpstreamIntelligenceIngestBatches(context.Context, int64, string) ([]UpstreamIntelligenceIngestBatch, error)
	IngestUpstreamIntelligenceBatch(context.Context, UpstreamIntelligenceIngest) (contracts.UpstreamIntelligenceSource, contracts.UpstreamCollectionRun, UpstreamIntelligenceIngestBatch, bool, error)

	AppendUpstreamWalletObservation(context.Context, contracts.UpstreamWalletObservation) (contracts.UpstreamWalletObservation, error)
	ListUpstreamWalletObservations(context.Context, contracts.UpstreamWalletObservationFilter) ([]contracts.UpstreamWalletObservation, error)
	AppendUpstreamOfferObservation(context.Context, contracts.UpstreamOfferObservation) (contracts.UpstreamOfferObservation, error)
	ListUpstreamOfferObservations(context.Context, contracts.UpstreamOfferObservationFilter) ([]contracts.UpstreamOfferObservation, error)
	UpsertUpstreamSnapshotAbsence(context.Context, UpstreamSnapshotAbsence) (UpstreamSnapshotAbsence, error)
	ListUpstreamSnapshotAbsences(context.Context, int64, string) ([]UpstreamSnapshotAbsence, error)

	UpsertUpstreamIntelligenceLink(context.Context, contracts.UpstreamIntelligenceLink) (contracts.UpstreamIntelligenceLink, error)
	ListUpstreamIntelligenceLinks(context.Context, contracts.UpstreamIntelligenceLinkFilter) ([]contracts.UpstreamIntelligenceLink, error)
	AppendUpstreamChangeEvent(context.Context, contracts.UpstreamChangeEvent) (contracts.UpstreamChangeEvent, error)
	ListUpstreamChangeEvents(context.Context, contracts.UpstreamChangeEventFilter) ([]contracts.UpstreamChangeEvent, error)

	GetUpstreamIntelligenceFactVersion(context.Context, int64) (contracts.UpstreamIntelligenceFactVersion, error)
	// FinalizeUpstreamCollectionRun atomically promotes a terminal run and its
	// already validated facts, advances the owner's version, and updates the
	// source pointer. Change detection supplies events/absence updates in UI-02;
	// this persistence operation remains all-or-nothing.
	FinalizeUpstreamCollectionRun(context.Context, int64, string) (contracts.UpstreamCollectionRun, contracts.UpstreamIntelligenceFactVersion, error)
}

var (
	_ UpstreamIntelligenceStore = (*MemoryStore)(nil)
	_ UpstreamIntelligenceStore = (*PostgresStore)(nil)
)
