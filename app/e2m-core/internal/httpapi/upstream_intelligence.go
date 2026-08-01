package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

var errUpstreamIntelligenceSensitiveValue = errors.New("upstream intelligence sensitive value rejected")

const maxUpstreamIntelligenceIngestBytes = 2 << 20

const (
	maxUpstreamIntelligenceBatches = 1_000
	maxUpstreamIntelligencePages   = 10_000
	upstreamIntelligenceClockSkew  = 5 * time.Minute
)

// handleUpstreamIntelligenceSnapshot accepts only the deliberately narrow wire
// DTO. The authenticated connector supplies owner, connector and instance
// scope; none of those identities are accepted from the request body.
func (s *Server) handleUpstreamIntelligenceSnapshot(w http.ResponseWriter, r *http.Request) {
	intelligence, ok := s.store.(store.UpstreamIntelligenceStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_disabled", "upstream intelligence intake is not enabled")
		return
	}
	connector, authorized := s.authorizeConnector(w, r)
	if !authorized {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpstreamIntelligenceIngestBytes)
	var req contracts.UpstreamIntelligenceIngestBatchRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := validateUpstreamIntelligenceIngestRequest(req, time.Now().UTC()); err != nil {
		if errors.Is(err, errUpstreamIntelligenceSensitiveValue) {
			if !s.recordOperationalSecurityEvent(r.Context(), func(recorder store.OperationalEventRecorder) error {
				return recorder.RecordCredentialLeakDetected(r.Context())
			}) {
				writeError(w, http.StatusServiceUnavailable, "security_event_persistence_failed", "security boundary event could not be recorded")
				return
			}
		}
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	payloadHash, err := contracts.CalculateUpstreamIntelligencePayloadHash(req)
	if err != nil || payloadHash != req.PayloadHash {
		writeError(w, http.StatusBadRequest, "payload_hash_mismatch", "payload hash does not match the canonical batch content")
		return
	}
	capacity, capacityEnabled := store.AsUpstreamIntelligenceIngestCapacityStore(s.store)
	if !capacityEnabled {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_capacity_disabled", "upstream intelligence intake capacity is not enabled")
		return
	}
	capacityResult, err := capacity.AdmitUpstreamIntelligenceIngest(r.Context(), store.UpstreamIntelligenceIngestCapacityRequest{
		UserID: connector.UserID, RunID: req.Run.ID, BatchNo: req.BatchNo, PayloadHash: req.PayloadHash,
		FactCount: len(req.Wallets) + len(req.Offers), Limit: s.upstreamIntelligenceIngestCapacity,
	})
	if errors.Is(err, store.ErrUpstreamIntelligenceIngestQuotaExceeded) {
		retryAfter := time.Until(capacityResult.WindowEnd)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int64(retryAfter/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "upstream_intelligence_ingest_rate_limited", "owner ingest capacity is exhausted for the current window")
		return
	}
	if err != nil {
		writeUpstreamIntelligenceStoreError(w, err)
		return
	}
	// A one-batch manifest is fully knowable before persistence. Multi-batch
	// manifests are re-derived once every durable leaf exists, and again inside
	// Store finalization.
	if req.Manifest.BatchCount == 1 {
		manifestHash, manifestErr := contracts.CalculateUpstreamIntelligenceManifestHash([]contracts.UpstreamIntelligenceManifestBatch{{
			BatchNo: req.BatchNo, PayloadHash: req.PayloadHash,
		}})
		if manifestErr != nil || manifestHash != req.Manifest.ManifestHash {
			writeError(w, http.StatusConflict, "manifest_hash_mismatch", "manifest hash does not match the ordered batch payload hashes")
			return
		}
	}

	source := contracts.UpstreamIntelligenceSource{
		UserID: connector.UserID, ConnectorID: connector.ID, InstanceID: connector.InstanceID,
		LocalRef: req.Source.LocalRef, Mode: req.Source.Mode, Provider: req.Source.Provider,
		DisplayName: req.Source.DisplayName, Currency: req.Source.Currency,
		PollIntervalSeconds: req.Source.PollIntervalSeconds, Status: req.Source.Status,
		Capabilities: req.Source.Capabilities,
	}
	run := upstreamIngestRunToStored(req.Run, connector, "", req.Manifest)
	wallets := make([]contracts.UpstreamWalletObservation, 0, len(req.Wallets))
	for _, input := range req.Wallets {
		wallets = append(wallets, upstreamIngestWalletToStored(input, connector.UserID, "", req.Run.ID))
	}
	offers := make([]contracts.UpstreamOfferObservation, 0, len(req.Offers))
	for _, input := range req.Offers {
		observation, deriveErr := upstreamIngestOfferToStored(input, connector.UserID, "", req.Run.ID)
		if deriveErr != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", deriveErr.Error())
			return
		}
		offers = append(offers, observation)
	}
	batch := store.UpstreamIntelligenceIngestBatch{
		RunID: req.Run.ID, UserID: connector.UserID,
		BatchNo: req.BatchNo, BatchCount: req.Manifest.BatchCount,
		PayloadHash: req.PayloadHash, ManifestHash: req.Manifest.ManifestHash,
		WalletCount: len(req.Wallets), OfferCount: len(req.Offers),
	}
	storedSource, storedRun, _, duplicate, err := intelligence.IngestUpstreamIntelligenceBatch(r.Context(), store.UpstreamIntelligenceIngest{
		Source: source, Run: run, Batch: batch, Wallets: wallets, Offers: offers,
	})
	if err != nil {
		writeUpstreamIntelligenceStoreError(w, err)
		return
	}

	response := contracts.UpstreamIntelligenceIngestBatchResponse{
		SourceID: storedSource.ID, Accepted: len(req.Wallets) + len(req.Offers),
	}
	if duplicate {
		response.Accepted = 0
		response.Duplicate = len(req.Wallets) + len(req.Offers)
	}
	// Finalization is attempted only after all declared batches exist. Missing
	// batches are a normal out-of-order state; conflicts remain explicit.
	batches, err := intelligence.ListUpstreamIntelligenceIngestBatches(r.Context(), connector.UserID, storedRun.ID)
	if err != nil {
		writeUpstreamIntelligenceStoreError(w, err)
		return
	}
	if len(batches) == req.Manifest.BatchCount {
		manifestBatches := make([]contracts.UpstreamIntelligenceManifestBatch, len(batches))
		for index, persisted := range batches {
			if persisted.BatchNo != index {
				writeError(w, http.StatusConflict, "batch_manifest_conflict", "batch sequence is incomplete")
				return
			}
			manifestBatches[index] = contracts.UpstreamIntelligenceManifestBatch{BatchNo: persisted.BatchNo, PayloadHash: persisted.PayloadHash}
		}
		manifestHash, err := contracts.CalculateUpstreamIntelligenceManifestHash(manifestBatches)
		if err != nil || manifestHash != req.Manifest.ManifestHash {
			writeError(w, http.StatusConflict, "manifest_hash_mismatch", "manifest hash does not match the ordered batch payload hashes")
			return
		}
		if _, _, err := intelligence.FinalizeUpstreamCollectionRun(r.Context(), connector.UserID, storedRun.ID); err != nil {
			writeUpstreamIntelligenceStoreError(w, err)
			return
		}
		response.Finalized = true
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) recordOperationalSecurityEvent(ctx context.Context, record func(store.OperationalEventRecorder) error) bool {
	recorder, ok := store.AsOperationalEventRecorder(s.store)
	return ok && record != nil && record(recorder) == nil
}

func validateUpstreamIntelligenceIngestRequest(req contracts.UpstreamIntelligenceIngestBatchRequest, now time.Time) error {
	if containsSensitiveUpstreamIntelligenceValue(req.Source.LocalRef) || containsSensitiveUpstreamIntelligenceValue(req.Source.DisplayName) ||
		containsSensitiveUpstreamIntelligenceValue(req.Run.ID) || containsSensitiveUpstreamIntelligenceValue(req.Run.ErrorCode) {
		return errUpstreamIntelligenceSensitiveValue
	}
	if req.SchemaVersion != contracts.UpstreamIntelligenceSchemaVersion {
		return errors.New("schema_version is unsupported")
	}
	if !validUpstreamIntelligenceWireIdentifier(req.Source.LocalRef, 128) || !validUpstreamIntelligenceWireIdentifier(req.Run.ID, 128) {
		return errors.New("source local_ref and run id are required")
	}
	if req.Source.Provider != "sub2api" || req.Source.Mode != contracts.UpstreamSourceOwned && req.Source.Mode != contracts.UpstreamSourceExternal ||
		req.Source.Status != contracts.UpstreamSourceActive && req.Source.Status != contracts.UpstreamSourcePaused && req.Source.Status != contracts.UpstreamSourceDisconnected ||
		!validUpstreamIntelligenceDisplayName(req.Source.DisplayName) || !validUpstreamIntelligenceCurrency(req.Source.Currency) ||
		req.Source.PollIntervalSeconds < 60 || req.Source.PollIntervalSeconds > 3600 {
		return errors.New("source registration is invalid")
	}
	if req.Manifest.BatchCount <= 0 || req.Manifest.BatchCount > maxUpstreamIntelligenceBatches ||
		req.Run.BatchCount != req.Manifest.BatchCount || req.BatchNo < 0 || req.BatchNo >= req.Manifest.BatchCount ||
		!contracts.IsUpstreamIntelligenceSHA256(req.PayloadHash) || !contracts.IsUpstreamIntelligenceSHA256(req.Manifest.ManifestHash) {
		return errors.New("batch number is outside the declared manifest")
	}
	batchFacts := len(req.Wallets) + len(req.Offers)
	if batchFacts > contracts.MaxUpstreamIntelligenceBatchFacts {
		return fmt.Errorf("batch facts must contain at most %d items", contracts.MaxUpstreamIntelligenceBatchFacts)
	}
	if req.Run.FactCount < batchFacts || req.Run.FactCount > req.Manifest.BatchCount*contracts.MaxUpstreamIntelligenceBatchFacts ||
		req.Run.PageCount < 0 || req.Run.PageCount > maxUpstreamIntelligencePages ||
		req.Run.StartedAt.IsZero() || req.Run.ObservedAt.IsZero() || req.Run.ObservedAt.Before(req.Run.StartedAt) {
		return errors.New("collection run counts or timestamps are invalid")
	}
	if req.Run.Status != contracts.UpstreamCollectionSucceeded && req.Run.Status != contracts.UpstreamCollectionPartial && req.Run.Status != contracts.UpstreamCollectionFailed ||
		req.Run.CompletedAt == nil || req.Run.CompletedAt.Before(req.Run.ObservedAt) ||
		req.Run.Status == contracts.UpstreamCollectionSucceeded && req.Run.Coverage != contracts.UpstreamCoverageComplete ||
		req.Run.Status == contracts.UpstreamCollectionPartial && req.Run.Coverage != contracts.UpstreamCoveragePartial ||
		req.Run.Status == contracts.UpstreamCollectionFailed && req.Run.Coverage != contracts.UpstreamCoverageUnavailable ||
		req.Run.Trigger != contracts.UpstreamCollectionScheduled && req.Run.Trigger != contracts.UpstreamCollectionManual && req.Run.Trigger != contracts.UpstreamCollectionTask ||
		req.Run.SnapshotHash != "" && !contracts.IsUpstreamIntelligenceSHA256(req.Run.SnapshotHash) {
		return errors.New("collection run state is invalid")
	}
	if req.Run.Status == contracts.UpstreamCollectionFailed {
		if !contracts.IsUpstreamCollectionErrorCode(req.Run.ErrorCode) {
			return errors.New("failed collection run requires a stable error code")
		}
	} else if req.Run.ErrorCode != "" || req.Run.Retryable {
		return errors.New("successful or partial collection run cannot carry a terminal error")
	}
	// Empty terminal snapshots have deliberately narrow semantics. A complete
	// source may genuinely expose an empty catalog, and a failed source may
	// report no facts. Partial-with-no-evidence is ambiguous and cannot advance
	// the fact version or later participate in absence detection.
	if req.Run.FactCount == 0 {
		if req.Manifest.BatchCount != 1 || req.BatchNo != 0 || req.Run.PageCount > 1 ||
			(req.Run.Status != contracts.UpstreamCollectionSucceeded && req.Run.Status != contracts.UpstreamCollectionFailed) {
			return errors.New("zero-fact collection run shape is invalid")
		}
	}
	latestAllowed := now.Add(upstreamIntelligenceClockSkew)
	if req.Run.StartedAt.After(latestAllowed) || req.Run.ObservedAt.After(latestAllowed) || req.Run.CompletedAt != nil && req.Run.CompletedAt.After(latestAllowed) {
		return errors.New("collection run timestamp is in the future")
	}
	seenWallets := make(map[string]struct{}, len(req.Wallets))
	for _, wallet := range req.Wallets {
		if err := validateUpstreamIntelligenceWireWallet(wallet, req.Run.ID, latestAllowed); err != nil {
			return err
		}
		if _, exists := seenWallets[wallet.ID]; exists {
			return errors.New("duplicate wallet observation id")
		}
		seenWallets[wallet.ID] = struct{}{}
		if req.Run.Status == contracts.UpstreamCollectionSucceeded && wallet.Coverage != contracts.UpstreamCoverageComplete {
			return errors.New("succeeded collection run contains incomplete wallet evidence")
		}
	}
	seenOffers := make(map[string]struct{}, len(req.Offers))
	seenOfferKeys := make(map[string]struct{}, len(req.Offers))
	for _, offer := range req.Offers {
		if err := validateUpstreamIntelligenceWireOffer(offer, req.Run.ID, latestAllowed); err != nil {
			return err
		}
		identity := offer.GroupKey + "\x00" + offer.ModelKey + "\x00" + string(offer.PriceDimension)
		if _, exists := seenOffers[offer.ID]; exists {
			return errors.New("duplicate offer observation id")
		}
		if _, exists := seenOfferKeys[identity]; exists {
			return errors.New("duplicate offer observation identity")
		}
		seenOffers[offer.ID], seenOfferKeys[identity] = struct{}{}, struct{}{}
		if req.Run.Status == contracts.UpstreamCollectionSucceeded && offer.Coverage != contracts.UpstreamCoverageComplete {
			return errors.New("succeeded collection run contains incomplete offer evidence")
		}
	}
	return nil
}

func validateUpstreamIntelligenceWireWallet(input contracts.UpstreamIntelligenceIngestWalletObservation, runID string, latestAllowed time.Time) error {
	if upstreamIntelligenceWalletContainsSensitiveValue(input) {
		return errUpstreamIntelligenceSensitiveValue
	}
	if !validUpstreamIntelligenceWireIdentifier(input.ID, 128) || input.RunID != runID || !validUpstreamIntelligenceAccuracy(input.Accuracy) ||
		!validUpstreamIntelligenceCoverage(input.Coverage) || input.ObservedAt.IsZero() || input.ObservedAt.After(latestAllowed) ||
		input.FreshUntil.Before(input.ObservedAt) || len(input.ReasonCode) > 64 || containsSensitiveUpstreamIntelligenceValue(input.ReasonCode) ||
		!validUpstreamIntelligenceConfidence(input.Accuracy, input.Confidence) ||
		input.Accuracy == contracts.UpstreamEvidenceUnknown && len(input.MissingFields) == 0 && strings.TrimSpace(input.ReasonCode) == "" {
		return errors.New("wallet observation is invalid")
	}
	if input.UnitKind != contracts.UpstreamWalletFiat && input.UnitKind != contracts.UpstreamWalletCredit && input.UnitKind != contracts.UpstreamWalletUnknown ||
		input.UnitKind == contracts.UpstreamWalletFiat && !validUpstreamIntelligenceCurrency(input.Currency) ||
		input.UnitKind != contracts.UpstreamWalletFiat && input.Currency != "" || !validUpstreamIntelligenceStringList(input.MissingFields) {
		return errors.New("wallet observation unit or evidence is invalid")
	}
	return nil
}

func validateUpstreamIntelligenceWireOffer(input contracts.UpstreamIntelligenceIngestOfferObservation, runID string, latestAllowed time.Time) error {
	if upstreamIntelligenceOfferContainsSensitiveValue(input) {
		return errUpstreamIntelligenceSensitiveValue
	}
	if !validUpstreamIntelligenceWireIdentifier(input.ID, 128) || input.RunID != runID ||
		!validUpstreamIntelligenceWireIdentifier(input.GroupKey, 128) || !validUpstreamIntelligenceWireIdentifier(input.ModelKey, 256) ||
		input.PriceDimension != contracts.UpstreamPriceInput && input.PriceDimension != contracts.UpstreamPriceOutput &&
			input.PriceDimension != contracts.UpstreamPriceCachedInput && input.PriceDimension != contracts.UpstreamPriceRequest ||
		input.SettlementCurrency != "" && !validUpstreamIntelligenceCurrency(input.SettlementCurrency) || input.PerTokens <= 0 ||
		input.AdapterSchemaVersion <= 0 || input.AdapterSchemaVersion > 1_000 || !validUpstreamIntelligenceAccuracy(input.Accuracy) ||
		!validUpstreamIntelligenceCoverage(input.Coverage) || !validUpstreamIntelligenceConfidence(input.Accuracy, input.Confidence) ||
		input.ObservedAt.IsZero() || input.EffectiveAt.IsZero() || input.ObservedAt.After(latestAllowed) || input.EffectiveAt.After(latestAllowed) ||
		input.FreshUntil.Before(input.ObservedAt) || input.ValidUntil != nil && !input.ValidUntil.After(input.EffectiveAt) ||
		len(input.ReasonCode) > 64 || containsSensitiveUpstreamIntelligenceValue(input.ReasonCode) ||
		input.Accuracy == contracts.UpstreamEvidenceUnknown && len(input.MissingFields) == 0 && strings.TrimSpace(input.ReasonCode) == "" ||
		!validUpstreamIntelligenceStringList(input.MissingFields) || len(input.SourceRevision) > 128 || containsSensitiveUpstreamIntelligenceValue(input.SourceRevision) {
		return errors.New("offer observation is invalid")
	}
	if input.GroupMultiplier != nil && !validUpstreamIntelligenceDecimalSign(*input.GroupMultiplier, false) ||
		input.RechargeYield != nil && !validUpstreamIntelligenceDecimalSign(*input.RechargeYield, true) ||
		input.PublishedUnitPrice != nil && !validUpstreamIntelligenceDecimalSign(*input.PublishedUnitPrice, false) {
		return errors.New("offer observation decimal is invalid")
	}
	return nil
}

func validUpstreamIntelligenceAccuracy(value contracts.UpstreamEvidenceAccuracy) bool {
	return value == contracts.UpstreamEvidenceExact || value == contracts.UpstreamEvidenceDerived || value == contracts.UpstreamEvidenceEstimated ||
		value == contracts.UpstreamEvidenceUnknown || value == contracts.UpstreamEvidenceUnattributed
}

func validUpstreamIntelligenceCoverage(value contracts.UpstreamEvidenceCoverage) bool {
	return value == contracts.UpstreamCoverageComplete || value == contracts.UpstreamCoveragePartial || value == contracts.UpstreamCoverageUnavailable
}

func validUpstreamIntelligenceConfidence(accuracy contracts.UpstreamEvidenceAccuracy, value *contracts.CanonicalDecimal) bool {
	if value == nil {
		return true
	}
	if accuracy != contracts.UpstreamEvidenceDerived && accuracy != contracts.UpstreamEvidenceEstimated {
		return false
	}
	rat, err := value.Rat()
	return err == nil && rat.Sign() >= 0 && rat.Cmp(new(big.Rat).SetInt64(1)) <= 0
}

func validUpstreamIntelligenceDecimalSign(value contracts.CanonicalDecimal, positive bool) bool {
	rat, err := value.Rat()
	return err == nil && (positive && rat.Sign() > 0 || !positive && rat.Sign() >= 0)
}

func validUpstreamIntelligenceCurrency(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func validUpstreamIntelligenceDisplayName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0 &&
		!containsSensitiveUpstreamIntelligenceValue(value)
}

func upstreamIntelligenceWalletContainsSensitiveValue(input contracts.UpstreamIntelligenceIngestWalletObservation) bool {
	return containsSensitiveUpstreamIntelligenceValue(input.ID) || containsSensitiveUpstreamIntelligenceValue(input.RunID) ||
		containsSensitiveUpstreamIntelligenceValue(input.ReasonCode) || upstreamIntelligenceStringListContainsSensitiveValue(input.MissingFields)
}

func upstreamIntelligenceOfferContainsSensitiveValue(input contracts.UpstreamIntelligenceIngestOfferObservation) bool {
	return containsSensitiveUpstreamIntelligenceValue(input.ID) || containsSensitiveUpstreamIntelligenceValue(input.RunID) ||
		containsSensitiveUpstreamIntelligenceValue(input.GroupKey) || containsSensitiveUpstreamIntelligenceValue(input.ModelKey) ||
		containsSensitiveUpstreamIntelligenceValue(input.ReasonCode) || containsSensitiveUpstreamIntelligenceValue(input.SourceRevision) ||
		upstreamIntelligenceStringListContainsSensitiveValue(input.MissingFields)
}

func upstreamIntelligenceStringListContainsSensitiveValue(values []string) bool {
	for _, value := range values {
		if containsSensitiveUpstreamIntelligenceValue(value) {
			return true
		}
	}
	return false
}

func validUpstreamIntelligenceWireIdentifier(value string, maxBytes int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 || containsSensitiveUpstreamIntelligenceValue(value) {
		return false
	}
	return true
}

func validUpstreamIntelligenceStringList(values []string) bool {
	if len(values) > 64 {
		return false
	}
	for _, value := range values {
		if !validUpstreamIntelligenceWireIdentifier(value, 64) {
			return false
		}
	}
	return true
}

func containsSensitiveUpstreamIntelligenceValue(value string) bool {
	return strings.TrimSpace(value) != "" && contracts.LooksLikeConnectorSensitiveValue(value)
}

func upstreamIngestRunToStored(input contracts.UpstreamIntelligenceIngestRun, connector contracts.Connector, sourceID string, manifest contracts.UpstreamIngestBatchManifest) contracts.UpstreamCollectionRun {
	return contracts.UpstreamCollectionRun{
		ID: input.ID, UserID: connector.UserID, SourceID: sourceID, ConnectorID: connector.ID,
		Trigger: input.Trigger, Status: input.Status, Coverage: input.Coverage,
		StartedAt: input.StartedAt, ObservedAt: input.ObservedAt, CompletedAt: input.CompletedAt,
		SnapshotHash: input.SnapshotHash, ManifestHash: manifest.ManifestHash, BatchCount: manifest.BatchCount,
		FactCount: input.FactCount, PageCount: input.PageCount, ErrorCode: input.ErrorCode, Retryable: input.Retryable,
	}
}

func upstreamIngestWalletToStored(input contracts.UpstreamIntelligenceIngestWalletObservation, userID int64, sourceID, runID string) contracts.UpstreamWalletObservation {
	return contracts.UpstreamWalletObservation{
		ID: input.ID, RunID: runID, UserID: userID, SourceID: sourceID,
		BalanceAmount: input.BalanceAmount, UnitKind: input.UnitKind, Currency: input.Currency,
		Accuracy: input.Accuracy, Coverage: input.Coverage, Confidence: input.Confidence,
		ObservedAt: input.ObservedAt, FreshUntil: input.FreshUntil,
		MissingFields: input.MissingFields, ReasonCode: input.ReasonCode,
	}
}

func upstreamIngestOfferToStored(input contracts.UpstreamIntelligenceIngestOfferObservation, userID int64, sourceID, runID string) (contracts.UpstreamOfferObservation, error) {
	observation := contracts.UpstreamOfferObservation{
		ID: input.ID, RunID: runID, UserID: userID, SourceID: sourceID,
		GroupKey: input.GroupKey, ModelKey: input.ModelKey, PriceDimension: input.PriceDimension,
		SettlementCurrency: input.SettlementCurrency, GroupMultiplier: input.GroupMultiplier,
		RechargeYield: input.RechargeYield, PublishedUnitPrice: input.PublishedUnitPrice,
		PerTokens: input.PerTokens, Accuracy: input.Accuracy, Coverage: input.Coverage,
		Confidence: input.Confidence, ObservedAt: input.ObservedAt, EffectiveAt: input.EffectiveAt,
		FreshUntil: input.FreshUntil, ValidUntil: input.ValidUntil, MissingFields: input.MissingFields,
		ReasonCode: input.ReasonCode, AdapterSchemaVersion: input.AdapterSchemaVersion,
		SourceRevision: input.SourceRevision,
	}
	if input.GroupMultiplier != nil && input.RechargeYield != nil {
		value, err := contracts.CalculateEffectiveMultiplier(*input.GroupMultiplier, *input.RechargeYield)
		if err != nil {
			return contracts.UpstreamOfferObservation{}, err
		}
		observation.EffectiveMultiplier = &value
		// A numeric price without an identified settlement currency is useful
		// evidence but is not a comparable monetary cost. Preserve the published
		// input and multiplier while leaving effective cost explicitly unknown.
		if input.PublishedUnitPrice != nil && input.SettlementCurrency != "" {
			cost, err := contracts.CalculateEffectiveUnitCost(*input.PublishedUnitPrice, value)
			if err != nil {
				return contracts.UpstreamOfferObservation{}, err
			}
			observation.EffectiveUnitCost = &cost
		}
		observation.FormulaVersion = "effective-cost/v1"
	}
	return observation, nil
}

func writeUpstreamIntelligenceStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "upstream_intelligence_not_found", "upstream intelligence scope was not found")
	case errors.Is(err, store.ErrDuplicate), errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "upstream_intelligence_conflict", "upstream intelligence facts conflict with an existing idempotency key")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", "upstream intelligence facts are invalid")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "upstream intelligence facts could not be stored")
	}
}

var _ = time.Time{}
