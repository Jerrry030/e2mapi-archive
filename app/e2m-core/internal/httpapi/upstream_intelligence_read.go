package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamintelligence"
)

const (
	upstreamIntelligenceCursorVersion = 2
	// After an observation leaves its declared freshness interval it remains
	// stale for one more declared interval. This keeps the last successful fact
	// visible while making prolonged collection failures explicitly expired.
	upstreamIntelligenceMinimumStaleGrace = time.Minute
	upstreamIntelligenceOverviewListLimit = 10
	upstreamIntelligenceCursorTTL         = 15 * time.Minute
	upstreamIntelligenceCursorMaxKeys     = 4
	upstreamIntelligenceQualityMinSamples = 5
)

var (
	errUpstreamIntelligenceCursorUnavailable  = errors.New("upstream intelligence cursor signing is unavailable")
	upstreamIntelligenceAllowedSourceStatuses = upstreamIntelligenceStringSet(
		string(contracts.UpstreamSourceActive), string(contracts.UpstreamSourcePaused), string(contracts.UpstreamSourceDisconnected),
	)
	upstreamIntelligenceAllowedAccuracies = upstreamIntelligenceStringSet(
		string(contracts.UpstreamEvidenceExact), string(contracts.UpstreamEvidenceDerived), string(contracts.UpstreamEvidenceEstimated),
		string(contracts.UpstreamEvidenceUnknown), string(contracts.UpstreamEvidenceUnattributed),
	)
	upstreamIntelligenceAllowedCoverages = upstreamIntelligenceStringSet(
		string(contracts.UpstreamCoverageComplete), string(contracts.UpstreamCoveragePartial), string(contracts.UpstreamCoverageUnavailable),
	)
	upstreamIntelligenceAllowedFreshness = upstreamIntelligenceStringSet(
		string(contracts.UpstreamFreshnessCurrent), string(contracts.UpstreamFreshnessStale), string(contracts.UpstreamFreshnessExpired),
	)
	upstreamIntelligenceAllowedPriceDimensions = upstreamIntelligenceStringSet(
		string(contracts.UpstreamPriceInput), string(contracts.UpstreamPriceOutput),
		string(contracts.UpstreamPriceCachedInput), string(contracts.UpstreamPriceRequest),
	)
	upstreamIntelligenceAllowedChangeTypes = upstreamIntelligenceStringSet(
		string(contracts.UpstreamChangeBalanceLow), string(contracts.UpstreamChangeBalanceRecovered),
		string(contracts.UpstreamChangeGroupAdded), string(contracts.UpstreamChangeGroupChanged), string(contracts.UpstreamChangeGroupRemoved),
		string(contracts.UpstreamChangeModelAdded), string(contracts.UpstreamChangePriceIncreased),
		string(contracts.UpstreamChangePriceDecreased), string(contracts.UpstreamChangeModelRemoved),
		string(contracts.UpstreamChangeSourceStale), string(contracts.UpstreamChangeSourceRecovered),
	)
	upstreamIntelligenceAllowedSeverities = upstreamIntelligenceStringSet(
		string(contracts.UpstreamChangeInfo), string(contracts.UpstreamChangeWarning), string(contracts.UpstreamChangeCritical),
	)
	upstreamIntelligenceAllowedImpactScopeKeys = upstreamIntelligenceStringSet("model_key", "group_key", "price_dimension")
)

type upstreamIntelligenceReadProjection struct {
	metadata contracts.UpstreamIntelligenceReadMetadata
	sources  []contracts.UpstreamIntelligenceReadSourceSummary
	wallets  []contracts.UpstreamIntelligenceWalletReadModel
	rates    []contracts.UpstreamIntelligenceRateReadModel
	changes  []contracts.UpstreamIntelligenceChangeReadModel
	frontier []contracts.UpstreamIntelligenceFrontierPoint
}

type upstreamIntelligenceCursor struct {
	Version           int       `json:"v"`
	KeyID             string    `json:"kid"`
	Kind              string    `json:"kind"`
	UserID            int64     `json:"user_id"`
	FactVersion       int64     `json:"fact_version"`
	FilterFingerprint string    `json:"filter_fingerprint"`
	Offset            int       `json:"offset"`
	ReferenceTime     time.Time `json:"reference_time"`
	IssuedAt          int64     `json:"iat"`
	ExpiresAt         int64     `json:"exp"`
	Signature         string    `json:"signature"`
}

type upstreamIntelligenceQuery struct {
	userID        int64
	factVersion   *int64
	referenceTime *time.Time
	values        map[string]string
}

func upstreamIntelligenceStringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// ParseUpstreamIntelligenceCursorKeyring parses a comma-separated keyring in
// the form "kid=64-hex-chars,kid-previous=64-hex-chars". Cursor keys are kept
// separate from Vault encryption keys so either secret can be rotated without
// coupling two cryptographic purposes.
func ParseUpstreamIntelligenceCursorKeyring(raw string) (map[string][]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("cursor keyring must not be empty")
	}
	entries := strings.Split(raw, ",")
	if len(entries) > upstreamIntelligenceCursorMaxKeys {
		return nil, fmt.Errorf("cursor keyring must contain at most %d keys", upstreamIntelligenceCursorMaxKeys)
	}
	keys := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) != 2 {
			return nil, errors.New("cursor keyring entries must use kid=64-hex-chars")
		}
		kid := strings.TrimSpace(parts[0])
		if !validUpstreamIntelligenceCursorKeyID(kid) {
			return nil, errors.New("cursor keyring contains an invalid key id")
		}
		if _, exists := keys[kid]; exists {
			return nil, fmt.Errorf("duplicate cursor key id %q", kid)
		}
		key, err := hex.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil || len(key) != sha256.Size {
			return nil, fmt.Errorf("cursor key %q must be exactly 32 bytes encoded as 64 hex characters", kid)
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("cursor keyring must not be empty")
	}
	return keys, nil
}

// ConfigureUpstreamIntelligenceCursorKeyring installs a shared keyring. New
// cursors use activeKID; retained old keys continue to verify cursors during a
// rolling rotation. All Core replicas must receive the same keyring.
func (s *Server) ConfigureUpstreamIntelligenceCursorKeyring(activeKID string, input map[string][]byte) error {
	activeKID = strings.TrimSpace(activeKID)
	if !validUpstreamIntelligenceCursorKeyID(activeKID) {
		return errors.New("active cursor key id is invalid")
	}
	if len(input) == 0 || len(input) > upstreamIntelligenceCursorMaxKeys {
		return fmt.Errorf("cursor keyring must contain between 1 and %d keys", upstreamIntelligenceCursorMaxKeys)
	}
	keys := make(map[string][32]byte, len(input))
	for kid, raw := range input {
		if !validUpstreamIntelligenceCursorKeyID(kid) || len(raw) != sha256.Size {
			return fmt.Errorf("cursor key %q must have a safe id and exactly 32 bytes", kid)
		}
		var key [32]byte
		copy(key[:], raw)
		keys[kid] = key
	}
	if _, exists := keys[activeKID]; !exists {
		return fmt.Errorf("active cursor key id %q is not present in the keyring", activeKID)
	}
	s.intelligenceCursorMu.Lock()
	s.intelligenceCursorActiveKID = activeKID
	s.intelligenceCursorKeys = keys
	s.intelligenceCursorMu.Unlock()
	return nil
}

func validUpstreamIntelligenceCursorKeyID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handleUpstreamIntelligenceOverview(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "source_id", "model_key", "group_key", "provider", "currency", "window", "accuracy",
	), map[string]map[string]bool{
		"window":   upstreamIntelligenceStringSet(string(contracts.UpstreamIntelligenceWindow24h), string(contracts.UpstreamIntelligenceWindow7d)),
		"accuracy": upstreamIntelligenceAllowedAccuracies,
	}, nil)
	if !ok {
		return
	}
	projection, snapshot, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	filter := contracts.UpstreamIntelligenceOverviewFilter{
		SourceID: query.values["source_id"], ModelKey: query.values["model_key"], GroupKey: query.values["group_key"],
		Provider: query.values["provider"], Currency: query.values["currency"],
		Window: contracts.UpstreamIntelligenceReadWindow(query.values["window"]), Accuracy: contracts.UpstreamEvidenceAccuracy(query.values["accuracy"]),
	}
	if filter.Window == "" {
		filter.Window = contracts.UpstreamIntelligenceWindow24h
	}
	filteredSources := filterUpstreamIntelligenceSources(projection.sources, contracts.UpstreamIntelligenceSourcesFilter{
		Provider: filter.Provider, Currency: filter.Currency, Accuracy: filter.Accuracy,
	}, projection)
	if filter.SourceID != "" {
		filteredSources = filterUpstreamIntelligenceSourcesByID(filteredSources, filter.SourceID)
	}
	filteredRates := filterUpstreamIntelligenceRates(projection.rates, contracts.UpstreamIntelligenceRatesFilter{
		SourceID: filter.SourceID, ModelKey: filter.ModelKey, GroupKey: filter.GroupKey,
		Provider: filter.Provider, Currency: filter.Currency, Accuracy: filter.Accuracy,
	})
	filteredWallets := filterUpstreamIntelligenceWallets(projection.wallets, filter)
	scopedChanges := filterUpstreamIntelligenceChanges(projection.changes, contracts.UpstreamIntelligenceChangesFilter{
		SourceID: filter.SourceID, ModelKey: filter.ModelKey, GroupKey: filter.GroupKey,
	}, projection.metadata.GeneratedAt)
	filteredChanges := filterUpstreamIntelligenceChanges(scopedChanges, contracts.UpstreamIntelligenceChangesFilter{
		SourceID: filter.SourceID, ModelKey: filter.ModelKey, GroupKey: filter.GroupKey, Window: filter.Window,
	}, projection.metadata.GeneratedAt)
	filteredFrontier := filterUpstreamIntelligenceFrontier(projection.frontier, contracts.UpstreamIntelligenceFrontierFilter{
		SourceID: filter.SourceID, ModelKey: filter.ModelKey, GroupKey: filter.GroupKey,
		Provider: filter.Provider, Currency: filter.Currency,
	})
	response := contracts.UpstreamIntelligenceOverviewResponse{
		UpstreamIntelligenceReadMetadata: projection.metadata,
		Metrics:                          buildUpstreamIntelligenceOverviewMetrics(snapshot, filteredSources, filteredWallets, filteredRates, scopedChanges, projection.metadata.GeneratedAt),
		Wallets:                          firstUpstreamIntelligenceWallets(filteredWallets, upstreamIntelligenceOverviewListLimit),
		TopRates:                         firstUpstreamIntelligenceRates(filteredRates, upstreamIntelligenceOverviewListLimit),
		RecentChanges:                    firstUpstreamIntelligenceChanges(filteredChanges, upstreamIntelligenceOverviewListLimit),
		Frontier:                         firstUpstreamIntelligenceFrontier(filteredFrontier, upstreamIntelligenceOverviewListLimit),
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceSources(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "status", "provider", "currency", "accuracy", "coverage", "freshness", "limit", "cursor",
	), map[string]map[string]bool{
		"status": upstreamIntelligenceAllowedSourceStatuses, "accuracy": upstreamIntelligenceAllowedAccuracies,
		"coverage": upstreamIntelligenceAllowedCoverages, "freshness": upstreamIntelligenceAllowedFreshness,
	}, upstreamIntelligenceStringSet("limit"))
	if !ok {
		return
	}
	if !s.bindUpstreamIntelligenceCursorQuery(w, &query, "sources") {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	filter := contracts.UpstreamIntelligenceSourcesFilter{
		Status: contracts.UpstreamIntelligenceSourceStatus(query.values["status"]), Provider: query.values["provider"], Currency: query.values["currency"],
		Accuracy: contracts.UpstreamEvidenceAccuracy(query.values["accuracy"]), Coverage: contracts.UpstreamEvidenceCoverage(query.values["coverage"]),
		Freshness: contracts.UpstreamIntelligenceFreshness(query.values["freshness"]), Limit: intValue(query.values["limit"]), Cursor: query.values["cursor"],
	}.Normalize()
	items := filterUpstreamIntelligenceSources(projection.sources, filter, projection)
	offset, ok := s.upstreamIntelligenceCursorOffset(w, filter.Cursor, "sources", query.userID, projection.metadata.FactVersion, upstreamIntelligenceFilterFingerprint(map[string]string{
		"status": string(filter.Status), "provider": filter.Provider, "currency": filter.Currency, "accuracy": string(filter.Accuracy),
		"coverage": string(filter.Coverage), "freshness": string(filter.Freshness), "limit": strconv.Itoa(filter.Limit),
	}))
	if !ok {
		return
	}
	page, next := paginateUpstreamIntelligence(items, offset, filter.Limit)
	response := contracts.UpstreamIntelligenceSourcesResponse{UpstreamIntelligenceReadMetadata: projection.metadata, Items: page}
	if next >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("sources", query.userID, projection.metadata.FactVersion,
			upstreamIntelligenceFilterFingerprint(map[string]string{
				"status": string(filter.Status), "provider": filter.Provider, "currency": filter.Currency, "accuracy": string(filter.Accuracy),
				"coverage": string(filter.Coverage), "freshness": string(filter.Freshness), "limit": strconv.Itoa(filter.Limit),
			}), next, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.NextCursor = cursor
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceSourceDetail(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "rates_limit", "rates_cursor", "changes_limit", "changes_cursor",
	), nil, upstreamIntelligenceStringSet("rates_limit", "changes_limit"))
	if !ok {
		return
	}
	if !s.bindUpstreamIntelligenceSourceDetailCursorQuery(w, &query) {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("id"))
	if sourceID == "" || len(sourceID) > 256 {
		writeError(w, http.StatusBadRequest, "validation_failed", "upstream intelligence source id is invalid")
		return
	}
	var source contracts.UpstreamIntelligenceReadSourceSummary
	found := false
	for _, candidate := range projection.sources {
		if candidate.ID == sourceID {
			source, found = candidate, true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "upstream intelligence source not found")
		return
	}
	filter := contracts.UpstreamIntelligenceSourceDetailFilter{
		RatesLimit: intValue(query.values["rates_limit"]), RatesCursor: query.values["rates_cursor"],
		ChangesLimit: intValue(query.values["changes_limit"]), ChangesCursor: query.values["changes_cursor"],
	}.Normalize()
	rates := filterUpstreamIntelligenceRates(projection.rates, contracts.UpstreamIntelligenceRatesFilter{SourceID: sourceID})
	changes := filterUpstreamIntelligenceChanges(projection.changes, contracts.UpstreamIntelligenceChangesFilter{SourceID: sourceID}, projection.metadata.GeneratedAt)
	rateFingerprint := upstreamIntelligenceFilterFingerprint(map[string]string{"source_id": sourceID, "limit": strconv.Itoa(filter.RatesLimit)})
	changeFingerprint := upstreamIntelligenceFilterFingerprint(map[string]string{"source_id": sourceID, "limit": strconv.Itoa(filter.ChangesLimit)})
	rateOffset, ok := s.upstreamIntelligenceCursorOffset(w, filter.RatesCursor, "source_rates", query.userID, projection.metadata.FactVersion, rateFingerprint)
	if !ok {
		return
	}
	changeOffset, ok := s.upstreamIntelligenceCursorOffset(w, filter.ChangesCursor, "source_changes", query.userID, projection.metadata.FactVersion, changeFingerprint)
	if !ok {
		return
	}
	ratePage, rateNext := paginateUpstreamIntelligence(rates, rateOffset, filter.RatesLimit)
	changePage, changeNext := paginateUpstreamIntelligence(changes, changeOffset, filter.ChangesLimit)
	response := contracts.UpstreamIntelligenceSourceDetailResponse{
		UpstreamIntelligenceReadMetadata: projection.metadata, Source: source, CurrentRates: ratePage, RecentChanges: changePage,
	}
	for index := range projection.wallets {
		if projection.wallets[index].Source.ID == sourceID {
			wallet := projection.wallets[index]
			response.Wallet = &wallet
			break
		}
	}
	if rateNext >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("source_rates", query.userID, projection.metadata.FactVersion, rateFingerprint, rateNext, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.RatesNextCursor = cursor
	}
	if changeNext >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("source_changes", query.userID, projection.metadata.FactVersion, changeFingerprint, changeNext, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.ChangesNextCursor = cursor
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceRates(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "source_id", "model_key", "group_key", "provider", "currency", "price_dimension",
		"accuracy", "coverage", "freshness", "comparable", "limit", "cursor",
	), map[string]map[string]bool{
		"price_dimension": upstreamIntelligenceAllowedPriceDimensions, "accuracy": upstreamIntelligenceAllowedAccuracies,
		"coverage": upstreamIntelligenceAllowedCoverages, "freshness": upstreamIntelligenceAllowedFreshness,
		"comparable": upstreamIntelligenceStringSet("true", "false"),
	}, upstreamIntelligenceStringSet("limit"))
	if !ok {
		return
	}
	if !s.bindUpstreamIntelligenceCursorQuery(w, &query, "rates") {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	filter := contracts.UpstreamIntelligenceRatesFilter{
		SourceID: query.values["source_id"], ModelKey: query.values["model_key"], GroupKey: query.values["group_key"],
		Provider: query.values["provider"], Currency: query.values["currency"], PriceDimension: contracts.UpstreamPriceDimension(query.values["price_dimension"]),
		Accuracy: contracts.UpstreamEvidenceAccuracy(query.values["accuracy"]), Coverage: contracts.UpstreamEvidenceCoverage(query.values["coverage"]),
		Freshness: contracts.UpstreamIntelligenceFreshness(query.values["freshness"]), Limit: intValue(query.values["limit"]), Cursor: query.values["cursor"],
	}.Normalize()
	if raw, exists := query.values["comparable"]; exists {
		value := raw == "true"
		filter.Comparable = &value
	}
	items := filterUpstreamIntelligenceRates(projection.rates, filter)
	fingerprint := upstreamIntelligenceFilterFingerprint(map[string]string{
		"source_id": filter.SourceID, "model_key": filter.ModelKey, "group_key": filter.GroupKey, "provider": filter.Provider,
		"currency": filter.Currency, "price_dimension": string(filter.PriceDimension), "accuracy": string(filter.Accuracy),
		"coverage": string(filter.Coverage), "freshness": string(filter.Freshness), "comparable": optionalBoolString(filter.Comparable),
		"limit": strconv.Itoa(filter.Limit),
	})
	offset, ok := s.upstreamIntelligenceCursorOffset(w, filter.Cursor, "rates", query.userID, projection.metadata.FactVersion, fingerprint)
	if !ok {
		return
	}
	page, next := paginateUpstreamIntelligence(items, offset, filter.Limit)
	response := contracts.UpstreamIntelligenceRatesResponse{UpstreamIntelligenceReadMetadata: projection.metadata, Items: page}
	if next >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("rates", query.userID, projection.metadata.FactVersion, fingerprint, next, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.NextCursor = cursor
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceChanges(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "source_id", "model_key", "group_key", "event_type", "severity", "window", "limit", "cursor",
	), map[string]map[string]bool{
		"event_type": upstreamIntelligenceAllowedChangeTypes, "severity": upstreamIntelligenceAllowedSeverities,
		"window": upstreamIntelligenceStringSet(string(contracts.UpstreamIntelligenceWindow24h), string(contracts.UpstreamIntelligenceWindow7d)),
	}, upstreamIntelligenceStringSet("limit"))
	if !ok {
		return
	}
	if !s.bindUpstreamIntelligenceCursorQuery(w, &query, "changes") {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	filter := contracts.UpstreamIntelligenceChangesFilter{
		SourceID: query.values["source_id"], ModelKey: query.values["model_key"], GroupKey: query.values["group_key"],
		Type: contracts.UpstreamChangeEventType(query.values["event_type"]), Severity: contracts.UpstreamChangeSeverity(query.values["severity"]),
		Window: contracts.UpstreamIntelligenceReadWindow(query.values["window"]), Limit: intValue(query.values["limit"]), Cursor: query.values["cursor"],
	}.Normalize()
	items := filterUpstreamIntelligenceChanges(projection.changes, filter, projection.metadata.GeneratedAt)
	fingerprint := upstreamIntelligenceFilterFingerprint(map[string]string{
		"source_id": filter.SourceID, "model_key": filter.ModelKey, "group_key": filter.GroupKey, "event_type": string(filter.Type),
		"severity": string(filter.Severity), "window": string(filter.Window), "limit": strconv.Itoa(filter.Limit),
	})
	offset, ok := s.upstreamIntelligenceCursorOffset(w, filter.Cursor, "changes", query.userID, projection.metadata.FactVersion, fingerprint)
	if !ok {
		return
	}
	page, next := paginateUpstreamIntelligence(items, offset, filter.Limit)
	response := contracts.UpstreamIntelligenceChangesResponse{UpstreamIntelligenceReadMetadata: projection.metadata, Items: page}
	if next >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("changes", query.userID, projection.metadata.FactVersion, fingerprint, next, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.NextCursor = cursor
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceFrontier(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet(
		"user_id", "fact_version", "source_id", "model_key", "group_key", "provider", "currency", "price_dimension", "freshness", "limit", "cursor",
	), map[string]map[string]bool{
		"price_dimension": upstreamIntelligenceAllowedPriceDimensions, "freshness": upstreamIntelligenceAllowedFreshness,
	}, upstreamIntelligenceStringSet("limit"))
	if !ok {
		return
	}
	if !s.bindUpstreamIntelligenceCursorQuery(w, &query, "frontier") {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, query)
	if !ok {
		return
	}
	filter := contracts.UpstreamIntelligenceFrontierFilter{
		SourceID: query.values["source_id"], ModelKey: query.values["model_key"], GroupKey: query.values["group_key"],
		Provider: query.values["provider"], Currency: query.values["currency"], PriceDimension: contracts.UpstreamPriceDimension(query.values["price_dimension"]),
		Freshness: contracts.UpstreamIntelligenceFreshness(query.values["freshness"]), Limit: intValue(query.values["limit"]), Cursor: query.values["cursor"],
	}.Normalize()
	items := filterUpstreamIntelligenceFrontier(projection.frontier, filter)
	fingerprint := upstreamIntelligenceFilterFingerprint(map[string]string{
		"source_id": filter.SourceID, "model_key": filter.ModelKey, "group_key": filter.GroupKey, "provider": filter.Provider,
		"currency": filter.Currency, "price_dimension": string(filter.PriceDimension), "freshness": string(filter.Freshness), "limit": strconv.Itoa(filter.Limit),
	})
	offset, ok := s.upstreamIntelligenceCursorOffset(w, filter.Cursor, "frontier", query.userID, projection.metadata.FactVersion, fingerprint)
	if !ok {
		return
	}
	page, next := paginateUpstreamIntelligence(items, offset, filter.Limit)
	response := contracts.UpstreamIntelligenceFrontierResponse{UpstreamIntelligenceReadMetadata: projection.metadata, Items: page}
	if next >= 0 {
		cursor, cursorErr := s.encodeUpstreamIntelligenceCursor("frontier", query.userID, projection.metadata.FactVersion, fingerprint, next, projection.metadata.GeneratedAt)
		if cursorErr != nil {
			writeUpstreamIntelligenceCursorError(w, cursorErr)
			return
		}
		response.NextCursor = cursor
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) handleUpstreamIntelligenceEvidence(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	query, ok := parseUpstreamIntelligenceQuery(w, r, upstreamIntelligenceStringSet("user_id", "fact_version"), nil, nil)
	if !ok {
		return
	}
	reader, ok := s.store.(store.UpstreamIntelligenceReadStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_disabled", "upstream intelligence read model is not enabled")
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("id"))
	if evidenceID == "" || len(evidenceID) > 256 {
		writeError(w, http.StatusBadRequest, "validation_failed", "evidence id is invalid")
		return
	}
	snapshot, err := reader.ReadUpstreamIntelligenceEvidence(r.Context(), query.userID, evidenceID)
	if err != nil {
		writeUpstreamIntelligenceReadStoreError(w, err, "upstream intelligence evidence")
		return
	}
	if !upstreamIntelligenceFactVersionMatches(w, query.factVersion, snapshot.FactVersion.FactVersion) {
		return
	}
	metadata := contracts.UpstreamIntelligenceReadMetadata{FactVersion: snapshot.FactVersion.FactVersion, GeneratedAt: snapshot.GeneratedAt}
	sourceFreshness := sourceUpstreamIntelligenceFreshness(snapshot.Source, snapshot.Wallet, snapshot.Offer, snapshot.GeneratedAt)
	source := projectUpstreamIntelligenceSource(snapshot.Source, nil, sourceFreshness)
	response := contracts.UpstreamIntelligenceEvidenceResponse{
		UpstreamIntelligenceReadMetadata: metadata, ID: evidenceID, Source: source,
	}
	if snapshot.Run != nil {
		run := projectUpstreamIntelligenceRun(*snapshot.Run)
		response.Run = &run
	}
	switch {
	case snapshot.Wallet != nil:
		response.Kind = contracts.UpstreamIntelligenceEvidenceWallet
		wallet := projectUpstreamIntelligenceWallet(*snapshot.Wallet, source, snapshot.GeneratedAt)
		response.Wallet = &wallet
	case snapshot.Offer != nil:
		response.Kind = contracts.UpstreamIntelligenceEvidenceOffer
		offer := projectUpstreamIntelligenceRate(*snapshot.Offer, source, snapshot.GeneratedAt)
		response.Offer = &offer
	case snapshot.Change != nil:
		response.Kind = contracts.UpstreamIntelligenceEvidenceChange
		change := projectUpstreamIntelligenceChange(*snapshot.Change, source)
		response.Change = &change
	default:
		writeError(w, http.StatusInternalServerError, "store_error", "upstream intelligence evidence is incomplete")
		return
	}
	writeUpstreamIntelligenceReadJSON(w, response)
}

func (s *Server) bindUpstreamIntelligenceCursorQuery(w http.ResponseWriter, query *upstreamIntelligenceQuery, kind string) bool {
	raw := query.values["cursor"]
	if raw == "" {
		return true
	}
	if !s.upstreamIntelligenceCursorConfigured() {
		writeUpstreamIntelligenceCursorError(w, errUpstreamIntelligenceCursorUnavailable)
		return false
	}
	cursor, err := s.decodeUpstreamIntelligenceCursor(raw)
	if err != nil {
		writeUpstreamIntelligenceCursorError(w, err)
		return false
	}
	if cursor.Kind != kind || cursor.UserID != query.userID {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "upstream intelligence cursor does not match this request")
		return false
	}
	query.referenceTime = &cursor.ReferenceTime
	return true
}

func (s *Server) bindUpstreamIntelligenceSourceDetailCursorQuery(w http.ResponseWriter, query *upstreamIntelligenceQuery) bool {
	var referenceTime *time.Time
	for field, kind := range map[string]string{"rates_cursor": "source_rates", "changes_cursor": "source_changes"} {
		raw := query.values[field]
		if raw == "" {
			continue
		}
		if !s.upstreamIntelligenceCursorConfigured() {
			writeUpstreamIntelligenceCursorError(w, errUpstreamIntelligenceCursorUnavailable)
			return false
		}
		cursor, err := s.decodeUpstreamIntelligenceCursor(raw)
		if err != nil {
			writeUpstreamIntelligenceCursorError(w, err)
			return false
		}
		if cursor.Kind != kind || cursor.UserID != query.userID {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "upstream intelligence cursor does not match this request")
			return false
		}
		if referenceTime != nil && !cursor.ReferenceTime.Equal(*referenceTime) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "upstream intelligence cursors do not share one reference time")
			return false
		}
		value := cursor.ReferenceTime
		referenceTime = &value
	}
	query.referenceTime = referenceTime
	return true
}

func writeUpstreamIntelligenceCursorError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUpstreamIntelligenceCursorUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "cursor_unavailable", "upstream intelligence cursor signing is unavailable")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_cursor", "upstream intelligence cursor is invalid")
}

func parseUpstreamIntelligenceQuery(w http.ResponseWriter, r *http.Request, allowed map[string]bool, enums map[string]map[string]bool, positiveInts map[string]bool) (upstreamIntelligenceQuery, bool) {
	result := upstreamIntelligenceQuery{values: make(map[string]string)}
	parsedQuery, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "query string is invalid")
		return upstreamIntelligenceQuery{}, false
	}
	for key, values := range parsedQuery {
		if _, ok := allowed[key]; !ok {
			writeError(w, http.StatusBadRequest, "validation_failed", "unknown query parameter: "+key)
			return upstreamIntelligenceQuery{}, false
		}
		if len(values) != 1 {
			writeError(w, http.StatusBadRequest, "validation_failed", "query parameter must not be repeated: "+key)
			return upstreamIntelligenceQuery{}, false
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "query parameter must not be empty: "+key)
			return upstreamIntelligenceQuery{}, false
		}
		if len(value) > 1024 {
			writeError(w, http.StatusBadRequest, "validation_failed", "query parameter is too long: "+key)
			return upstreamIntelligenceQuery{}, false
		}
		result.values[key] = value
	}
	rawUserID, exists := result.values["user_id"]
	if !exists {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id is required")
		return upstreamIntelligenceQuery{}, false
	}
	userID, err := strconv.ParseInt(rawUserID, 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "user_id must be a positive integer")
		return upstreamIntelligenceQuery{}, false
	}
	result.userID = userID
	if raw, exists := result.values["fact_version"]; exists {
		version, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || version < 0 {
			writeError(w, http.StatusBadRequest, "validation_failed", "fact_version must be a non-negative integer")
			return upstreamIntelligenceQuery{}, false
		}
		result.factVersion = &version
	}
	for key, values := range enums {
		if value, exists := result.values[key]; exists {
			if _, ok := values[value]; !ok {
				writeError(w, http.StatusBadRequest, "validation_failed", "invalid "+key)
				return upstreamIntelligenceQuery{}, false
			}
		}
	}
	for key := range positiveInts {
		if raw, exists := result.values[key]; exists {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 || value > contracts.MaxUpstreamIntelligenceListLimit {
				writeError(w, http.StatusBadRequest, "validation_failed", key+" must be between 1 and "+strconv.Itoa(contracts.MaxUpstreamIntelligenceListLimit))
				return upstreamIntelligenceQuery{}, false
			}
		}
	}
	return result, true
}

func (s *Server) readUpstreamIntelligenceProjection(w http.ResponseWriter, r *http.Request, query upstreamIntelligenceQuery) (upstreamIntelligenceReadProjection, store.UpstreamIntelligenceCurrentSnapshot, bool) {
	reader, ok := s.store.(store.UpstreamIntelligenceReadStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "upstream_intelligence_disabled", "upstream intelligence read model is not enabled")
		return upstreamIntelligenceReadProjection{}, store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	snapshot, err := reader.ReadUpstreamIntelligenceCurrent(r.Context(), query.userID, query.referenceTime)
	if err != nil {
		writeUpstreamIntelligenceReadStoreError(w, err, "upstream intelligence snapshot")
		return upstreamIntelligenceReadProjection{}, store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	if snapshot.UserID != query.userID || snapshot.FactVersion.UserID != query.userID {
		writeError(w, http.StatusConflict, "read_model_conflict", "upstream intelligence owner snapshot is inconsistent")
		return upstreamIntelligenceReadProjection{}, store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	if !upstreamIntelligenceFactVersionMatches(w, query.factVersion, snapshot.FactVersion.FactVersion) {
		return upstreamIntelligenceReadProjection{}, store.UpstreamIntelligenceCurrentSnapshot{}, false
	}
	return projectUpstreamIntelligenceSnapshot(snapshot), snapshot, true
}

func upstreamIntelligenceFactVersionMatches(w http.ResponseWriter, expected *int64, current int64) bool {
	if expected != nil && *expected != current {
		writeError(w, http.StatusConflict, "stale_cursor", "upstream intelligence fact version changed; refresh and retry")
		return false
	}
	return true
}

func writeUpstreamIntelligenceReadStoreError(w http.ResponseWriter, err error, subject string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", subject+" not found")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "validation_failed", subject+" is invalid")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "read_model_conflict", subject+" exceeds a safe consistent-read bound")
	default:
		writeError(w, http.StatusInternalServerError, "store_error", subject+" could not be read")
	}
}

func writeUpstreamIntelligenceReadJSON(w http.ResponseWriter, value any) {
	setNoStore(w)
	writeJSON(w, http.StatusOK, value)
}

func projectUpstreamIntelligenceSnapshot(snapshot store.UpstreamIntelligenceCurrentSnapshot) upstreamIntelligenceReadProjection {
	metadata := contracts.UpstreamIntelligenceReadMetadata{FactVersion: snapshot.FactVersion.FactVersion, GeneratedAt: snapshot.GeneratedAt}
	runBySource := make(map[string]contracts.UpstreamCollectionRun, len(snapshot.LatestRuns))
	walletBySource := make(map[string]contracts.UpstreamWalletObservation, len(snapshot.Wallets))
	for _, run := range snapshot.LatestRuns {
		runBySource[run.SourceID] = run
	}
	for _, wallet := range snapshot.Wallets {
		walletBySource[wallet.SourceID] = wallet
	}
	offersBySource := make(map[string][]contracts.UpstreamOfferObservation)
	for _, offer := range snapshot.Offers {
		offersBySource[offer.SourceID] = append(offersBySource[offer.SourceID], offer)
	}
	sources := make([]contracts.UpstreamIntelligenceReadSourceSummary, 0, len(snapshot.Sources))
	sourceByID := make(map[string]contracts.UpstreamIntelligenceReadSourceSummary, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		wallet, hasWallet := walletBySource[source.ID]
		var walletPtr *contracts.UpstreamWalletObservation
		if hasWallet {
			walletPtr = &wallet
		}
		var offerPtr *contracts.UpstreamOfferObservation
		if values := offersBySource[source.ID]; len(values) > 0 {
			offer := values[0]
			for _, candidate := range values[1:] {
				if candidate.FreshUntil.Before(offer.FreshUntil) {
					offer = candidate
				}
			}
			offerPtr = &offer
		}
		freshness := sourceUpstreamIntelligenceFreshness(source, walletPtr, offerPtr, snapshot.GeneratedAt)
		var runPtr *contracts.UpstreamCollectionRun
		if run, exists := runBySource[source.ID]; exists {
			runCopy := run
			runPtr = &runCopy
		}
		projected := projectUpstreamIntelligenceSource(source, runPtr, freshness)
		sources = append(sources, projected)
		sourceByID[source.ID] = projected
	}
	wallets := make([]contracts.UpstreamIntelligenceWalletReadModel, 0, len(snapshot.Wallets))
	for _, wallet := range snapshot.Wallets {
		if source, exists := sourceByID[wallet.SourceID]; exists {
			wallets = append(wallets, projectUpstreamIntelligenceWallet(wallet, source, snapshot.GeneratedAt))
		}
	}
	rates := make([]contracts.UpstreamIntelligenceRateReadModel, 0, len(snapshot.Offers))
	for _, offer := range snapshot.Offers {
		if source, exists := sourceByID[offer.SourceID]; exists {
			rates = append(rates, projectUpstreamIntelligenceRate(offer, source, snapshot.GeneratedAt))
		}
	}
	applyUpstreamIntelligenceCohortComparability(rates)
	changes := make([]contracts.UpstreamIntelligenceChangeReadModel, 0, len(snapshot.Changes))
	for _, change := range snapshot.Changes {
		if source, exists := sourceByID[change.SourceID]; exists {
			changes = append(changes, projectUpstreamIntelligenceChange(change, source))
		}
	}
	frontier := projectUpstreamIntelligenceFrontier(snapshot, rates)
	normalizeUpstreamIntelligenceProjectionOrder(sources, wallets, rates, changes, frontier)
	return upstreamIntelligenceReadProjection{metadata: metadata, sources: sources, wallets: wallets, rates: rates, changes: changes, frontier: frontier}
}

func projectUpstreamIntelligenceFrontier(snapshot store.UpstreamIntelligenceCurrentSnapshot, rates []contracts.UpstreamIntelligenceRateReadModel) []contracts.UpstreamIntelligenceFrontierPoint {
	resolutions := make(map[string]store.UpstreamIntelligenceLinkResolution, len(snapshot.LinkResolutions))
	invalidResolutions := make(map[string]bool)
	for _, resolution := range snapshot.LinkResolutions {
		if resolution.LinkID == "" || resolution.UserID != snapshot.UserID {
			continue
		}
		if _, exists := resolutions[resolution.LinkID]; exists {
			delete(resolutions, resolution.LinkID)
			invalidResolutions[resolution.LinkID] = true
			continue
		}
		if !invalidResolutions[resolution.LinkID] {
			resolutions[resolution.LinkID] = resolution
		}
	}
	quality := make(map[string]contracts.ChannelHealthSnapshot, len(snapshot.QualitySnapshots))
	invalidQuality := make(map[string]bool)
	for _, candidate := range snapshot.QualitySnapshots {
		if candidate.Window != contracts.Window5m || strings.TrimSpace(candidate.ChannelID) == "" || strings.TrimSpace(candidate.Model) == "" ||
			candidate.ID == "" || candidate.CreatedAt.IsZero() || candidate.QualitySampleCount < 0 ||
			!finiteUpstreamIntelligenceQualitySnapshot(candidate) {
			continue
		}
		key := upstreamIntelligenceQualityKey(candidate.ChannelID, candidate.Model)
		if _, exists := quality[key]; exists {
			delete(quality, key)
			invalidQuality[key] = true
			continue
		}
		if !invalidQuality[key] {
			quality[key] = candidate
		}
	}
	linksByRate := make(map[string][]contracts.UpstreamIntelligenceLink)
	for _, link := range snapshot.Links {
		if link.UserID != snapshot.UserID || link.Status != contracts.UpstreamLinkActive {
			continue
		}
		key := upstreamIntelligenceLinkKey(link.IntelligenceSourceID, link.PriceDimension)
		linksByRate[key] = append(linksByRate[key], link)
	}
	candidates := make([]upstreamintelligence.FrontierCandidate, 0, len(rates))
	for _, rate := range rates {
		links := linksByRate[upstreamIntelligenceLinkKey(rate.Source.ID, rate.PriceDimension)]
		if len(links) == 0 {
			candidates = append(candidates, upstreamintelligence.FrontierCandidate{OwnerID: snapshot.UserID, Rate: rate})
			continue
		}
		for index := range links {
			link := links[index]
			candidate := upstreamintelligence.FrontierCandidate{OwnerID: snapshot.UserID, Rate: rate, Link: &link}
			if resolution, exists := resolutions[link.ID]; exists && !invalidResolutions[link.ID] {
				candidate.ResolvedChannelID = resolution.ResolvedChannelID
				candidate.ResolvedChannelOwnerID = resolution.ResolvedChannelOwnerID
				candidate.TargetVerified = resolution.TargetVerified
				if snapshotValue, found := quality[upstreamIntelligenceQualityKey(resolution.ResolvedChannelID, rate.ModelKey)]; found {
					candidate.Quality = projectUpstreamIntelligenceQualityCandidate(snapshot.UserID, snapshotValue)
				}
			}
			candidates = append(candidates, candidate)
		}
	}
	return upstreamintelligence.BuildFrontier(candidates, snapshot.GeneratedAt)
}

func upstreamIntelligenceLinkKey(sourceID string, dimension contracts.UpstreamPriceDimension) string {
	return sourceID + "\x00" + string(dimension)
}

func upstreamIntelligenceQualityKey(channelID, model string) string {
	return channelID + "\x00" + model
}

func projectUpstreamIntelligenceQualityCandidate(ownerID int64, snapshot contracts.ChannelHealthSnapshot) *upstreamintelligence.QualityCandidate {
	result := &upstreamintelligence.QualityCandidate{
		OwnerID: ownerID, ChannelID: snapshot.ChannelID, ModelKey: snapshot.Model, SnapshotID: snapshot.ID,
		Window: snapshot.Window, QualitySampleCount: snapshot.QualitySampleCount,
		MinimumSampleCount: upstreamIntelligenceQualityMinSamples, HealthState: snapshot.HealthState,
		ObservedAt: snapshot.CreatedAt, FreshUntil: snapshot.CreatedAt.Add(snapshot.Window.Duration()),
	}
	if snapshot.HealthState == contracts.HealthUnknown || snapshot.QualitySampleCount <= 0 {
		return result
	}
	result.QualityScore = upstreamIntelligenceQualityDecimal(snapshot.QualityScore, 0, 100, true)
	result.SuccessRate = upstreamIntelligenceQualityDecimal(snapshot.QualitySuccessRate, 0, 1, true)
	result.TTFTP95Milliseconds = upstreamIntelligenceQualityDecimal(snapshot.TTFTP95, 0, math.MaxFloat64, snapshot.TTFTP95 > 0)
	result.DurationP95Milliseconds = upstreamIntelligenceQualityDecimal(snapshot.DurationP95, 0, math.MaxFloat64, snapshot.DurationP95 > 0)
	if result.QualityScore == nil || result.SuccessRate == nil || snapshot.TTFTP95 < 0 || snapshot.DurationP95 < 0 {
		result.QualityScore = nil
	}
	return result
}

func finiteUpstreamIntelligenceQualitySnapshot(snapshot contracts.ChannelHealthSnapshot) bool {
	values := []float64{snapshot.QualityScore, snapshot.QualitySuccessRate, snapshot.TTFTP95, snapshot.DurationP95}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func upstreamIntelligenceQualityDecimal(value, minimum, maximum float64, present bool) *contracts.CanonicalDecimal {
	if !present || math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum {
		return nil
	}
	text := strconv.FormatFloat(value, 'f', -1, 64)
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil
	}
	decimal, err := contracts.QuantizeCanonicalDecimal(rational, contracts.UpstreamDecimalMaxScale)
	if err != nil {
		return nil
	}
	return &decimal
}

func projectUpstreamIntelligenceSource(source contracts.UpstreamIntelligenceSource, run *contracts.UpstreamCollectionRun, freshness *contracts.UpstreamIntelligenceFreshness) contracts.UpstreamIntelligenceReadSourceSummary {
	result := contracts.UpstreamIntelligenceReadSourceSummary{
		ID: source.ID, Mode: source.Mode, Provider: source.Provider, DisplayName: source.DisplayName, Currency: source.Currency,
		Status: source.Status, Capabilities: source.Capabilities, Freshness: freshness,
		LastRunAt: cloneTimePtr(source.LastRunAt), LastSuccessAt: cloneTimePtr(source.LastSuccessAt), NextPollAt: cloneTimePtr(source.NextPollAt),
		LastCoverage: source.LastCoverage, LastErrorCode: safeUpstreamIntelligenceErrorCode(source.LastErrorCode),
	}
	if run != nil {
		result.LastCoverage = run.Coverage
		result.LastErrorCode = safeUpstreamIntelligenceErrorCode(run.ErrorCode)
	}
	return result
}

func projectUpstreamIntelligenceRun(run contracts.UpstreamCollectionRun) contracts.UpstreamIntelligenceReadRunSummary {
	return contracts.UpstreamIntelligenceReadRunSummary{
		ID: run.ID, Trigger: run.Trigger, Status: run.Status, Coverage: run.Coverage,
		StartedAt: run.StartedAt, ObservedAt: run.ObservedAt, ReceivedAt: run.ReceivedAt, CompletedAt: cloneTimePtr(run.CompletedAt),
		FactCount: run.FactCount, PageCount: run.PageCount, ErrorCode: safeUpstreamIntelligenceErrorCode(run.ErrorCode), Retryable: run.Retryable,
	}
}

func projectUpstreamIntelligenceWallet(wallet contracts.UpstreamWalletObservation, source contracts.UpstreamIntelligenceReadSourceSummary, now time.Time) contracts.UpstreamIntelligenceWalletReadModel {
	return contracts.UpstreamIntelligenceWalletReadModel{
		ObservationID: wallet.ID, Source: source, BalanceAmount: cloneDecimalPtr(wallet.BalanceAmount), UnitKind: wallet.UnitKind, Currency: wallet.Currency,
		Evidence: projectUpstreamIntelligenceEvidence(wallet.Accuracy, wallet.Coverage, wallet.Confidence, wallet.ObservedAt, nil, wallet.ReceivedAt, wallet.FreshUntil, wallet.MissingFields, wallet.ReasonCode, now),
	}
}

func projectUpstreamIntelligenceRate(offer contracts.UpstreamOfferObservation, source contracts.UpstreamIntelligenceReadSourceSummary, now time.Time) contracts.UpstreamIntelligenceRateReadModel {
	evidence := projectUpstreamIntelligenceEvidence(offer.Accuracy, offer.Coverage, offer.Confidence, offer.ObservedAt, &offer.EffectiveAt,
		offer.ReceivedAt, offer.FreshUntil, offer.MissingFields, offer.ReasonCode, now)
	result := contracts.UpstreamIntelligenceRateReadModel{
		ObservationID: offer.ID, Source: source, GroupKey: offer.GroupKey, ModelKey: offer.ModelKey, PriceDimension: offer.PriceDimension,
		SettlementCurrency: offer.SettlementCurrency, PerTokens: offer.PerTokens,
		GroupMultiplier: cloneDecimalPtr(offer.GroupMultiplier), RechargeYield: cloneDecimalPtr(offer.RechargeYield), PublishedUnitPrice: cloneDecimalPtr(offer.PublishedUnitPrice),
		EffectiveMultiplier: cloneDecimalPtr(offer.EffectiveMultiplier), EffectiveUnitCost: cloneDecimalPtr(offer.EffectiveUnitCost), FormulaVersion: offer.FormulaVersion,
		Evidence: evidence,
	}
	result.UpstreamIntelligenceComparability = upstreamIntelligenceComparability(offer, evidence)
	return result
}

func projectUpstreamIntelligenceChange(change contracts.UpstreamChangeEvent, source contracts.UpstreamIntelligenceReadSourceSummary) contracts.UpstreamIntelligenceChangeReadModel {
	return contracts.UpstreamIntelligenceChangeReadModel{
		ID: change.ID, Source: source, Type: change.Type, BeforeObservationID: change.BeforeObservationID, AfterObservationID: change.AfterObservationID,
		AbsoluteChange: cloneDecimalPtr(change.AbsoluteChange), PercentageChange: cloneDecimalPtr(change.PercentageChange),
		FirstDetectedAt: change.FirstDetectedAt, ConfirmedAt: change.ConfirmedAt, Severity: change.Severity,
		ImpactScope: safeUpstreamIntelligenceImpactScope(change.ImpactScope), GroupKey: change.GroupKey, ModelKey: change.ModelKey, PriceDimension: change.PriceDimension,
	}
}

func projectUpstreamIntelligenceEvidence(accuracy contracts.UpstreamEvidenceAccuracy, coverage contracts.UpstreamEvidenceCoverage, confidence *contracts.CanonicalDecimal,
	observedAt time.Time, effectiveAt *time.Time, receivedAt, freshUntil time.Time, missing []string, reason string, now time.Time) contracts.UpstreamIntelligenceReadEvidence {
	safeMissing := safeUpstreamIntelligenceMissingFields(missing)
	safeReason := safeUpstreamIntelligenceReasonCode(reason)
	if (accuracy == contracts.UpstreamEvidenceUnknown || accuracy == contracts.UpstreamEvidenceUnattributed) && len(safeMissing) == 0 && safeReason == "" {
		safeReason = "evidence_incomplete"
	}
	return contracts.UpstreamIntelligenceReadEvidence{
		Accuracy: accuracy, Coverage: coverage, Freshness: upstreamIntelligenceFreshness(observedAt, freshUntil, now),
		Confidence: cloneDecimalPtr(confidence), ObservedAt: observedAt, EffectiveAt: cloneTimePtr(effectiveAt), ReceivedAt: receivedAt, FreshUntil: freshUntil,
		MissingFields: safeMissing, ReasonCode: safeReason,
	}
}

func upstreamIntelligenceFreshness(observedAt, freshUntil, now time.Time) contracts.UpstreamIntelligenceFreshness {
	if !now.After(freshUntil) {
		return contracts.UpstreamFreshnessCurrent
	}
	grace := freshUntil.Sub(observedAt)
	if grace < upstreamIntelligenceMinimumStaleGrace {
		grace = upstreamIntelligenceMinimumStaleGrace
	}
	if now.After(freshUntil.Add(grace)) {
		return contracts.UpstreamFreshnessExpired
	}
	return contracts.UpstreamFreshnessStale
}

func sourceUpstreamIntelligenceFreshness(source contracts.UpstreamIntelligenceSource, wallet *contracts.UpstreamWalletObservation, offer *contracts.UpstreamOfferObservation, now time.Time) *contracts.UpstreamIntelligenceFreshness {
	var result *contracts.UpstreamIntelligenceFreshness
	consider := func(value contracts.UpstreamIntelligenceFreshness) {
		if result == nil || upstreamIntelligenceFreshnessRank(value) > upstreamIntelligenceFreshnessRank(*result) {
			copyValue := value
			result = &copyValue
		}
	}
	if wallet != nil {
		consider(upstreamIntelligenceFreshness(wallet.ObservedAt, wallet.FreshUntil, now))
	}
	if offer != nil {
		consider(upstreamIntelligenceFreshness(offer.ObservedAt, offer.FreshUntil, now))
	}
	if result == nil && source.LastSuccessAt != nil {
		freshUntil := source.LastSuccessAt.Add(time.Duration(maxInt(source.PollIntervalSeconds, 60)) * time.Second)
		consider(upstreamIntelligenceFreshness(*source.LastSuccessAt, freshUntil, now))
	}
	return result
}

func upstreamIntelligenceComparability(offer contracts.UpstreamOfferObservation, evidence contracts.UpstreamIntelligenceReadEvidence) contracts.UpstreamIntelligenceComparability {
	reason := contracts.UpstreamIntelligenceComparabilityReason("")
	switch {
	case offer.SettlementCurrency == "":
		reason = contracts.UpstreamIntelligenceNotComparableMissingCurrency
	case offer.PerTokens <= 0 || offer.PriceDimension == "":
		reason = contracts.UpstreamIntelligenceNotComparableMissingUnit
	case offer.PublishedUnitPrice == nil || offer.EffectiveUnitCost == nil:
		reason = contracts.UpstreamIntelligenceNotComparableMissingPrice
	case offer.GroupMultiplier == nil || offer.EffectiveMultiplier == nil:
		reason = contracts.UpstreamIntelligenceNotComparableMissingMultiplier
	case offer.RechargeYield == nil:
		reason = contracts.UpstreamIntelligenceNotComparableMissingRechargeYield
	case !positiveUpstreamIntelligenceDecimal(offer.RechargeYield):
		reason = contracts.UpstreamIntelligenceNotComparableInvalidRechargeYield
	case offer.Accuracy == contracts.UpstreamEvidenceUnknown:
		reason = contracts.UpstreamIntelligenceNotComparableUnknownEvidence
	case offer.Accuracy == contracts.UpstreamEvidenceUnattributed:
		reason = contracts.UpstreamIntelligenceNotComparableUnattributedEvidence
	case offer.Coverage != contracts.UpstreamCoverageComplete:
		reason = contracts.UpstreamIntelligenceNotComparableIncompleteCoverage
	case evidence.Freshness == contracts.UpstreamFreshnessStale:
		reason = contracts.UpstreamIntelligenceNotComparableStaleEvidence
	case evidence.Freshness == contracts.UpstreamFreshnessExpired:
		reason = contracts.UpstreamIntelligenceNotComparableExpiredEvidence
	}
	return contracts.UpstreamIntelligenceComparability{Comparable: reason == "", ComparabilityReason: reason}
}

func applyUpstreamIntelligenceCohortComparability(items []contracts.UpstreamIntelligenceRateReadModel) {
	type cohortKey struct {
		modelKey       string
		groupKey       string
		priceDimension contracts.UpstreamPriceDimension
	}
	type cohortFacts struct {
		currencies map[string]struct{}
		units      map[int64]struct{}
	}
	cohorts := make(map[cohortKey]*cohortFacts)
	for index := range items {
		if !items[index].Comparable {
			continue
		}
		key := cohortKey{items[index].ModelKey, items[index].GroupKey, items[index].PriceDimension}
		facts := cohorts[key]
		if facts == nil {
			facts = &cohortFacts{currencies: make(map[string]struct{}), units: make(map[int64]struct{})}
			cohorts[key] = facts
		}
		facts.currencies[items[index].SettlementCurrency] = struct{}{}
		facts.units[items[index].PerTokens] = struct{}{}
	}

	// Determine blockers before mutating any row. Marking the first mismatch
	// inline used to remove that row from later comparisons and could miss a
	// third source in the same cohort.
	for left := range items {
		if !items[left].Comparable {
			continue
		}
		facts := cohorts[cohortKey{items[left].ModelKey, items[left].GroupKey, items[left].PriceDimension}]
		switch {
		case len(facts.currencies) > 1:
			items[left].Comparable, items[left].ComparabilityReason = false, contracts.UpstreamIntelligenceNotComparableCurrencyMismatch
		case len(facts.units) > 1:
			items[left].Comparable, items[left].ComparabilityReason = false, contracts.UpstreamIntelligenceNotComparableUnitMismatch
		}
	}
}

func positiveUpstreamIntelligenceDecimal(value *contracts.CanonicalDecimal) bool {
	if value == nil {
		return false
	}
	rat, err := value.Rat()
	return err == nil && rat.Sign() > 0
}

func normalizeUpstreamIntelligenceProjectionOrder(sources []contracts.UpstreamIntelligenceReadSourceSummary, wallets []contracts.UpstreamIntelligenceWalletReadModel,
	rates []contracts.UpstreamIntelligenceRateReadModel, changes []contracts.UpstreamIntelligenceChangeReadModel, frontier []contracts.UpstreamIntelligenceFrontierPoint) {
	sort.SliceStable(sources, func(i, j int) bool {
		left, right := upstreamIntelligenceSourceUnknownRank(sources[i]), upstreamIntelligenceSourceUnknownRank(sources[j])
		if left != right {
			return left < right
		}
		return sources[i].ID < sources[j].ID
	})
	sort.SliceStable(wallets, func(i, j int) bool {
		left, right := upstreamIntelligenceAccuracyRank(wallets[i].Evidence.Accuracy), upstreamIntelligenceAccuracyRank(wallets[j].Evidence.Accuracy)
		if left != right {
			return left < right
		}
		return wallets[i].Source.ID < wallets[j].Source.ID
	})
	sort.SliceStable(rates, func(i, j int) bool { return upstreamIntelligenceRateLess(rates[i], rates[j]) })
	sort.SliceStable(changes, func(i, j int) bool {
		if !changes[i].ConfirmedAt.Equal(changes[j].ConfirmedAt) {
			return changes[i].ConfirmedAt.After(changes[j].ConfirmedAt)
		}
		return changes[i].ID > changes[j].ID
	})
	sort.SliceStable(frontier, func(i, j int) bool { return upstreamIntelligenceRateLess(frontier[i].Rate, frontier[j].Rate) })
}

func upstreamIntelligenceRateLess(left, right contracts.UpstreamIntelligenceRateReadModel) bool {
	leftRank, rightRank := upstreamIntelligenceRateUnknownRank(left), upstreamIntelligenceRateUnknownRank(right)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if left.EffectiveUnitCost != nil && right.EffectiveUnitCost != nil {
		if comparison := compareUpstreamIntelligenceDecimal(*left.EffectiveUnitCost, *right.EffectiveUnitCost); comparison != 0 {
			return comparison < 0
		}
	}
	leftKey := strings.Join([]string{left.ModelKey, left.GroupKey, string(left.PriceDimension), left.Source.ID, left.ObservationID}, "\x00")
	rightKey := strings.Join([]string{right.ModelKey, right.GroupKey, string(right.PriceDimension), right.Source.ID, right.ObservationID}, "\x00")
	return leftKey < rightKey
}

func upstreamIntelligenceRateUnknownRank(value contracts.UpstreamIntelligenceRateReadModel) int {
	if value.Evidence.Accuracy == contracts.UpstreamEvidenceUnknown || value.Evidence.Accuracy == contracts.UpstreamEvidenceUnattributed || value.EffectiveUnitCost == nil {
		return 2
	}
	if !value.Comparable {
		return 1
	}
	return 0
}

func upstreamIntelligenceSourceUnknownRank(value contracts.UpstreamIntelligenceReadSourceSummary) int {
	if value.Freshness == nil {
		return 3
	}
	return upstreamIntelligenceFreshnessRank(*value.Freshness)
}

func upstreamIntelligenceFreshnessRank(value contracts.UpstreamIntelligenceFreshness) int {
	switch value {
	case contracts.UpstreamFreshnessCurrent:
		return 0
	case contracts.UpstreamFreshnessStale:
		return 1
	case contracts.UpstreamFreshnessExpired:
		return 2
	default:
		return 3
	}
}

func upstreamIntelligenceAccuracyRank(value contracts.UpstreamEvidenceAccuracy) int {
	switch value {
	case contracts.UpstreamEvidenceExact:
		return 0
	case contracts.UpstreamEvidenceDerived:
		return 1
	case contracts.UpstreamEvidenceEstimated:
		return 2
	case contracts.UpstreamEvidenceUnknown:
		return 3
	case contracts.UpstreamEvidenceUnattributed:
		return 4
	default:
		return 5
	}
}

func compareUpstreamIntelligenceDecimal(left, right contracts.CanonicalDecimal) int {
	leftRat, leftErr := left.Rat()
	rightRat, rightErr := right.Rat()
	if leftErr != nil || rightErr != nil {
		return strings.Compare(string(left), string(right))
	}
	return leftRat.Cmp(rightRat)
}

func filterUpstreamIntelligenceSources(items []contracts.UpstreamIntelligenceReadSourceSummary, filter contracts.UpstreamIntelligenceSourcesFilter, projection upstreamIntelligenceReadProjection) []contracts.UpstreamIntelligenceReadSourceSummary {
	sourceEvidence := make(map[string][]contracts.UpstreamIntelligenceReadEvidence)
	for _, wallet := range projection.wallets {
		sourceEvidence[wallet.Source.ID] = append(sourceEvidence[wallet.Source.ID], wallet.Evidence)
	}
	for _, rate := range projection.rates {
		sourceEvidence[rate.Source.ID] = append(sourceEvidence[rate.Source.ID], rate.Evidence)
	}
	out := make([]contracts.UpstreamIntelligenceReadSourceSummary, 0, len(items))
	for _, item := range items {
		if filter.Status != "" && item.Status != filter.Status || filter.Provider != "" && item.Provider != filter.Provider ||
			filter.Currency != "" && item.Currency != filter.Currency || filter.Freshness != "" && (item.Freshness == nil || *item.Freshness != filter.Freshness) {
			continue
		}
		if filter.Accuracy != "" || filter.Coverage != "" {
			matched := false
			for _, evidence := range sourceEvidence[item.ID] {
				if (filter.Accuracy == "" || evidence.Accuracy == filter.Accuracy) && (filter.Coverage == "" || evidence.Coverage == filter.Coverage) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func filterUpstreamIntelligenceSourcesByID(items []contracts.UpstreamIntelligenceReadSourceSummary, sourceID string) []contracts.UpstreamIntelligenceReadSourceSummary {
	out := make([]contracts.UpstreamIntelligenceReadSourceSummary, 0, 1)
	for _, item := range items {
		if item.ID == sourceID {
			out = append(out, item)
		}
	}
	return out
}

func filterUpstreamIntelligenceWallets(items []contracts.UpstreamIntelligenceWalletReadModel, filter contracts.UpstreamIntelligenceOverviewFilter) []contracts.UpstreamIntelligenceWalletReadModel {
	out := make([]contracts.UpstreamIntelligenceWalletReadModel, 0, len(items))
	for _, item := range items {
		if filter.SourceID != "" && item.Source.ID != filter.SourceID || filter.Provider != "" && item.Source.Provider != filter.Provider ||
			filter.Currency != "" && item.Currency != filter.Currency || filter.Accuracy != "" && item.Evidence.Accuracy != filter.Accuracy {
			continue
		}
		out = append(out, item)
	}
	return out
}

func filterUpstreamIntelligenceRates(items []contracts.UpstreamIntelligenceRateReadModel, filter contracts.UpstreamIntelligenceRatesFilter) []contracts.UpstreamIntelligenceRateReadModel {
	out := make([]contracts.UpstreamIntelligenceRateReadModel, 0, len(items))
	for _, item := range items {
		if filter.SourceID != "" && item.Source.ID != filter.SourceID || filter.ModelKey != "" && item.ModelKey != filter.ModelKey ||
			filter.GroupKey != "" && item.GroupKey != filter.GroupKey || filter.Provider != "" && item.Source.Provider != filter.Provider ||
			filter.Currency != "" && item.SettlementCurrency != filter.Currency || filter.PriceDimension != "" && item.PriceDimension != filter.PriceDimension ||
			filter.Accuracy != "" && item.Evidence.Accuracy != filter.Accuracy || filter.Coverage != "" && item.Evidence.Coverage != filter.Coverage ||
			filter.Freshness != "" && item.Evidence.Freshness != filter.Freshness || filter.Comparable != nil && item.Comparable != *filter.Comparable {
			continue
		}
		out = append(out, item)
	}
	return out
}

func filterUpstreamIntelligenceChanges(items []contracts.UpstreamIntelligenceChangeReadModel, filter contracts.UpstreamIntelligenceChangesFilter, now time.Time) []contracts.UpstreamIntelligenceChangeReadModel {
	cutoff := time.Time{}
	switch filter.Window {
	case contracts.UpstreamIntelligenceWindow24h:
		cutoff = now.Add(-24 * time.Hour)
	case contracts.UpstreamIntelligenceWindow7d:
		cutoff = now.Add(-7 * 24 * time.Hour)
	}
	out := make([]contracts.UpstreamIntelligenceChangeReadModel, 0, len(items))
	for _, item := range items {
		if filter.SourceID != "" && item.Source.ID != filter.SourceID || filter.ModelKey != "" && item.ModelKey != filter.ModelKey ||
			filter.GroupKey != "" && item.GroupKey != filter.GroupKey || filter.Type != "" && item.Type != filter.Type ||
			filter.Severity != "" && item.Severity != filter.Severity || !cutoff.IsZero() && item.ConfirmedAt.Before(cutoff) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func filterUpstreamIntelligenceFrontier(items []contracts.UpstreamIntelligenceFrontierPoint, filter contracts.UpstreamIntelligenceFrontierFilter) []contracts.UpstreamIntelligenceFrontierPoint {
	out := make([]contracts.UpstreamIntelligenceFrontierPoint, 0, len(items))
	for _, item := range items {
		rate := item.Rate
		if filter.SourceID != "" && rate.Source.ID != filter.SourceID || filter.ModelKey != "" && rate.ModelKey != filter.ModelKey ||
			filter.GroupKey != "" && rate.GroupKey != filter.GroupKey || filter.Provider != "" && rate.Source.Provider != filter.Provider ||
			filter.Currency != "" && rate.SettlementCurrency != filter.Currency || filter.PriceDimension != "" && rate.PriceDimension != filter.PriceDimension ||
			filter.Freshness != "" && rate.Evidence.Freshness != filter.Freshness {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildUpstreamIntelligenceOverviewMetrics(snapshot store.UpstreamIntelligenceCurrentSnapshot, sources []contracts.UpstreamIntelligenceReadSourceSummary,
	wallets []contracts.UpstreamIntelligenceWalletReadModel, rates []contracts.UpstreamIntelligenceRateReadModel,
	changes []contracts.UpstreamIntelligenceChangeReadModel, now time.Time) contracts.UpstreamIntelligenceOverviewMetrics {
	metrics := contracts.UpstreamIntelligenceOverviewMetrics{SourceCount: len(sources), CurrentRateCount: len(rates)}
	for _, source := range sources {
		if source.Status == contracts.UpstreamSourceActive {
			metrics.ActiveSourceCount++
		}
		if source.Freshness != nil && *source.Freshness == contracts.UpstreamFreshnessStale {
			metrics.StaleSourceCount++
		}
		if source.Freshness != nil && *source.Freshness == contracts.UpstreamFreshnessExpired {
			metrics.ExpiredSourceCount++
		}
		if source.LastErrorCode != "" {
			metrics.FailedSourceCount++
		}
		if source.NextPollAt != nil && (metrics.NextPollAt == nil || source.NextPollAt.Before(*metrics.NextPollAt)) {
			metrics.NextPollAt = cloneTimePtr(source.NextPollAt)
		}
	}
	freshComparable := 0
	for _, rate := range rates {
		if rate.Comparable {
			metrics.ComparableRateCount++
			if rate.Evidence.Freshness == contracts.UpstreamFreshnessCurrent {
				freshComparable++
			}
		}
	}
	if len(rates) > 0 {
		metrics.FreshComparableCoverage = upstreamIntelligenceRatioDecimal(freshComparable, len(rates))
	}
	for _, wallet := range wallets {
		if wallet.BalanceAmount == nil || wallet.Evidence.Accuracy == contracts.UpstreamEvidenceUnknown || wallet.Evidence.Accuracy == contracts.UpstreamEvidenceUnattributed ||
			wallet.Evidence.Coverage != contracts.UpstreamCoverageComplete || wallet.Evidence.Freshness != contracts.UpstreamFreshnessCurrent ||
			nonPositiveUpstreamIntelligenceDecimal(wallet.BalanceAmount) {
			metrics.BalanceRiskSourceCount++
		}
	}
	for _, change := range changes {
		if !change.ConfirmedAt.Before(now.Add(-24 * time.Hour)) {
			metrics.Changes24h++
		}
		if !change.ConfirmedAt.Before(now.Add(-7 * 24 * time.Hour)) {
			metrics.Changes7d++
		}
	}
	_ = snapshot // reserved for future snapshot-level aggregate evidence
	return metrics
}

func upstreamIntelligenceRatioDecimal(numerator, denominator int) *contracts.CanonicalDecimal {
	if denominator <= 0 {
		return nil
	}
	value, err := contracts.QuantizeCanonicalDecimal(new(big.Rat).SetFrac64(int64(numerator), int64(denominator)), contracts.UpstreamDecimalMaxScale)
	if err != nil {
		return nil
	}
	return &value
}

func nonPositiveUpstreamIntelligenceDecimal(value *contracts.CanonicalDecimal) bool {
	if value == nil {
		return true
	}
	rat, err := value.Rat()
	return err != nil || rat.Sign() <= 0
}

func (s *Server) upstreamIntelligenceCursorOffset(w http.ResponseWriter, raw, kind string, userID, factVersion int64, fingerprint string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	cursor, err := s.decodeUpstreamIntelligenceCursor(raw)
	if err != nil {
		writeUpstreamIntelligenceCursorError(w, err)
		return 0, false
	}
	if cursor.Kind != kind || cursor.UserID != userID || cursor.FilterFingerprint != fingerprint {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "upstream intelligence cursor does not match this request")
		return 0, false
	}
	if cursor.FactVersion != factVersion {
		writeError(w, http.StatusConflict, "stale_cursor", "upstream intelligence facts changed; restart pagination")
		return 0, false
	}
	return cursor.Offset, true
}

func (s *Server) encodeUpstreamIntelligenceCursor(kind string, userID, factVersion int64, fingerprint string, offset int, referenceTime time.Time) (string, error) {
	kid, key, ok := s.activeUpstreamIntelligenceCursorKey()
	if !ok {
		return "", errUpstreamIntelligenceCursorUnavailable
	}
	issuedAt := time.Now().UTC()
	cursor := upstreamIntelligenceCursor{
		Version: upstreamIntelligenceCursorVersion, KeyID: kid, Kind: kind, UserID: userID, FactVersion: factVersion,
		FilterFingerprint: fingerprint, Offset: offset, ReferenceTime: referenceTime.UTC(),
		IssuedAt: issuedAt.Unix(), ExpiresAt: issuedAt.Add(upstreamIntelligenceCursorTTL).Unix(),
	}
	cursor.Signature = upstreamIntelligenceCursorSignature(cursor, key)
	payload, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (s *Server) decodeUpstreamIntelligenceCursor(raw string) (upstreamIntelligenceCursor, error) {
	if len(raw) > 2048 {
		return upstreamIntelligenceCursor{}, errors.New("cursor too long")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return upstreamIntelligenceCursor{}, err
	}
	var cursor upstreamIntelligenceCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return upstreamIntelligenceCursor{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return upstreamIntelligenceCursor{}, errors.New("cursor must contain exactly one JSON value")
	}
	if cursor.Version != upstreamIntelligenceCursorVersion || !validUpstreamIntelligenceCursorKeyID(cursor.KeyID) || cursor.Kind == "" ||
		cursor.UserID <= 0 || cursor.FactVersion < 0 || cursor.FilterFingerprint == "" || cursor.Offset <= 0 || cursor.ReferenceTime.IsZero() ||
		cursor.IssuedAt <= 0 || cursor.ExpiresAt <= cursor.IssuedAt || cursor.ExpiresAt-cursor.IssuedAt != int64(upstreamIntelligenceCursorTTL/time.Second) ||
		cursor.Signature == "" {
		return upstreamIntelligenceCursor{}, errors.New("invalid cursor fields")
	}
	key, configured, exists := s.upstreamIntelligenceCursorKey(cursor.KeyID)
	if !configured {
		return upstreamIntelligenceCursor{}, errUpstreamIntelligenceCursorUnavailable
	}
	if !exists {
		return upstreamIntelligenceCursor{}, errors.New("cursor key id is unknown")
	}
	want := upstreamIntelligenceCursorSignature(cursor, key)
	if !hmac.Equal([]byte(want), []byte(cursor.Signature)) {
		return upstreamIntelligenceCursor{}, errors.New("cursor signature mismatch")
	}
	now := time.Now().UTC().Unix()
	if cursor.IssuedAt > now+60 || cursor.ExpiresAt <= now {
		return upstreamIntelligenceCursor{}, errors.New("cursor is outside its validity period")
	}
	return cursor, nil
}

func upstreamIntelligenceCursorSignature(cursor upstreamIntelligenceCursor, key []byte) string {
	payload := fmt.Sprintf("e2m.upstream-intelligence.cursor.v2\x00%d\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%s\x00%d\x00%d",
		cursor.Version, cursor.KeyID, cursor.Kind, cursor.UserID, cursor.FactVersion, cursor.FilterFingerprint, cursor.Offset,
		cursor.ReferenceTime.UTC().Format(time.RFC3339Nano), cursor.IssuedAt, cursor.ExpiresAt)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) activeUpstreamIntelligenceCursorKey() (string, []byte, bool) {
	s.intelligenceCursorMu.RLock()
	defer s.intelligenceCursorMu.RUnlock()
	key, ok := s.intelligenceCursorKeys[s.intelligenceCursorActiveKID]
	if !ok {
		return "", nil, false
	}
	return s.intelligenceCursorActiveKID, key[:], true
}

func (s *Server) upstreamIntelligenceCursorKey(kid string) ([]byte, bool, bool) {
	s.intelligenceCursorMu.RLock()
	defer s.intelligenceCursorMu.RUnlock()
	if len(s.intelligenceCursorKeys) == 0 {
		return nil, false, false
	}
	key, ok := s.intelligenceCursorKeys[kid]
	return key[:], true, ok
}

func (s *Server) upstreamIntelligenceCursorConfigured() bool {
	s.intelligenceCursorMu.RLock()
	defer s.intelligenceCursorMu.RUnlock()
	return len(s.intelligenceCursorKeys) > 0
}

func upstreamIntelligenceFilterFingerprint(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("e2m.upstream-intelligence.filter.v1\x00"))
	for _, key := range keys {
		_, _ = hasher.Write([]byte(key))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(values[key]))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func paginateUpstreamIntelligence[T any](items []T, offset, limit int) ([]T, int) {
	if offset < 0 || offset > len(items) {
		return []T{}, -1
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := append([]T{}, items[offset:end]...)
	if end < len(items) {
		return page, end
	}
	return page, -1
}

func firstUpstreamIntelligenceWallets(items []contracts.UpstreamIntelligenceWalletReadModel, limit int) []contracts.UpstreamIntelligenceWalletReadModel {
	page, _ := paginateUpstreamIntelligence(items, 0, minInt(limit, len(items)))
	return page
}

func firstUpstreamIntelligenceRates(items []contracts.UpstreamIntelligenceRateReadModel, limit int) []contracts.UpstreamIntelligenceRateReadModel {
	page, _ := paginateUpstreamIntelligence(items, 0, minInt(limit, len(items)))
	return page
}

func firstUpstreamIntelligenceChanges(items []contracts.UpstreamIntelligenceChangeReadModel, limit int) []contracts.UpstreamIntelligenceChangeReadModel {
	page, _ := paginateUpstreamIntelligence(items, 0, minInt(limit, len(items)))
	return page
}

func firstUpstreamIntelligenceFrontier(items []contracts.UpstreamIntelligenceFrontierPoint, limit int) []contracts.UpstreamIntelligenceFrontierPoint {
	page, _ := paginateUpstreamIntelligence(items, 0, minInt(limit, len(items)))
	return page
}

func safeUpstreamIntelligenceImpactScope(input map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range input {
		if _, ok := upstreamIntelligenceAllowedImpactScopeKeys[key]; !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || containsSensitiveUpstreamIntelligenceValue(value) {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func safeUpstreamIntelligenceMissingFields(input []string) []string {
	out := make([]string, 0, len(input))
	seen := make(map[string]struct{})
	for _, value := range input {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 || containsSensitiveUpstreamIntelligenceValue(value) {
			continue
		}
		valid := true
		for _, char := range value {
			if char != '_' && char != '-' && char != '.' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func safeUpstreamIntelligenceReasonCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || containsSensitiveUpstreamIntelligenceValue(value) {
		return ""
	}
	for _, char := range value {
		if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return ""
		}
	}
	return value
}

func safeUpstreamIntelligenceErrorCode(value string) string {
	if contracts.IsUpstreamCollectionErrorCode(value) {
		return value
	}
	return ""
}

func appendUniqueUpstreamIntelligenceReason(values []contracts.UpstreamIntelligenceComparabilityReason, value contracts.UpstreamIntelligenceComparabilityReason) []contracts.UpstreamIntelligenceComparabilityReason {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneDecimalPtr(value *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func optionalBoolString(value *bool) string {
	if value == nil {
		return ""
	}
	return strconv.FormatBool(*value)
}

func intValue(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
