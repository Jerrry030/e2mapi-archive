package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const (
	passiveObservationStateVersion = 1
	passiveObservationBatchSize    = 500
	passiveObservationInterval     = 15 * time.Second
	passiveObservationMaxAge       = 24 * time.Hour
	passiveObservationFutureSkew   = 2 * time.Minute
	passiveObservationConflictCap  = 1000
)

type passiveObservationState struct {
	Version       int                                     `json:"version"`
	Scope         string                                  `json:"scope"`
	Cursor        string                                  `json:"cursor,omitempty"`
	PendingCursor string                                  `json:"pending_cursor,omitempty"`
	Pending       []contracts.ConnectorChannelObservation `json:"pending,omitempty"`
	Conflicts     []passiveObservationConflict            `json:"conflicts,omitempty"`
}

// passiveObservationConflict is a durable dead-letter marker. The full
// observation remains in Core's append-only history under the conflicting id;
// Connector retains only the local id and scope needed for diagnosis.
type passiveObservationConflict struct {
	Scope         string    `json:"scope"`
	ObservationID string    `json:"observation_id"`
	RejectedAt    time.Time `json:"rejected_at"`
}

func (c *Connector) collectPassiveObservationsIfDue(ctx context.Context) error {
	now := time.Now()
	if now.Before(c.nextPassiveObservationAt) {
		return nil
	}
	c.nextPassiveObservationAt = now.Add(passiveObservationInterval)
	if c.cfg.ConfigStore == nil {
		return nil
	}
	cfg, err := c.cfg.ConfigStore.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("connector: load passive observation config: %w", err)
	}
	cfg.Normalize()
	if err := ValidateGatewayLocalConfig(cfg); err != nil || !observationCapabilitiesForGatewayConfig(cfg).PassiveCollection {
		return nil
	}
	return c.collectPassiveObservations(ctx)
}

// collectPassiveObservations runs one durable collect/upload transaction. A
// page and its next cursor are persisted together before upload. Only a full
// Core acknowledgement commits the cursor, so a crash or lost HTTP response
// replays the same stable observation ids instead of losing or double-counting
// request facts.
func (c *Connector) collectPassiveObservations(ctx context.Context) error {
	gateway, err := c.gateway()
	if err != nil {
		return err
	}
	reader, ok := gateway.(gateways.PassiveObservationReader)
	if !ok || !reader.ObservationCapabilities().PassiveCollection {
		return nil
	}
	scope, err := c.passiveObservationScope()
	if err != nil {
		return err
	}
	state, err := c.loadPassiveObservationState()
	if err != nil {
		return err
	}
	if state.Scope != "" && state.Scope != scope {
		state = passiveObservationState{
			Version: passiveObservationStateVersion,
			Scope:   scope,
			// Conflict history is diagnostic state, not a cursor. Retain it when
			// the configured gateway changes so a permanent rejection is visible.
			Conflicts: state.Conflicts,
		}
	}
	if state.Scope == "" {
		state.Scope = scope
	}

	if len(state.Pending) > 0 {
		conflicts, err := c.reportPassiveObservationBatch(ctx, scope, state.Pending)
		if err != nil {
			return err
		}
		state.Conflicts = mergePassiveObservationConflicts(state.Conflicts, conflicts)
		state.Cursor = state.PendingCursor
		state.PendingCursor = ""
		state.Pending = nil
		if err := c.savePassiveObservationState(state); err != nil {
			return fmt.Errorf("connector: commit passive observation cursor: %w", err)
		}
	}

	page, err := reader.ReadPassiveObservations(ctx, state.Cursor, passiveObservationBatchSize)
	if err != nil {
		return fmt.Errorf("connector: collect passive observations: %w", err)
	}
	page.NextCursor = strings.TrimSpace(page.NextCursor)
	if page.NextCursor == "" {
		return errors.New("connector: passive observation reader returned an empty cursor")
	}
	if page.NextCursor == state.Cursor && len(page.Observations) > 0 {
		return errors.New("connector: passive observation reader did not advance its cursor")
	}

	coreNow := time.Now().Add(c.coreClockOffset).UTC()
	pending := make([]contracts.ConnectorChannelObservation, 0, len(page.Observations))
	for _, observation := range page.Observations {
		observation.ObservationID = passiveScopedObservationID(scope, observation.ObservationID)
		observation.Source = contracts.ObservationPassive
		observation.ObservedAt = observation.ObservedAt.UTC().Truncate(time.Microsecond)
		if observation.ObservedAt.Before(coreNow.Add(-passiveObservationMaxAge)) ||
			observation.ObservedAt.After(coreNow.Add(passiveObservationFutureSkew)) {
			continue
		}
		if err := validatePassiveObservation(observation); err != nil {
			return fmt.Errorf("connector: passive observation %q is invalid: %w", observation.ObservationID, err)
		}
		pending = append(pending, observation)
	}

	if len(pending) == 0 {
		if page.NextCursor == state.Cursor {
			return nil
		}
		state.Cursor = page.NextCursor
		return c.savePassiveObservationState(state)
	}
	state.PendingCursor = page.NextCursor
	state.Pending = pending
	if err := c.savePassiveObservationState(state); err != nil {
		return fmt.Errorf("connector: persist passive observation outbox: %w", err)
	}
	conflicts, err := c.reportPassiveObservationBatch(ctx, scope, pending)
	if err != nil {
		return err
	}
	state.Conflicts = mergePassiveObservationConflicts(state.Conflicts, conflicts)
	state.Cursor = state.PendingCursor
	state.PendingCursor = ""
	state.Pending = nil
	if err := c.savePassiveObservationState(state); err != nil {
		return fmt.Errorf("connector: commit passive observation cursor: %w", err)
	}
	return nil
}

// reportPassiveObservationBatch isolates only permanent identity conflicts.
// Core may have committed a prefix before returning 409, so every retry is
// intentionally idempotent. Recursive bisection identifies the bad row while
// allowing all other facts in the durable page to be acknowledged.
func (c *Connector) reportPassiveObservationBatch(
	ctx context.Context,
	scope string,
	observations []contracts.ConnectorChannelObservation,
) ([]passiveObservationConflict, error) {
	if err := c.reportObservationBatch(ctx, observations); err == nil {
		return nil, nil
	} else if !isObservationIDConflict(err) {
		return nil, err
	}
	if len(observations) == 1 {
		return []passiveObservationConflict{{
			Scope:         scope,
			ObservationID: observations[0].ObservationID,
			RejectedAt:    time.Now().UTC(),
		}}, nil
	}

	middle := len(observations) / 2
	left, err := c.reportPassiveObservationBatch(ctx, scope, observations[:middle])
	if err != nil {
		return nil, err
	}
	right, err := c.reportPassiveObservationBatch(ctx, scope, observations[middle:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func isObservationIDConflict(err error) bool {
	var responseError *coreHTTPError
	return errors.As(err, &responseError) && responseError.StatusCode == http.StatusConflict &&
		responseError.Code == "observation_id_conflict"
}

func mergePassiveObservationConflicts(existing, additions []passiveObservationConflict) []passiveObservationConflict {
	if len(additions) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(additions))
	out := make([]passiveObservationConflict, 0, min(passiveObservationConflictCap, len(existing)+len(additions)))
	for _, conflict := range append(existing, additions...) {
		key := conflict.Scope + "\x00" + conflict.ObservationID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, conflict)
	}
	if len(out) > passiveObservationConflictCap {
		out = append([]passiveObservationConflict(nil), out[len(out)-passiveObservationConflictCap:]...)
	}
	return out
}

func (c *Connector) reportObservationBatch(ctx context.Context, observations []contracts.ConnectorChannelObservation) error {
	if len(observations) == 0 || len(observations) > passiveObservationBatchSize {
		return fmt.Errorf("connector: observation batch size %d is invalid", len(observations))
	}
	req := contracts.ConnectorObservationBatchRequest{
		ConnectorID:  c.cfg.ConnectorID,
		Observations: observations,
	}
	var response contracts.ConnectorObservationBatchResponse
	if err := c.postJSON(ctx, "/api/v1/connectors/observations", c.connectorToken(), req, &response); err != nil {
		return fmt.Errorf("connector: report observations: %w", err)
	}
	if response.Accepted != len(observations) {
		return fmt.Errorf("connector: report observations: core accepted %d of %d observations", response.Accepted, len(observations))
	}
	return nil
}

func validatePassiveObservation(observation contracts.ConnectorChannelObservation) error {
	if !validPassiveObservationID(observation.ObservationID) {
		return errors.New("observation_id is invalid")
	}
	if !validPassiveField(observation.RemoteID) {
		return errors.New("remote_id is invalid")
	}
	if !validPassiveField(observation.Model) {
		return errors.New("model is invalid")
	}
	if observation.StatusCode < 0 || observation.StatusCode > 599 {
		return errors.New("status_code must be between 0 and 599")
	}
	if observation.FirstTokenMS < 0 || observation.TotalMS < 0 ||
		math.IsNaN(observation.FirstTokenMS) || math.IsNaN(observation.TotalMS) ||
		math.IsInf(observation.FirstTokenMS, 0) || math.IsInf(observation.TotalMS, 0) {
		return errors.New("latency values must be finite and non-negative")
	}
	if observation.FirstTokenMS > 0 && observation.TotalMS > 0 && observation.FirstTokenMS > observation.TotalMS {
		return errors.New("first_token_ms cannot exceed total_ms")
	}
	if observation.InputTokens < 0 || observation.OutputTokens < 0 {
		return errors.New("token counts must be non-negative")
	}
	if observation.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	if observation.Success && observation.ErrorType != contracts.ErrorNone {
		return errors.New("successful observation cannot include error_type")
	}
	if !observation.Success && observation.ErrorType == contracts.ErrorNone {
		return errors.New("failed observation requires error_type")
	}
	if !validQualityProbeErrorType(observation.ErrorType) {
		return errors.New("error_type is not supported")
	}
	return nil
}

func validPassiveField(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > contracts.MaxConnectorIdentifierBytes || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPassiveObservationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '.' || ch == '_' || ch == ':' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func passiveScopedObservationID(scope, sourceID string) string {
	return "passive:v1:" + scope[:16] + ":" + strings.TrimSpace(sourceID)
}

func (c *Connector) passiveObservationScope() (string, error) {
	if c.cfg.ConfigStore == nil {
		return "", errors.New("connector local store is not configured")
	}
	cfg, err := c.cfg.ConfigStore.Load()
	if err != nil {
		return "", err
	}
	cfg.Normalize()
	sum := sha256.Sum256([]byte(cfg.GatewayKind + "\x00" + cfg.GatewayURL))
	return hex.EncodeToString(sum[:]), nil
}

func (c *Connector) passiveObservationStatePath() (string, error) {
	if c.cfg.ConfigStore == nil || strings.TrimSpace(c.cfg.ConfigStore.path) == "" {
		return "", errors.New("connector local store is not configured")
	}
	return filepath.Join(filepath.Dir(c.cfg.ConfigStore.path), "passive-observations.json"), nil
}

func (c *Connector) loadPassiveObservationState() (passiveObservationState, error) {
	path, err := c.passiveObservationStatePath()
	if err != nil {
		return passiveObservationState{}, err
	}
	raw, err := readRegularFileNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return passiveObservationState{Version: passiveObservationStateVersion}, nil
	}
	if err != nil {
		return passiveObservationState{}, err
	}
	var state passiveObservationState
	if err := json.Unmarshal(raw, &state); err != nil {
		return passiveObservationState{}, err
	}
	if state.Version != passiveObservationStateVersion {
		return passiveObservationState{}, fmt.Errorf("unsupported passive observation state version %d", state.Version)
	}
	if len(state.Pending) > passiveObservationBatchSize || (len(state.Pending) > 0 && strings.TrimSpace(state.PendingCursor) == "") ||
		len(state.Conflicts) > passiveObservationConflictCap {
		return passiveObservationState{}, errors.New("passive observation state is inconsistent")
	}
	return state, nil
}

func (c *Connector) savePassiveObservationState(state passiveObservationState) error {
	path, err := c.passiveObservationStatePath()
	if err != nil {
		return err
	}
	state.Version = passiveObservationStateVersion
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(path, append(raw, '\n'))
}

func observationCapabilitiesForGatewayConfig(cfg GatewayLocalConfig) *contracts.ConnectorObservationCapabilities {
	capabilities := contracts.ConnectorObservationCapabilities{}
	kind := strings.ToLower(strings.TrimSpace(cfg.GatewayKind))
	if kind == "sub2api" || kind == "newapi" || kind == "new-api" || kind == "cpa" && cfg.Runtime.CPAUsageStatisticsEnabled {
		capabilities = contracts.ConnectorObservationCapabilities{
			PassiveCollection:   true,
			SuccessEvents:       true,
			FailureEvents:       true,
			ErrorClassification: true,
			FirstTokenMS:        true,
			TotalMS:             true,
			TokenCounts:         true,
		}
	}
	return &capabilities
}
