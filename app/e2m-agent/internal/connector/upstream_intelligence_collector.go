package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"e2m.local/agent/internal/adapters/sub2api"
	"e2m.local/contracts"
)

const (
	UpstreamIntelligenceCollectErrorSourceInactive     = "source_inactive"
	UpstreamIntelligenceCollectErrorCredentialMissing  = "credential_missing"
	UpstreamIntelligenceCollectErrorGatewayUnavailable = "gateway_unavailable"
	UpstreamIntelligenceCollectErrorRunIdentity        = "run_identity_failed"
	UpstreamIntelligenceCollectErrorSnapshotInvalid    = "snapshot_invalid"
)

type UpstreamIntelligenceCollectClock func() time.Time
type UpstreamIntelligenceRunIDFunc func() (string, error)

// UpstreamIntelligenceCollectorConfig supplies only process-local collection
// dependencies. ManagedGatewayURL resolves the URL for an owned source; its
// administrator credentials are deliberately not accepted here.
type UpstreamIntelligenceCollectorConfig struct {
	HTTPClient        *http.Client
	ManagedGatewayURL func() (string, error)
	Clock             UpstreamIntelligenceCollectClock
	NewRunID          UpstreamIntelligenceRunIDFunc
}

type UpstreamIntelligenceCollector struct {
	httpClient        *http.Client
	managedGatewayURL func() (string, error)
	clock             UpstreamIntelligenceCollectClock
	newRunID          UpstreamIntelligenceRunIDFunc
}

// UpstreamIntelligenceCollectionSummary is safe to log or return from a local
// orchestration layer. It contains neither the source URL nor credentials or
// upstream response data.
type UpstreamIntelligenceCollectionSummary struct {
	RunID      string                             `json:"run_id,omitempty"`
	Status     contracts.UpstreamCollectionStatus `json:"status,omitempty"`
	Coverage   contracts.UpstreamEvidenceCoverage `json:"coverage,omitempty"`
	FactCount  int                                `json:"fact_count"`
	BatchCount int                                `json:"batch_count"`
	ObservedAt time.Time                          `json:"observed_at,omitempty"`
	ErrorCode  string                             `json:"error_code,omitempty"`
	Retryable  bool                               `json:"retryable"`
}

// UpstreamIntelligenceCollectionError exposes only a stable code and retry
// hint. The underlying URL, token, HTTP body, and raw transport error are not
// retained, wrapped, or returned.
type UpstreamIntelligenceCollectionError struct {
	Code      string
	Retryable bool
}

func (e *UpstreamIntelligenceCollectionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func NewUpstreamIntelligenceCollector(config UpstreamIntelligenceCollectorConfig) *UpstreamIntelligenceCollector {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	newRunID := config.NewRunID
	if newRunID == nil {
		newRunID = newUpstreamIntelligenceCollectionRunID
	}
	return &UpstreamIntelligenceCollector{
		httpClient: config.HTTPClient, managedGatewayURL: config.ManagedGatewayURL,
		clock: clock, newRunID: newRunID,
	}
}

// Collect performs one source-scoped read and returns fully packaged Core
// ingest batches. It has no local API, scheduler, persistence, or outbox side
// effects. A failed upstream snapshot is still a successful collection result
// represented by a zero-fact failed run; only local preflight/packaging errors
// are returned as errors.
func (collector *UpstreamIntelligenceCollector) Collect(ctx context.Context, source UpstreamIntelligenceSource, trigger contracts.UpstreamCollectionTrigger) ([]contracts.UpstreamIntelligenceIngestBatchRequest, UpstreamIntelligenceCollectionSummary, error) {
	if collector == nil {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorSnapshotInvalid, false)
	}
	if source.Status != UpstreamIntelligenceSourceActive || source.TombstonedAt != nil {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorSourceInactive, false)
	}
	userBearer := strings.TrimSpace(source.Credentials.UserBearerToken)
	if userBearer == "" {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorCredentialMissing, false)
	}
	if strings.IndexFunc(userBearer, func(character rune) bool { return unicode.IsControl(character) || unicode.IsSpace(character) }) >= 0 {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorCredentialMissing, false)
	}
	if err := validateUpstreamIntelligenceSource(source); err != nil {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorSnapshotInvalid, false)
	}

	baseURL, err := collector.sourceAPIBaseURL(source)
	if err != nil {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorGatewayUnavailable, false)
	}
	client, err := sub2api.NewIntelligenceClient(sub2api.IntelligenceClientConfig{
		BaseURL: baseURL, HTTPClient: collector.httpClient,
		Authorize: func(request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+userBearer)
			return nil
		},
	})
	if err != nil {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorGatewayUnavailable, false)
	}

	runID, err := collector.newRunID()
	if err != nil || !validUpstreamIntelligenceEnvelopeIdentifier(strings.TrimSpace(runID), 128) {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorRunIdentity, true)
	}
	startedAt := canonicalUpstreamIntelligenceTime(collector.clock())
	snapshot := client.Collect(ctx)
	observedAt := canonicalUpstreamIntelligenceTime(collector.clock())
	if observedAt.Before(startedAt) {
		observedAt = startedAt
	}
	completedAt := observedAt
	freshUntil := observedAt.Add(time.Duration(source.PollIntervalSeconds) * time.Second)
	batches, err := BuildUpstreamIntelligenceSnapshotBatches(UpstreamIntelligenceSnapshotEnvelope{
		Source: source.Public(), RunID: strings.TrimSpace(runID), Trigger: trigger,
		StartedAt: startedAt, ObservedAt: observedAt, CompletedAt: completedAt,
		FreshUntil: freshUntil, Currency: source.Currency, Snapshot: snapshot,
	})
	if err != nil || len(batches) == 0 {
		return upstreamIntelligenceCollectionFailure(UpstreamIntelligenceCollectErrorSnapshotInvalid, false)
	}
	run := batches[0].Run
	summary := UpstreamIntelligenceCollectionSummary{
		RunID: run.ID, Status: run.Status, Coverage: run.Coverage,
		FactCount: run.FactCount, BatchCount: run.BatchCount, ObservedAt: run.ObservedAt,
		ErrorCode: run.ErrorCode, Retryable: run.Retryable,
	}
	return batches, summary, nil
}

func (collector *UpstreamIntelligenceCollector) sourceAPIBaseURL(source UpstreamIntelligenceSource) (string, error) {
	var raw string
	switch source.Mode {
	case UpstreamIntelligenceSourceOwned:
		if collector.managedGatewayURL == nil {
			return "", errors.New("managed gateway URL is unavailable")
		}
		resolved, err := collector.managedGatewayURL()
		if err != nil {
			return "", errors.New("managed gateway URL is unavailable")
		}
		raw = resolved
	case UpstreamIntelligenceSourceExternal:
		raw = source.GatewayURL
	default:
		return "", errors.New("unsupported source mode")
	}
	return normalizeUpstreamIntelligenceAPIBaseURL(raw)
}

func normalizeUpstreamIntelligenceAPIBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("source gateway URL is invalid")
	}
	path := strings.TrimRight(parsed.Path, "/")
	for strings.HasSuffix(path, "/api/v1") {
		path = strings.TrimRight(strings.TrimSuffix(path, "/api/v1"), "/")
	}
	parsed.RawPath = ""
	parsed.Path = path + "/api/v1"
	return strings.TrimRight(parsed.String(), "/"), nil
}

func upstreamIntelligenceCollectionFailure(code string, retryable bool) ([]contracts.UpstreamIntelligenceIngestBatchRequest, UpstreamIntelligenceCollectionSummary, error) {
	summary := UpstreamIntelligenceCollectionSummary{ErrorCode: code, Retryable: retryable}
	return nil, summary, &UpstreamIntelligenceCollectionError{Code: code, Retryable: retryable}
}

func newUpstreamIntelligenceCollectionRunID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "uirun_" + hex.EncodeToString(buffer), nil
}
