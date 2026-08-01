package httpapi

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/upstreamcost"
)

const (
	maxObservationBatch    = 500
	maxObservationIDLength = 128
	maxObservationAge      = 24 * time.Hour
	maxObservationSkew     = 2 * time.Minute
)

type observationBinding struct {
	planID    string
	channelID string
	remoteID  string
	poolID    string
}

// handleConnectorObservations accepts passive request telemetry and active
// probe results from a connector. The authenticated connector fixes the
// instance scope; channel and remote ids must resolve to that instance's own
// published bindings before any row is appended.
func (s *Server) handleConnectorObservations(w http.ResponseWriter, r *http.Request) {
	if s.quality == nil {
		writeError(w, http.StatusServiceUnavailable, "quality_metrics_disabled", "quality observation intake is not enabled")
		return
	}
	var req contracts.ConnectorObservationBatchRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	connector, ok := s.authorizeConnector(w, r)
	if !ok || !s.requireConnectorIdentity(w, r, strings.TrimSpace(req.ConnectorID), connector) {
		return
	}
	if len(req.Observations) == 0 || len(req.Observations) > maxObservationBatch {
		writeError(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("observations must contain 1..%d items", maxObservationBatch))
		return
	}

	byChannel, byRemote, err := s.connectorObservationBindings(r, connector)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	now := time.Now().UTC()
	type resolvedObservation struct {
		quality contracts.ChannelObservation
		cost    *store.UpstreamCostAttributionJob
	}
	resolved := make([]resolvedObservation, 0, len(req.Observations))
	seenIDs := make(map[string]struct{}, len(req.Observations))
	for i, input := range req.Observations {
		binding, err := resolveObservationBinding(input, byChannel, byRemote)
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("observations[%d]: %v", i, err))
			return
		}
		if err := validateConnectorObservation(input, now); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("observations[%d]: %v", i, err))
			return
		}
		if _, duplicate := seenIDs[input.ObservationID]; duplicate {
			writeError(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("observations[%d]: observation_id is duplicated within the batch", i))
			return
		}
		seenIDs[input.ObservationID] = struct{}{}
		observedAt := input.ObservedAt
		if !observedAt.IsZero() {
			// PostgreSQL timestamptz stores microseconds. Canonicalizing before the
			// first write keeps a lost-response retry byte-for-fact idempotent across
			// memory and PostgreSQL stores.
			observedAt = observedAt.UTC().Truncate(time.Microsecond)
		}
		source := input.Source
		if source == "" {
			source = contracts.ObservationPassive
		}
		quality := contracts.ChannelObservation{
			ID:           connectorObservationID(connector.ID, input.ObservationID),
			ChannelID:    binding.channelID,
			InstanceID:   connector.InstanceID,
			PoolID:       binding.poolID,
			Model:        strings.TrimSpace(input.Model),
			Capability:   input.Capability,
			EndpointPath: strings.TrimSpace(input.EndpointPath),
			Success:      input.Success,
			StatusCode:   input.StatusCode,
			ErrorType:    input.ErrorType,
			FirstTokenMS: input.FirstTokenMS,
			TotalMS:      input.TotalMS,
			InputTokens:  input.InputTokens,
			OutputTokens: input.OutputTokens,
			Source:       source,
			ObservedAt:   observedAt,
		}
		var costJob *store.UpstreamCostAttributionJob
		if input.CostUsage != nil {
			usage, err := upstreamcost.UsageFromConnectorObservation(
				connector.UserID, connector.InstanceID, binding.channelID, quality.ID, input,
			)
			if err != nil {
				writeError(w, http.StatusBadRequest, "validation_failed", fmt.Sprintf("observations[%d]: invalid cost_usage", i))
				return
			}
			costJob = &store.UpstreamCostAttributionJob{
				UsageObservationID: usage.ObservationID, UserID: usage.UserID,
				ChannelID: usage.ChannelID, InstanceID: usage.InstanceID,
				ModelKey: usage.ModelKey, GroupKey: usage.GroupKey,
				InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
				CachedInputTokens: usage.CachedInputTokens, RequestCount: usage.RequestCount,
				OccurredAt: usage.OccurredAt, CalculationVersion: contracts.UpstreamCostCalculationVersionV1,
			}
		}
		resolved = append(resolved, resolvedObservation{quality: quality, cost: costJob})
	}
	if s.costObservations == nil {
		for _, item := range resolved {
			if item.cost != nil {
				writeError(w, http.StatusServiceUnavailable, "upstream_cost_disabled", "cost attribution intake is not enabled")
				return
			}
		}
	}

	// Validate the complete batch before writing. A store failure may leave an
	// append-only prefix; retrying the same connector-scoped observation IDs is
	// idempotent and completes the remainder without double-counting that prefix.
	for _, item := range resolved {
		obs := item.quality
		var err error
		if item.cost != nil {
			_, err = s.costObservations.AppendChannelObservationWithCostJob(r.Context(), obs, item.cost)
		} else {
			_, err = s.quality.RecordObservation(r.Context(), obs)
		}
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, "observation_id_conflict", "observation_id was already used for different observation content")
				return
			}
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		binding := byChannel[obs.ChannelID]
		verificationStatus := contracts.BindingVerificationFailed
		verificationSource := contracts.BindingVerificationSourcePassive
		verificationErrorCode := string(obs.ErrorType)
		if obs.Source == contracts.ObservationProbe {
			verificationSource = contracts.BindingVerificationSourceProbe
		}
		if obs.Success {
			verificationErrorCode = ""
			if obs.Source == contracts.ObservationProbe {
				verificationStatus = contracts.BindingVerificationProbeVerified
			} else {
				verificationStatus = contracts.BindingVerificationPassiveVerified
			}
		}
		if _, err := s.store.RecordPublishedBindingVerification(r.Context(), binding.planID, binding.channelID, verificationStatus, verificationSource, obs.ObservedAt, verificationErrorCode); err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusAccepted, contracts.ConnectorObservationBatchResponse{Accepted: len(resolved)})
}

// connectorObservationID scopes a connector-local event identifier before it
// reaches the store's global primary key. The length prefix keeps the mapping
// unambiguous regardless of characters allowed in connector IDs.
func connectorObservationID(connectorID, observationID string) string {
	return fmt.Sprintf("c%d:%s:%s", len(connectorID), connectorID, observationID)
}

func (s *Server) connectorObservationBindings(r *http.Request, connector contracts.Connector) (map[string]observationBinding, map[string][]observationBinding, error) {
	plans, err := s.store.ListRoutePlans(r.Context(), connector.UserID)
	if err != nil {
		return nil, nil, err
	}
	byChannel := map[string]observationBinding{}
	byRemote := map[string][]observationBinding{}
	for _, plan := range plans {
		if plan.InstanceID != connector.InstanceID || plan.Status == contracts.RoutePlanDraft {
			continue
		}
		bindings, err := s.store.ListPublishedBindings(r.Context(), plan.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, binding := range bindings {
			if binding.InstanceID != "" && binding.InstanceID != connector.InstanceID {
				continue
			}
			if binding.State != contracts.BindingActive && binding.State != contracts.BindingDisabled && binding.State != contracts.BindingFailed {
				continue
			}
			item := observationBinding{planID: plan.ID, channelID: binding.ChannelID, remoteID: binding.RemoteID, poolID: plan.PoolID}
			byChannel[binding.ChannelID] = item
			if binding.RemoteID != "" {
				byRemote[binding.RemoteID] = append(byRemote[binding.RemoteID], item)
			}
		}
	}
	return byChannel, byRemote, nil
}

func resolveObservationBinding(input contracts.ConnectorChannelObservation, byChannel map[string]observationBinding, byRemote map[string][]observationBinding) (observationBinding, error) {
	channelID := strings.TrimSpace(input.ChannelID)
	remoteID := strings.TrimSpace(input.RemoteID)
	if channelID == "" && remoteID == "" {
		return observationBinding{}, errors.New("channel_id or remote_id is required")
	}
	var byCh observationBinding
	var chOK bool
	if channelID != "" {
		byCh, chOK = byChannel[channelID]
		if !chOK {
			return observationBinding{}, errors.New("channel_id is not published to this connector instance")
		}
	}
	var byRem observationBinding
	var remOK bool
	if remoteID != "" {
		matches := byRemote[remoteID]
		if len(matches) == 0 {
			return observationBinding{}, errors.New("remote_id is not published to this connector instance")
		}
		if chOK {
			for _, candidate := range matches {
				if candidate.channelID == byCh.channelID && candidate.poolID == byCh.poolID && candidate.remoteID == byCh.remoteID {
					byRem, remOK = candidate, true
					break
				}
			}
			if !remOK {
				return observationBinding{}, errors.New("channel_id and remote_id refer to different bindings")
			}
		} else {
			if len(matches) != 1 {
				return observationBinding{}, errors.New("remote_id is ambiguous; channel_id is required")
			}
			byRem, remOK = matches[0], true
		}
	}
	if chOK && remOK && (byCh.channelID != byRem.channelID || byCh.poolID != byRem.poolID) {
		return observationBinding{}, errors.New("channel_id and remote_id refer to different bindings")
	}
	if chOK {
		return byCh, nil
	}
	return byRem, nil
}

func validateConnectorObservation(input contracts.ConnectorChannelObservation, now time.Time) error {
	if !validObservationID(input.ObservationID) {
		return fmt.Errorf("observation_id must be 1..%d characters using letters, digits, '.', '_', ':' or '-'", maxObservationIDLength)
	}
	if strings.TrimSpace(input.Model) == "" {
		return errors.New("model is required")
	}
	if input.Source == contracts.ObservationProbe || input.Capability != "" || strings.TrimSpace(input.EndpointPath) != "" {
		if !contracts.IsQualityProbeCapability(input.Capability) || !contracts.IsQualityProbeEndpointPath(input.EndpointPath) {
			return errors.New("probe observations require capability and endpoint_path")
		}
	}
	if input.StatusCode < 0 || input.StatusCode > 599 {
		return errors.New("status_code must be between 0 and 599")
	}
	if !finiteNonNegative(input.FirstTokenMS) || !finiteNonNegative(input.TotalMS) {
		return errors.New("latency values must be finite and non-negative")
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 {
		return errors.New("token counts must be non-negative")
	}
	if input.FirstTokenMS > 0 && input.TotalMS > 0 && input.FirstTokenMS > input.TotalMS {
		return errors.New("first_token_ms cannot exceed total_ms")
	}
	if input.Source != "" && input.Source != contracts.ObservationPassive && input.Source != contracts.ObservationProbe {
		return errors.New("source must be passive or probe")
	}
	if !validObservationErrorType(input.ErrorType) {
		return errors.New("error_type is not supported")
	}
	if input.Success && input.ErrorType != contracts.ErrorNone {
		return errors.New("a successful observation cannot include error_type")
	}
	if !input.Success && input.ErrorType == contracts.ErrorNone {
		return errors.New("a failed observation must include error_type (use unknown when attribution is unavailable)")
	}
	if !input.ObservedAt.IsZero() {
		observedAt := input.ObservedAt.UTC()
		if observedAt.Before(now.Add(-maxObservationAge)) || observedAt.After(now.Add(maxObservationSkew)) {
			return errors.New("observed_at is outside the accepted time range")
		}
	}
	return nil
}

func validObservationID(value string) bool {
	if len(value) == 0 || len(value) > maxObservationIDLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validObservationErrorType(value contracts.ObservationErrorType) bool {
	switch value {
	case contracts.ErrorNone, contracts.ErrorTimeout, contracts.ErrorRateLimit,
		contracts.ErrorAuth, contracts.ErrorInsufficientBalance, contracts.ErrorServer,
		contracts.ErrorClient, contracts.ErrorNetwork, contracts.ErrorCanceled, contracts.ErrorPlatform,
		contracts.ErrorUnknown:
		return true
	default:
		return false
	}
}
