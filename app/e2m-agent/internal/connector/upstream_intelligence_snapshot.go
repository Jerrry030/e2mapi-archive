package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/agent/internal/adapters/sub2api"
	"e2m.local/contracts"
)

const (
	upstreamIntelligenceAdapterSchemaVersion = 1
	upstreamIntelligenceSnapshotHashDomain   = "e2m.sub2api-intelligence.snapshot.v1"
	upstreamIntelligenceIdentityHashDomain   = "e2m.sub2api-intelligence.observation.v1"
)

// UpstreamIntelligenceSnapshotEnvelope contains the caller-owned run metadata
// needed to turn one already collected, sanitized Sub2API snapshot into Core's
// ingest contract. It intentionally contains no URL, credential, header, raw
// response, Connector identity, or Core owner/source identity.
type UpstreamIntelligenceSnapshotEnvelope struct {
	Source      UpstreamIntelligenceSourcePublic
	RunID       string
	Trigger     contracts.UpstreamCollectionTrigger
	StartedAt   time.Time
	ObservedAt  time.Time
	CompletedAt time.Time
	FreshUntil  time.Time
	Currency    string
	Snapshot    sub2api.IntelligenceSnapshot
}

// BuildUpstreamIntelligenceSnapshotBatches is a pure, deterministic packaging
// step. All facts are normalized and sorted before stable IDs, snapshot hash,
// batch leaves, and the ordered run manifest are calculated.
func BuildUpstreamIntelligenceSnapshotBatches(input UpstreamIntelligenceSnapshotEnvelope) ([]contracts.UpstreamIntelligenceIngestBatchRequest, error) {
	normalized, err := normalizeUpstreamIntelligenceSnapshotEnvelope(input)
	if err != nil {
		return nil, err
	}

	wallets, offers, err := upstreamIntelligenceSnapshotFacts(normalized)
	if err != nil {
		return nil, err
	}
	factCount := len(wallets) + len(offers)
	status, errorCode, retryable, err := upstreamIntelligenceTerminalState(normalized.Snapshot, factCount)
	if err != nil {
		return nil, err
	}

	snapshotHash, err := calculateUpstreamIntelligenceSnapshotHash(wallets, offers)
	if err != nil {
		return nil, err
	}
	batchCount := (factCount + contracts.MaxUpstreamIntelligenceBatchFacts - 1) / contracts.MaxUpstreamIntelligenceBatchFacts
	if batchCount == 0 {
		batchCount = 1
	}
	completedAt := normalized.CompletedAt
	run := contracts.UpstreamIntelligenceIngestRun{
		ID: normalized.RunID, Trigger: normalized.Trigger, Status: status, Coverage: normalized.Snapshot.Coverage,
		StartedAt: normalized.StartedAt, ObservedAt: normalized.ObservedAt, CompletedAt: &completedAt,
		SnapshotHash: snapshotHash, BatchCount: batchCount, FactCount: factCount, PageCount: 1,
		ErrorCode: errorCode, Retryable: retryable,
	}
	source := upstreamIntelligenceSourceRegistration(normalized)

	requests := make([]contracts.UpstreamIntelligenceIngestBatchRequest, batchCount)
	for batchNo := 0; batchNo < batchCount; batchNo++ {
		requests[batchNo] = contracts.UpstreamIntelligenceIngestBatchRequest{
			SchemaVersion: contracts.UpstreamIntelligenceSchemaVersion,
			Source:        source,
			Run:           run,
			Manifest:      contracts.UpstreamIngestBatchManifest{BatchCount: batchCount},
			BatchNo:       batchNo,
		}
	}

	// Wallet facts sort before offer facts. This keeps partitioning stable even
	// if the adapter returns its independently set-like collections reordered.
	factIndex := 0
	for _, wallet := range wallets {
		batchNo := factIndex / contracts.MaxUpstreamIntelligenceBatchFacts
		requests[batchNo].Wallets = append(requests[batchNo].Wallets, wallet)
		factIndex++
	}
	for _, offer := range offers {
		batchNo := factIndex / contracts.MaxUpstreamIntelligenceBatchFacts
		requests[batchNo].Offers = append(requests[batchNo].Offers, offer)
		factIndex++
	}

	manifestLeaves := make([]contracts.UpstreamIntelligenceManifestBatch, batchCount)
	for index := range requests {
		payloadHash, err := contracts.CalculateUpstreamIntelligencePayloadHash(requests[index])
		if err != nil {
			return nil, fmt.Errorf("calculate upstream intelligence batch %d hash: %w", index, err)
		}
		requests[index].PayloadHash = payloadHash
		manifestLeaves[index] = contracts.UpstreamIntelligenceManifestBatch{BatchNo: index, PayloadHash: payloadHash}
	}
	manifestHash, err := contracts.CalculateUpstreamIntelligenceManifestHash(manifestLeaves)
	if err != nil {
		return nil, fmt.Errorf("calculate upstream intelligence manifest: %w", err)
	}
	for index := range requests {
		requests[index].Manifest.ManifestHash = manifestHash
	}
	return requests, nil
}

func normalizeUpstreamIntelligenceSnapshotEnvelope(input UpstreamIntelligenceSnapshotEnvelope) (UpstreamIntelligenceSnapshotEnvelope, error) {
	input.Source.LocalRef = strings.TrimSpace(input.Source.LocalRef)
	input.Source.Provider = strings.ToLower(strings.TrimSpace(input.Source.Provider))
	input.Source.DisplayName = strings.TrimSpace(input.Source.DisplayName)
	input.Source.Currency = strings.ToUpper(strings.TrimSpace(input.Source.Currency))
	input.RunID = strings.TrimSpace(input.RunID)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.StartedAt = canonicalUpstreamIntelligenceTime(input.StartedAt)
	input.ObservedAt = canonicalUpstreamIntelligenceTime(input.ObservedAt)
	input.CompletedAt = canonicalUpstreamIntelligenceTime(input.CompletedAt)
	input.FreshUntil = canonicalUpstreamIntelligenceTime(input.FreshUntil)

	if input.Source.Provider == "" {
		input.Source.Provider = "sub2api"
	}
	if input.Currency == "" {
		input.Currency = input.Source.Currency
	}
	if !validUpstreamIntelligenceEnvelopeIdentifier(input.Source.LocalRef, 128) || !validUpstreamIntelligenceEnvelopeIdentifier(input.RunID, 128) {
		return input, errors.New("source local reference and run ID are required safe identifiers")
	}
	if input.Source.Provider != "sub2api" {
		return input, errors.New("source provider must be sub2api")
	}
	if input.Source.Mode != UpstreamIntelligenceSourceOwned && input.Source.Mode != UpstreamIntelligenceSourceExternal {
		return input, errors.New("source mode must be owned or external")
	}
	if input.Source.Status != UpstreamIntelligenceSourceActive && input.Source.Status != UpstreamIntelligenceSourcePaused {
		return input, errors.New("source must be active or paused")
	}
	if input.Source.DisplayName == "" || len(input.Source.DisplayName) > 128 || !utf8.ValidString(input.Source.DisplayName) ||
		strings.IndexFunc(input.Source.DisplayName, unicode.IsControl) >= 0 || contracts.LooksLikeConnectorSensitiveValue(input.Source.DisplayName) {
		return input, errors.New("source display name is invalid")
	}
	if input.Source.PollIntervalSeconds < 60 || input.Source.PollIntervalSeconds > 3600 {
		return input, errors.New("source poll interval is out of bounds")
	}
	if input.Source.Currency != "" && !validUpstreamIntelligenceCurrency(input.Source.Currency) || input.Currency != "" && !validUpstreamIntelligenceCurrency(input.Currency) {
		return input, errors.New("currency must be a three-letter ISO 4217 code")
	}
	if input.Trigger != contracts.UpstreamCollectionScheduled && input.Trigger != contracts.UpstreamCollectionManual && input.Trigger != contracts.UpstreamCollectionTask {
		return input, errors.New("collection trigger is invalid")
	}
	if input.StartedAt.IsZero() || input.ObservedAt.IsZero() || input.CompletedAt.IsZero() || input.FreshUntil.IsZero() ||
		input.ObservedAt.Before(input.StartedAt) || input.CompletedAt.Before(input.ObservedAt) || input.FreshUntil.Before(input.ObservedAt) {
		return input, errors.New("collection timestamps are invalid")
	}
	if input.Snapshot.Coverage != contracts.UpstreamCoverageComplete && input.Snapshot.Coverage != contracts.UpstreamCoveragePartial &&
		input.Snapshot.Coverage != contracts.UpstreamCoverageUnavailable {
		return input, errors.New("snapshot coverage is invalid")
	}
	return input, nil
}

func upstreamIntelligenceSourceRegistration(input UpstreamIntelligenceSnapshotEnvelope) contracts.UpstreamIntelligenceIngestSourceRegistration {
	status := contracts.UpstreamSourceActive
	if input.Source.Status == UpstreamIntelligenceSourcePaused {
		status = contracts.UpstreamSourcePaused
	}
	return contracts.UpstreamIntelligenceIngestSourceRegistration{
		LocalRef: input.Source.LocalRef, Mode: contracts.UpstreamIntelligenceSourceMode(input.Source.Mode),
		Provider: input.Source.Provider, DisplayName: input.Source.DisplayName, Currency: input.Currency,
		PollIntervalSeconds: input.Source.PollIntervalSeconds, Status: status,
		Capabilities: upstreamIntelligenceCapabilities(input.Snapshot),
	}
}

func upstreamIntelligenceCapabilities(snapshot sub2api.IntelligenceSnapshot) contracts.UpstreamIntelligenceCapabilities {
	return contracts.UpstreamIntelligenceCapabilities{
		Balance: upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointProfile),
		Groups:  upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointGroups),
		Rates:   upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointRates),
		Prices:  upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointChannels),
	}
}

func upstreamIntelligenceSnapshotFacts(input UpstreamIntelligenceSnapshotEnvelope) ([]contracts.UpstreamIntelligenceIngestWalletObservation, []contracts.UpstreamIntelligenceIngestOfferObservation, error) {
	coverage := input.Snapshot.Coverage
	walletCoverage := upstreamIntelligenceEndpointCoverage(input.Snapshot, sub2api.IntelligenceEndpointProfile)
	catalogCoverage := upstreamIntelligenceCatalogCoverage(input.Snapshot)
	wallets := make([]contracts.UpstreamIntelligenceIngestWalletObservation, 0, 1)
	if input.Snapshot.Wallet != nil {
		wallet := input.Snapshot.Wallet
		if wallet.UnitKind != contracts.UpstreamWalletFiat && wallet.UnitKind != contracts.UpstreamWalletCredit && wallet.UnitKind != contracts.UpstreamWalletUnknown {
			return nil, nil, errors.New("wallet unit kind is invalid")
		}
		if !wallet.Balance.Valid() {
			return nil, nil, errors.New("wallet balance is invalid")
		}
		currency := ""
		if wallet.UnitKind == contracts.UpstreamWalletFiat {
			currency = input.Currency
			if currency == "" {
				return nil, nil, errors.New("fiat wallet requires a settlement currency")
			}
		}
		fact := contracts.UpstreamIntelligenceIngestWalletObservation{
			RunID: input.RunID, BalanceAmount: decimalCopy(wallet.Balance), UnitKind: wallet.UnitKind, Currency: currency,
			Accuracy: contracts.UpstreamEvidenceExact, Coverage: walletCoverage,
			ObservedAt: input.ObservedAt, FreshUntil: input.FreshUntil,
		}
		fact.ID = stableUpstreamIntelligenceObservationID("wallet", input.Source.LocalRef, input.RunID, string(wallet.UnitKind), currency)
		wallets = append(wallets, fact)
	}

	groupRates := make(map[int64]contracts.CanonicalDecimal, len(input.Snapshot.Groups))
	for _, group := range input.Snapshot.Groups {
		if group.ID <= 0 {
			return nil, nil, errors.New("group ID must be positive")
		}
		rate := group.DefaultRate
		if group.EffectiveRate != nil {
			rate = *group.EffectiveRate
		}
		if !rate.Valid() {
			return nil, nil, errors.New("group multiplier is invalid")
		}
		if existing, duplicate := groupRates[group.ID]; duplicate && existing != rate {
			return nil, nil, fmt.Errorf("conflicting multiplier for group %d", group.ID)
		}
		groupRates[group.ID] = rate
	}

	offers := make([]contracts.UpstreamIntelligenceIngestOfferObservation, 0)
	seenIdentities := make(map[string]struct{})
	for _, channel := range input.Snapshot.Channels {
		for _, platform := range channel.Platforms {
			for _, group := range platform.Groups {
				if group.ID <= 0 {
					return nil, nil, errors.New("channel group ID must be positive")
				}
				rate := group.DefaultRate
				if effective, ok := groupRates[group.ID]; ok {
					rate = effective
				}
				if !rate.Valid() {
					return nil, nil, errors.New("channel group multiplier is invalid")
				}
				for _, model := range platform.Models {
					facts, err := upstreamIntelligenceModelOffers(input, platform.Platform, group.ID, model, rate, catalogCoverage)
					if err != nil {
						return nil, nil, err
					}
					for _, fact := range facts {
						identity := fact.GroupKey + "\x00" + fact.ModelKey + "\x00" + string(fact.PriceDimension)
						if _, duplicate := seenIdentities[identity]; duplicate {
							return nil, nil, fmt.Errorf("duplicate offer identity for group %s, model %s, dimension %s", fact.GroupKey, fact.ModelKey, fact.PriceDimension)
						}
						seenIdentities[identity] = struct{}{}
						offers = append(offers, fact)
					}
				}
			}
		}
	}

	// Coverage is kept on each fact. This guard prevents a hand-crafted
	// impossible snapshot from claiming a complete run with partial evidence.
	if coverage == contracts.UpstreamCoverageComplete {
		for _, wallet := range wallets {
			if wallet.Coverage != contracts.UpstreamCoverageComplete {
				return nil, nil, errors.New("complete snapshot contains incomplete wallet evidence")
			}
		}
		for _, offer := range offers {
			if offer.Coverage != contracts.UpstreamCoverageComplete {
				return nil, nil, errors.New("complete snapshot contains incomplete offer evidence")
			}
		}
	}

	sort.Slice(wallets, func(i, j int) bool { return wallets[i].ID < wallets[j].ID })
	sort.Slice(offers, func(i, j int) bool { return upstreamIntelligenceOfferLess(offers[i], offers[j]) })
	return wallets, offers, nil
}

func upstreamIntelligenceModelOffers(input UpstreamIntelligenceSnapshotEnvelope, platform string, groupID int64, model sub2api.IntelligenceModel, multiplier contracts.CanonicalDecimal, coverage contracts.UpstreamEvidenceCoverage) ([]contracts.UpstreamIntelligenceIngestOfferObservation, error) {
	// ReferencePricing is an upstream reference/catalog comparison, not the
	// station's published settlement price. It is deliberately ignored. Tiered
	// interval rows are likewise omitted because the v1 offer identity has no
	// interval dimension and flattening them would silently change semantics.
	pricing := model.SitePricing
	if pricing == nil {
		return nil, nil
	}
	if pricing.PerTokens <= 0 {
		return nil, errors.New("site pricing per_tokens must be positive")
	}
	if input.Snapshot.RechargeYield.Value != nil {
		yield, err := input.Snapshot.RechargeYield.Value.Rat()
		if err != nil || yield.Sign() <= 0 {
			return nil, errors.New("recharge yield must be a positive canonical decimal")
		}
	}
	groupKey := strconv.FormatInt(groupID, 10)
	modelKey := strings.TrimSpace(model.Name)
	if modelKey == "" || len(modelKey) > 256 || contracts.LooksLikeConnectorSensitiveValue(modelKey) {
		return nil, errors.New("model key is invalid")
	}
	if platform = strings.TrimSpace(platform); platform == "" || model.Platform != platform {
		return nil, errors.New("model platform does not match its catalog platform")
	}

	type dimensionPrice struct {
		dimension contracts.UpstreamPriceDimension
		price     *contracts.CanonicalDecimal
	}
	prices := []dimensionPrice{
		{contracts.UpstreamPriceInput, pricing.InputPrice},
		{contracts.UpstreamPriceOutput, pricing.OutputPrice},
		// Sub2API exposes cache_write and cache_read. The v1 contract has one
		// cached_input dimension, so only cache-read (cached input consumption)
		// is a safe exact mapping; cache-write is not silently relabelled.
		{contracts.UpstreamPriceCachedInput, pricing.CacheReadPrice},
		{contracts.UpstreamPriceRequest, pricing.PerRequestPrice},
	}
	offers := make([]contracts.UpstreamIntelligenceIngestOfferObservation, 0, len(prices))
	for _, candidate := range prices {
		if candidate.price == nil {
			continue
		}
		if !candidate.price.Valid() {
			return nil, errors.New("site published price is invalid")
		}
		perTokens := pricing.PerTokens
		if candidate.dimension == contracts.UpstreamPriceRequest {
			perTokens = 1
		}
		fact := contracts.UpstreamIntelligenceIngestOfferObservation{
			RunID: input.RunID, GroupKey: groupKey, ModelKey: modelKey, PriceDimension: candidate.dimension,
			SettlementCurrency: input.Currency, GroupMultiplier: decimalCopy(multiplier),
			PublishedUnitPrice: decimalCopy(*candidate.price), PerTokens: perTokens,
			Accuracy: contracts.UpstreamEvidenceExact, Coverage: coverage,
			ObservedAt: input.ObservedAt, EffectiveAt: input.ObservedAt, FreshUntil: input.FreshUntil,
			AdapterSchemaVersion: upstreamIntelligenceAdapterSchemaVersion,
		}
		if input.Snapshot.RechargeYield.Value != nil {
			fact.RechargeYield = decimalCopy(*input.Snapshot.RechargeYield.Value)
		} else {
			fact.MissingFields = []string{"recharge_yield"}
			fact.ReasonCode = stableUpstreamIntelligenceReason(input.Snapshot.RechargeYield.ReasonCode, "recharge_yield_not_exposed")
		}
		fact.ID = stableUpstreamIntelligenceObservationID("offer", input.Source.LocalRef, input.RunID, groupKey, modelKey, string(candidate.dimension))
		offers = append(offers, fact)
	}
	return offers, nil
}

func upstreamIntelligenceTerminalState(snapshot sub2api.IntelligenceSnapshot, factCount int) (contracts.UpstreamCollectionStatus, string, bool, error) {
	switch snapshot.Coverage {
	case contracts.UpstreamCoverageComplete:
		return contracts.UpstreamCollectionSucceeded, "", false, nil
	case contracts.UpstreamCoveragePartial:
		if factCount == 0 {
			return "", "", false, errors.New("partial snapshot must contain at least one usable fact")
		}
		return contracts.UpstreamCollectionPartial, "", false, nil
	case contracts.UpstreamCoverageUnavailable:
		if factCount != 0 {
			return "", "", false, errors.New("unavailable snapshot must not contain facts")
		}
		code, retryable := upstreamIntelligenceRunFailure(snapshot)
		return contracts.UpstreamCollectionFailed, code, retryable, nil
	default:
		return "", "", false, errors.New("snapshot coverage is invalid")
	}
}

func upstreamIntelligenceRunFailure(snapshot sub2api.IntelligenceSnapshot) (string, bool) {
	states := append([]sub2api.IntelligenceEndpointState(nil), snapshot.Endpoints...)
	sort.Slice(states, func(i, j int) bool {
		return upstreamIntelligenceFailurePriority(states[i].ErrorCode) < upstreamIntelligenceFailurePriority(states[j].ErrorCode)
	})
	for _, state := range states {
		if state.Available || state.ErrorCode == "" {
			continue
		}
		if code := mapUpstreamIntelligenceErrorCode(state.ErrorCode); code != "" {
			return code, state.Retryable
		}
	}
	return contracts.UpstreamCollectionErrorUpstreamUnavailable, true
}

func upstreamIntelligenceFailurePriority(code sub2api.IntelligenceErrorCode) int {
	switch code {
	case sub2api.IntelligenceAuthFailed:
		return 0
	case sub2api.IntelligenceRateLimited:
		return 1
	case sub2api.IntelligenceSchemaUnsupported:
		return 2
	case sub2api.IntelligenceResponseTooLarge:
		return 3
	case sub2api.IntelligenceUpstreamUnavailable:
		return 4
	default:
		return 5
	}
}

func mapUpstreamIntelligenceErrorCode(code sub2api.IntelligenceErrorCode) string {
	switch code {
	case sub2api.IntelligenceAuthFailed:
		return contracts.UpstreamCollectionErrorAuthFailed
	case sub2api.IntelligenceRateLimited:
		return contracts.UpstreamCollectionErrorRateLimited
	case sub2api.IntelligenceSchemaUnsupported:
		return contracts.UpstreamCollectionErrorSchemaUnsupported
	case sub2api.IntelligenceResponseTooLarge:
		return contracts.UpstreamCollectionErrorResponseTooLarge
	case sub2api.IntelligenceUpstreamUnavailable:
		return contracts.UpstreamCollectionErrorUpstreamUnavailable
	default:
		return ""
	}
}

func upstreamIntelligenceEndpointAvailable(snapshot sub2api.IntelligenceSnapshot, endpoint sub2api.IntelligenceEndpoint) bool {
	for _, state := range snapshot.Endpoints {
		if state.Endpoint == endpoint {
			return state.Available
		}
	}
	return false
}

func upstreamIntelligenceEndpointCoverage(snapshot sub2api.IntelligenceSnapshot, endpoint sub2api.IntelligenceEndpoint) contracts.UpstreamEvidenceCoverage {
	if !upstreamIntelligenceEndpointAvailable(snapshot, endpoint) {
		return contracts.UpstreamCoverageUnavailable
	}
	return contracts.UpstreamCoverageComplete
}

func upstreamIntelligenceCatalogCoverage(snapshot sub2api.IntelligenceSnapshot) contracts.UpstreamEvidenceCoverage {
	if !upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointChannels) {
		return contracts.UpstreamCoverageUnavailable
	}
	if upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointGroups) &&
		upstreamIntelligenceEndpointAvailable(snapshot, sub2api.IntelligenceEndpointRates) {
		return contracts.UpstreamCoverageComplete
	}
	return contracts.UpstreamCoveragePartial
}

func upstreamIntelligenceOfferLess(left, right contracts.UpstreamIntelligenceIngestOfferObservation) bool {
	if left.GroupKey != right.GroupKey {
		return left.GroupKey < right.GroupKey
	}
	if left.ModelKey != right.ModelKey {
		return left.ModelKey < right.ModelKey
	}
	if left.PriceDimension != right.PriceDimension {
		return left.PriceDimension < right.PriceDimension
	}
	return left.ID < right.ID
}

func calculateUpstreamIntelligenceSnapshotHash(wallets []contracts.UpstreamIntelligenceIngestWalletObservation, offers []contracts.UpstreamIntelligenceIngestOfferObservation) (string, error) {
	// Run-scoped identity and observation/freshness timestamps are intentionally
	// removed: snapshot_hash describes the normalized evidence, not its delivery.
	normalizedWallets := append([]contracts.UpstreamIntelligenceIngestWalletObservation(nil), wallets...)
	for index := range normalizedWallets {
		normalizedWallets[index].ID = ""
		normalizedWallets[index].RunID = ""
		normalizedWallets[index].ObservedAt = time.Time{}
		normalizedWallets[index].FreshUntil = time.Time{}
		normalizedWallets[index].MissingFields = sortedUpstreamIntelligenceStrings(normalizedWallets[index].MissingFields)
	}
	normalizedOffers := append([]contracts.UpstreamIntelligenceIngestOfferObservation(nil), offers...)
	for index := range normalizedOffers {
		normalizedOffers[index].ID = ""
		normalizedOffers[index].RunID = ""
		normalizedOffers[index].ObservedAt = time.Time{}
		normalizedOffers[index].EffectiveAt = time.Time{}
		normalizedOffers[index].FreshUntil = time.Time{}
		normalizedOffers[index].ValidUntil = nil
		normalizedOffers[index].MissingFields = sortedUpstreamIntelligenceStrings(normalizedOffers[index].MissingFields)
	}
	sort.Slice(normalizedWallets, func(i, j int) bool {
		left, _ := json.Marshal(normalizedWallets[i])
		right, _ := json.Marshal(normalizedWallets[j])
		return string(left) < string(right)
	})
	sort.Slice(normalizedOffers, func(i, j int) bool { return upstreamIntelligenceOfferLess(normalizedOffers[i], normalizedOffers[j]) })
	if normalizedWallets == nil {
		normalizedWallets = make([]contracts.UpstreamIntelligenceIngestWalletObservation, 0)
	}
	if normalizedOffers == nil {
		normalizedOffers = make([]contracts.UpstreamIntelligenceIngestOfferObservation, 0)
	}
	payload := struct {
		Domain  string                                                  `json:"domain"`
		Wallets []contracts.UpstreamIntelligenceIngestWalletObservation `json:"wallets"`
		Offers  []contracts.UpstreamIntelligenceIngestOfferObservation  `json:"offers"`
	}{Domain: upstreamIntelligenceSnapshotHashDomain, Wallets: normalizedWallets, Offers: normalizedOffers}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode normalized upstream intelligence snapshot: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func stableUpstreamIntelligenceObservationID(kind string, parts ...string) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(upstreamIntelligenceIdentityHashDomain))
	for _, part := range append([]string{kind}, parts...) {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(part))
	}
	return "ui_" + kind + "_" + hex.EncodeToString(hasher.Sum(nil))
}

func canonicalUpstreamIntelligenceTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.Round(0).UTC()
}

func decimalCopy(value contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	copyValue := value
	return &copyValue
}

func sortedUpstreamIntelligenceStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func stableUpstreamIntelligenceReason(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || contracts.LooksLikeConnectorSensitiveValue(value) {
		return fallback
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fallback
		}
	}
	return value
}

func validUpstreamIntelligenceEnvelopeIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}
