package httpapi

import (
	"math"
	"net/http"
	"sort"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/strategy"
)

// The owner health payload is intentionally anonymous. Channel, pool, plan,
// instance, remote-account, model, credential, and decision-preview fields do
// not belong on this surface; owners get service facts and outcomes instead.
type ownerPoolHealthResponse struct {
	Capacity    ownerPoolCapacity       `json:"capacity"`
	SLA         ownerPoolSLA            `json:"sla"`
	Incidents   []ownerPoolIncident     `json:"incidents"`
	Switches    []ownerPoolSwitchResult `json:"switches"`
	GeneratedAt time.Time               `json:"generated_at"`
}

type ownerPoolCapacity struct {
	Published            int `json:"published"`
	Schedulable          int `json:"schedulable"`
	Isolated             int `json:"isolated"`
	AwaitingVerification int `json:"awaiting_verification"`
	VerificationFailed   int `json:"verification_failed"`
}

// SLA values are computed from every raw observation in the last five minutes.
// Unlike scheduling quality, this factual view does not remove client errors or
// cancellations from the request count.
type ownerPoolSLA struct {
	Window      contracts.HealthWindow `json:"window"`
	SuccessRate *float64               `json:"success_rate"`
	TTFTP95     *float64               `json:"ttft_p95_ms"`
	DurationP95 *float64               `json:"duration_p95_ms"`
	SampleCount int                    `json:"sample_count"`
	UpdatedAt   *time.Time             `json:"updated_at"`
}

type ownerPoolIncidentStatus string

const (
	ownerIncidentNeedsEjection  ownerPoolIncidentStatus = "needs_ejection"
	ownerIncidentIsolated       ownerPoolIncidentStatus = "isolated"
	ownerIncidentRecovering     ownerPoolIncidentStatus = "recovering"
	ownerIncidentDeliveryFailed ownerPoolIncidentStatus = "delivery_failed"
)

type ownerPoolIncident struct {
	Status      ownerPoolIncidentStatus `json:"status"`
	SuccessRate *float64                `json:"success_rate"`
	TTFTP95     *float64                `json:"ttft_p95_ms"`
	DurationP95 *float64                `json:"duration_p95_ms"`
	SampleCount int                     `json:"sample_count"`
	DetectedAt  *time.Time              `json:"detected_at,omitempty"`
	UpdatedAt   *time.Time              `json:"updated_at,omitempty"`
	Recovery    *ownerRecoveryProgress  `json:"recovery,omitempty"`
}

type ownerRecoveryProgress struct {
	SuccessfulProbes int        `json:"successful_probes"`
	RequiredProbes   int        `json:"required_probes"`
	NextProbeAt      *time.Time `json:"next_probe_at,omitempty"`
	LastProbeAt      *time.Time `json:"last_probe_at,omitempty"`
	RolloutStage     int        `json:"rollout_stage,omitempty"`
	ObserveAfter     *time.Time `json:"observe_after,omitempty"`
}

type ownerPoolSwitchOutcome string

const (
	ownerSwitchPending    ownerPoolSwitchOutcome = "pending"
	ownerSwitchInProgress ownerPoolSwitchOutcome = "in_progress"
	ownerSwitchSucceeded  ownerPoolSwitchOutcome = "succeeded"
	ownerSwitchSkipped    ownerPoolSwitchOutcome = "skipped"
	ownerSwitchRolledBack ownerPoolSwitchOutcome = "rolled_back"
	ownerSwitchFailed     ownerPoolSwitchOutcome = "failed"
)

type ownerPoolSwitchResult struct {
	Result     ownerPoolSwitchOutcome `json:"result"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

type ownerBindingHealth struct {
	binding     contracts.PublishedBinding
	snapshot    contracts.ChannelHealthSnapshot
	hasSnapshot bool
	evaluation  strategy.PenaltyEvaluation
	circuit     contracts.QualityCircuitRuntime
	hasCircuit  bool
}

func (s *Server) handleOwnerPoolHealth(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	userID, ok := s.scopeOwnerUser(w, r, r.URL.Query().Get("user_id"))
	if !ok {
		return
	}
	ctx := r.Context()
	plans, err := s.store.ListRoutePlans(ctx, userID)
	if err != nil {
		writeOwnerPoolHealthStoreError(w)
		return
	}

	generatedAt := time.Now().UTC()
	response := ownerPoolHealthResponse{
		SLA:         ownerPoolSLA{Window: contracts.Window5m},
		Incidents:   []ownerPoolIncident{},
		Switches:    []ownerPoolSwitchResult{},
		GeneratedAt: generatedAt,
	}
	var successes int
	ttftValues := []float64{}
	durationValues := []float64{}
	seenObservations := map[string]struct{}{}

	for _, plan := range plans {
		bindings, err := s.store.ListPublishedBindings(ctx, plan.ID)
		if err != nil {
			// Binding state is the capacity denominator. Returning an empty-looking
			// summary here would turn a storage failure into false availability.
			writeOwnerPoolHealthStoreError(w)
			return
		}
		circuits, err := s.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: plan.ID})
		if err != nil {
			writeOwnerPoolHealthStoreError(w)
			return
		}
		circuitByChannel := make(map[string]contracts.QualityCircuitRuntime, len(circuits))
		for _, circuit := range circuits {
			circuitByChannel[circuit.ChannelID] = circuit
		}

		resolvedStrategy, _ := s.resolvePlanStrategy(ctx, plan)
		for _, binding := range bindings {
			if binding.State == contracts.BindingRevoked {
				continue
			}
			response.Capacity.Published++

			circuit, hasCircuit := circuitByChannel[binding.ChannelID]
			isolated := hasCircuit && (circuit.State == contracts.QualityCircuitOpen || circuit.State == contracts.QualityCircuitHalfOpen)
			if isolated {
				response.Capacity.Isolated++
			} else if binding.IsCallable() {
				response.Capacity.Schedulable++
			}
			switch binding.VerificationStatus {
			case contracts.BindingVerificationPublishedPending, contracts.BindingVerificationAwaitingFirstRequest, "":
				response.Capacity.AwaitingVerification++
			case contracts.BindingVerificationFailed:
				response.Capacity.VerificationFailed++
			}

			channel, err := s.store.GetUpstreamChannel(ctx, binding.ChannelID)
			if err != nil {
				writeOwnerPoolHealthStoreError(w)
				return
			}
			snapshots, err := s.store.ListChannelHealthSnapshots(ctx, contracts.ChannelHealthSnapshotFilter{
				ChannelID: binding.ChannelID, InstanceID: binding.InstanceID, Window: contracts.Window5m,
			})
			if err != nil {
				writeOwnerPoolHealthStoreError(w)
				return
			}
			observations, err := s.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
				ChannelID: binding.ChannelID, InstanceID: binding.InstanceID,
				Source: contracts.ObservationPassive,
				Since:  generatedAt.Add(-5 * time.Minute), Until: generatedAt,
			})
			if err != nil {
				writeOwnerPoolHealthStoreError(w)
				return
			}
			for _, observation := range observations {
				if _, seen := seenObservations[observation.ID]; seen {
					continue
				}
				seenObservations[observation.ID] = struct{}{}
				response.SLA.SampleCount++
				if observation.Success {
					successes++
				}
				if observation.FirstTokenMS > 0 {
					ttftValues = append(ttftValues, observation.FirstTokenMS)
				}
				if observation.TotalMS > 0 {
					durationValues = append(durationValues, observation.TotalMS)
				}
				setLatestTime(&response.SLA.UpdatedAt, observation.ObservedAt)
			}
			health := ownerBindingHealth{binding: binding, circuit: circuit, hasCircuit: hasCircuit}
			health.evaluation = strategy.EvaluatePenalty(resolvedStrategy, strategy.Candidate{Channel: channel})
			for _, snapshot := range snapshots {
				evaluation := strategy.EvaluatePenalty(resolvedStrategy, strategy.Candidate{
					Channel: channel, Snapshot: snapshot, State: snapshot.HealthState,
				})
				if !health.hasSnapshot || evaluation.Score < health.evaluation.Score {
					health.snapshot = snapshot
					health.evaluation = evaluation
					health.hasSnapshot = true
				}
			}
			if incident, show := ownerIncidentFromBinding(health); show {
				response.Incidents = append(response.Incidents, incident)
			}
		}
	}
	if response.SLA.SampleCount > 0 {
		value := float64(successes) / float64(response.SLA.SampleCount)
		response.SLA.SuccessRate = &value
	}
	if len(ttftValues) > 0 {
		response.SLA.TTFTP95 = float64Pointer(ownerPercentile(ttftValues, .95))
	}
	if len(durationValues) > 0 {
		response.SLA.DurationP95 = float64Pointer(ownerPercentile(durationValues, .95))
	}

	decisions, err := s.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{UserID: userID, Limit: 20})
	if err != nil {
		writeOwnerPoolHealthStoreError(w)
		return
	}
	for _, decision := range decisions {
		response.Switches = append(response.Switches, ownerSwitchResult(decision))
	}
	sort.SliceStable(response.Incidents, func(i, j int) bool {
		return ownerIncidentRank(response.Incidents[i].Status) < ownerIncidentRank(response.Incidents[j].Status)
	})
	writeJSON(w, http.StatusOK, response)
}

func writeOwnerPoolHealthStoreError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "store_error", "managed upstream health is temporarily unavailable")
}

func ownerIncidentFromBinding(health ownerBindingHealth) (ownerPoolIncident, bool) {
	status := ownerPoolIncidentStatus("")
	if health.hasCircuit {
		if health.circuit.RecoveryReady {
			status = ownerIncidentRecovering
		}
		switch health.circuit.State {
		case contracts.QualityCircuitOpen:
			if !health.circuit.RecoveryReady {
				status = ownerIncidentIsolated
			}
		case contracts.QualityCircuitHalfOpen:
			status = ownerIncidentRecovering
		}
	}
	if status == "" && health.evaluation.Reason.Code == strategy.GatePenaltyThreshold {
		// A score crossing the policy line is a recommendation until the durable
		// circuit records the actual scheduling transition.
		status = ownerIncidentNeedsEjection
	}
	if status == "" && health.binding.State == contracts.BindingFailed {
		status = ownerIncidentDeliveryFailed
	}
	if status == "" {
		return ownerPoolIncident{}, false
	}

	incident := ownerPoolIncident{Status: status}
	if health.hasSnapshot {
		incident.SuccessRate = float64Pointer(health.snapshot.SuccessRate)
		incident.TTFTP95 = float64Pointer(health.snapshot.TTFTP95)
		incident.DurationP95 = float64Pointer(health.snapshot.DurationP95)
		incident.SampleCount = health.snapshot.SampleCount
		updatedAt := health.snapshot.CreatedAt
		if updatedAt.IsZero() {
			updatedAt = health.snapshot.BucketStart
		}
		incident.UpdatedAt = timePointer(updatedAt)
	}
	if status == ownerIncidentIsolated || status == ownerIncidentRecovering {
		incident.DetectedAt = copyTimePointer(health.circuit.OpenedAt)
		if incident.DetectedAt == nil {
			incident.DetectedAt = copyTimePointer(health.circuit.LastTransitionAt)
		}
		incident.Recovery = &ownerRecoveryProgress{
			SuccessfulProbes: health.circuit.ConsecutiveProbeSuccesses,
			RequiredProbes:   3,
			NextProbeAt:      copyTimePointer(health.circuit.ProbeAfter),
			LastProbeAt:      copyTimePointer(health.circuit.LastProbeAt),
			RolloutStage:     health.circuit.RecoveryStage,
			ObserveAfter:     copyTimePointer(health.circuit.RecoveryObserveAfter),
		}
	}
	return incident, true
}

func ownerSwitchResult(decision contracts.AutoSwitchDecision) ownerPoolSwitchResult {
	result := ownerPoolSwitchResult{StartedAt: decision.CreatedAt, FinishedAt: copyTimePointer(decision.ResolvedAt)}
	switch decision.Status {
	case contracts.AutoSwitchProposed:
		result.Result = ownerSwitchPending
	case contracts.AutoSwitchApplying, contracts.AutoSwitchObserving:
		result.Result = ownerSwitchInProgress
	case contracts.AutoSwitchCompleted:
		result.Result = ownerSwitchSucceeded
	case contracts.AutoSwitchSkipped:
		result.Result = ownerSwitchSkipped
	case contracts.AutoSwitchRolledBack:
		result.Result = ownerSwitchRolledBack
	case contracts.AutoSwitchFailed:
		result.Result = ownerSwitchFailed
	default:
		result.Result = ownerSwitchFailed
	}
	return result
}

func ownerIncidentRank(status ownerPoolIncidentStatus) int {
	switch status {
	case ownerIncidentIsolated:
		return 0
	case ownerIncidentRecovering:
		return 1
	case ownerIncidentNeedsEjection:
		return 2
	default:
		return 3
	}
}

func setLatestTime(target **time.Time, value time.Time) {
	if value.IsZero() {
		return
	}
	if *target == nil || value.After(**target) {
		*target = timePointer(value)
	}
}

func ownerPercentile(values []float64, quantile float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := quantile * float64(len(sorted)-1)
	lower, upper := int(math.Floor(rank)), int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	fraction := rank - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*fraction
}

func float64Pointer(value float64) *float64 { return &value }

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return timePointer(*value)
}
