package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/strategy"
)

type operationsCenterResponse struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Summary     operationsCenterSummary  `json:"summary"`
	Sources     []operationsSourceHealth `json:"sources"`
	Incidents   []operationsIncident     `json:"incidents"`
	Onboarding  []operationsOnboarding   `json:"onboarding"`
	Timeline    []operationsTimelineItem `json:"timeline"`
}

type operationsCenterSummary struct {
	PublishedPlans       int     `json:"published_plans"`
	ManagedBindings      int     `json:"managed_bindings"`
	SchedulableBindings  int     `json:"schedulable_bindings"`
	IsolatedBindings     int     `json:"isolated_bindings"`
	RecoveringBindings   int     `json:"recovering_bindings"`
	UnknownBindings      int     `json:"unknown_bindings"`
	FreshEvidencePercent float64 `json:"fresh_evidence_percent"`
	OpenIncidents        int     `json:"open_incidents"`
	ManualRecovery       int     `json:"manual_recovery"`
	OnboardingPending    int     `json:"onboarding_pending"`
	OnboardingRetryable  int     `json:"onboarding_retryable"`
	OnboardingActive     int     `json:"onboarding_active"`
	OnboardingDormant    int     `json:"onboarding_dormant"`
}

type operationsOnboarding struct {
	ID                  string                     `json:"id"`
	UserID              int64                      `json:"user_id"`
	InstanceID          string                     `json:"instance_id"`
	PoolID              string                     `json:"pool_id"`
	ConnectorID         string                     `json:"connector_id,omitempty"`
	PlanID              string                     `json:"plan_id,omitempty"`
	Stage               contracts.OnboardingStage  `json:"stage"`
	Status              contracts.OnboardingStatus `json:"status"`
	Attempts            int                        `json:"attempts"`
	DeliveredKeys       int                        `json:"delivered_keys"`
	LastErrorCode       string                     `json:"last_error_code,omitempty"`
	DesiredGeneration   int64                      `json:"desired_generation"`
	LastReadyGeneration int64                      `json:"last_ready_generation"`
	LastReadyAt         *time.Time                 `json:"last_ready_at,omitempty"`
	NextAttemptAt       *time.Time                 `json:"next_attempt_at,omitempty"`
	UpdatedAt           time.Time                  `json:"updated_at"`
}

type operationsSourceHealth struct {
	SourceID           string                `json:"source_id"`
	DisplayName        string                `json:"display_name"`
	Models             []string              `json:"models,omitempty"`
	TotalBindings      int                   `json:"total_bindings"`
	Schedulable        int                   `json:"schedulable"`
	Isolated           int                   `json:"isolated"`
	Recovering         int                   `json:"recovering"`
	Unknown            int                   `json:"unknown"`
	PassiveRequests5m  int                   `json:"passive_requests_5m"`
	WorstQualityScore  *float64              `json:"worst_quality_score,omitempty"`
	EvidenceUpdatedAt  *time.Time            `json:"evidence_updated_at,omitempty"`
	EvidenceFresh      bool                  `json:"evidence_fresh"`
	EvidenceConfidence float64               `json:"evidence_confidence"`
	HealthState        contracts.HealthState `json:"health_state"`
}

type operationsIncident struct {
	PlanID                string                          `json:"plan_id"`
	InstanceID            string                          `json:"instance_id"`
	UserID                int64                           `json:"user_id"`
	ChannelID             string                          `json:"channel_id"`
	SourceID              string                          `json:"source_id"`
	DisplayName           string                          `json:"display_name"`
	Status                string                          `json:"status"`
	BindingState          contracts.PublishedBindingState `json:"binding_state"`
	CircuitState          contracts.QualityCircuitState   `json:"circuit_state,omitempty"`
	QualityScore          *float64                        `json:"quality_score,omitempty"`
	Penalty               strategy.PenaltyBreakdown       `json:"penalty"`
	EvidenceUpdatedAt     *time.Time                      `json:"evidence_updated_at,omitempty"`
	EvidenceFresh         bool                            `json:"evidence_fresh"`
	EvidenceConfidence    float64                         `json:"evidence_confidence"`
	OpenedAt              *time.Time                      `json:"opened_at,omitempty"`
	NextProbeAt           *time.Time                      `json:"next_probe_at,omitempty"`
	LastProbeAt           *time.Time                      `json:"last_probe_at,omitempty"`
	SuccessfulProbes      int                             `json:"successful_probes"`
	RecoveryStage         int                             `json:"recovery_stage,omitempty"`
	RecoveryObserveAfter  *time.Time                      `json:"recovery_observe_after,omitempty"`
	Reason                contracts.QualityCircuitReason  `json:"reason"`
	ConnectorRecoveryMode string                          `json:"connector_recovery_mode"`
	ConnectorLastSeenAt   *time.Time                      `json:"connector_last_seen_at,omitempty"`
	AffectedDownstreams   int                             `json:"affected_downstreams"`
	AffectedRequests5m    int                             `json:"affected_requests_5m"`
	CurrentRoutes         []operationsCurrentRoute        `json:"current_routes"`
	EjectionCohortPercent int                             `json:"ejection_cohort_percent,omitempty"`
	EjectionCohortRole    string                          `json:"ejection_cohort_role,omitempty"`
	RecoveryCohortRole    string                          `json:"recovery_cohort_role,omitempty"`
}

type operationsCurrentRoute struct {
	SourceID    string `json:"source_id"`
	DisplayName string `json:"display_name"`
}

type operationsTimelineItem struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	PlanID     string    `json:"plan_id,omitempty"`
	InstanceID string    `json:"instance_id,omitempty"`
	UserID     int64     `json:"user_id,omitempty"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail,omitempty"`
	At         time.Time `json:"at"`
}

func (s *Server) handleOperationsCenter(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	response := operationsCenterResponse{
		GeneratedAt: now,
		Sources:     []operationsSourceHealth{},
		Incidents:   []operationsIncident{},
		Onboarding:  []operationsOnboarding{},
		Timeline:    []operationsTimelineItem{},
	}
	plans, err := s.store.ListRoutePlans(ctx, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	channels, err := s.store.ListUpstreamChannels(ctx, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	channelByID := make(map[string]contracts.UpstreamChannel, len(channels))
	for _, channel := range channels {
		channelByID[channel.ID] = channel
	}
	connectors, err := s.store.ListConnectors(ctx, contracts.ConnectorFilter{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	connectorByInstance := make(map[string]contracts.Connector, len(connectors))
	for _, connector := range connectors {
		connectorByInstance[connector.InstanceID] = connector
	}

	sourceByID := map[string]*operationsSourceHealth{}
	incidentCountBySource := map[string]int{}
	incidentRequestsBySource := map[string]int{}
	for _, plan := range plans {
		if plan.Status != contracts.RoutePlanPublished {
			continue
		}
		response.Summary.PublishedPlans++
		if err := s.appendPlanOperations(
			ctx, &response, plan, channelByID, connectorByInstance,
			sourceByID, incidentCountBySource, incidentRequestsBySource, now,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
	}
	if err := s.appendConnectorTaskTimeline(ctx, &response, plans, now); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := s.appendOnboardingOperations(ctx, &response); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for _, source := range sourceByID {
		if source.TotalBindings > 0 {
			source.EvidenceConfidence /= float64(source.TotalBindings)
		}
		response.Sources = append(response.Sources, *source)
	}
	for i := range response.Incidents {
		response.Incidents[i].AffectedDownstreams = incidentCountBySource[response.Incidents[i].SourceID]
		response.Incidents[i].AffectedRequests5m = incidentRequestsBySource[response.Incidents[i].SourceID]
	}
	if response.Summary.ManagedBindings > 0 {
		fresh := response.Summary.ManagedBindings - response.Summary.UnknownBindings
		response.Summary.FreshEvidencePercent = 100 * float64(fresh) / float64(response.Summary.ManagedBindings)
	}
	response.Summary.OpenIncidents = len(response.Incidents)
	sort.Slice(response.Sources, func(i, j int) bool {
		if response.Sources[i].Isolated != response.Sources[j].Isolated {
			return response.Sources[i].Isolated > response.Sources[j].Isolated
		}
		return response.Sources[i].SourceID < response.Sources[j].SourceID
	})
	sort.Slice(response.Incidents, func(i, j int) bool {
		return operationsIncidentTime(response.Incidents[i]).After(operationsIncidentTime(response.Incidents[j]))
	})
	sort.Slice(response.Timeline, func(i, j int) bool { return response.Timeline[i].At.After(response.Timeline[j].At) })
	if len(response.Timeline) > 100 {
		response.Timeline = response.Timeline[:100]
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) appendOnboardingOperations(ctx context.Context, response *operationsCenterResponse) error {
	workflows, err := s.store.ListOnboardingWorkflows(ctx, contracts.OnboardingWorkflowFilter{Limit: 200})
	if err != nil {
		return err
	}
	for _, workflow := range workflows {
		stage, status := operationsOnboardingServiceState(workflow)
		item := operationsOnboarding{
			ID: workflow.ID, UserID: workflow.UserID, InstanceID: workflow.InstanceID,
			PoolID: workflow.PoolID, ConnectorID: workflow.ConnectorID, PlanID: workflow.PlanID,
			Stage: stage, Status: status, Attempts: workflow.Attempts,
			DeliveredKeys: len(workflow.KeyVersionSummary), LastErrorCode: workflow.LastErrorCode,
			DesiredGeneration: workflow.DesiredGeneration, LastReadyGeneration: workflow.LastReadyGeneration,
			NextAttemptAt: workflow.NextAttemptAt, LastReadyAt: workflow.LastReadyAt, UpdatedAt: workflow.UpdatedAt,
		}
		response.Onboarding = append(response.Onboarding, item)
		switch status {
		case contracts.OnboardingReady:
			response.Summary.OnboardingActive++
		case contracts.OnboardingRetryable:
			response.Summary.OnboardingRetryable++
		case contracts.OnboardingDormantStatus:
			response.Summary.OnboardingDormant++
		default:
			response.Summary.OnboardingPending++
		}
		response.Timeline = append(response.Timeline, operationsTimelineItem{
			ID: workflow.ID, Kind: "onboarding_workflow", Status: string(status),
			PlanID: workflow.PlanID, InstanceID: workflow.InstanceID, UserID: workflow.UserID,
			Title: string(stage), Detail: workflow.LastErrorCode, At: workflow.UpdatedAt,
		})
	}
	return nil
}

// operationsOnboardingServiceState keeps the service view stable while a due
// ready workflow performs its periodic read-only verification. Claiming that
// workflow temporarily changes its durable execution state to
// running/checking_gateway, but the already-delivered desired generation is
// still serving. First activation, a changed desired generation, later repair
// stages, and every failed/retryable state retain their actual execution state.
func operationsOnboardingServiceState(workflow contracts.OnboardingWorkflow) (contracts.OnboardingStage, contracts.OnboardingStatus) {
	if workflow.Status == contracts.OnboardingRunning &&
		(workflow.Stage == contracts.OnboardingActive || workflow.Stage == contracts.OnboardingCheckingGateway) &&
		workflow.DesiredGeneration > 0 &&
		workflow.LastReadyGeneration == workflow.DesiredGeneration &&
		workflow.LastReadyAt != nil {
		return contracts.OnboardingActive, contracts.OnboardingReady
	}
	return workflow.Stage, workflow.Status
}

func (s *Server) appendConnectorTaskTimeline(
	ctx context.Context,
	response *operationsCenterResponse,
	plans []contracts.RoutePlan,
	now time.Time,
) error {
	tasks, err := s.store.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{
		Types: []contracts.ConnectorTaskType{
			contracts.ConnectorTaskGatewayAccountCreate,
			contracts.ConnectorTaskGatewayAccountUpdate,
			contracts.ConnectorTaskGatewayAccountDelete,
			contracts.ConnectorTaskGatewayQualityProbe,
			contracts.ConnectorTaskGatewayBindingProof,
		},
		Limit: 100,
	})
	if err != nil {
		return err
	}
	planByInstance := make(map[string]contracts.RoutePlan)
	for _, plan := range plans {
		if plan.Status == contracts.RoutePlanPublished {
			planByInstance[plan.InstanceID] = plan
		}
	}
	for _, task := range tasks {
		if !operationsTimelineTaskType(task.Type) {
			continue
		}
		plan := planByInstance[task.InstanceID]
		detail := task.Error.Code
		if task.AvailableAt.After(now) {
			detail = "scheduled for " + task.AvailableAt.UTC().Format(time.RFC3339)
		}
		response.Timeline = append(response.Timeline, operationsTimelineItem{
			ID: task.ID, Kind: "connector_task", Status: string(task.Status), PlanID: plan.ID,
			InstanceID: task.InstanceID, UserID: task.UserID, Title: string(task.Type),
			Detail: detail, At: task.UpdatedAt,
		})
	}
	return nil
}

func operationsTimelineTaskType(taskType contracts.ConnectorTaskType) bool {
	switch taskType {
	case contracts.ConnectorTaskGatewayAccountCreate,
		contracts.ConnectorTaskGatewayAccountUpdate,
		contracts.ConnectorTaskGatewayAccountDelete,
		contracts.ConnectorTaskGatewayQualityProbe,
		contracts.ConnectorTaskGatewayBindingProof:
		return true
	default:
		return false
	}
}

func (s *Server) appendPlanOperations(
	ctx context.Context,
	response *operationsCenterResponse,
	plan contracts.RoutePlan,
	channelByID map[string]contracts.UpstreamChannel,
	connectorByInstance map[string]contracts.Connector,
	sourceByID map[string]*operationsSourceHealth,
	incidentCountBySource map[string]int,
	incidentRequestsBySource map[string]int,
	now time.Time,
) error {
	bindings, err := s.store.ListPublishedBindings(ctx, plan.ID)
	if err != nil {
		return err
	}
	runtimes, err := s.store.ListQualityCircuitRuntimes(ctx, contracts.QualityCircuitRuntimeFilter{PlanID: plan.ID})
	if err != nil {
		return err
	}
	runtimeByChannel := make(map[string]contracts.QualityCircuitRuntime, len(runtimes))
	for _, runtime := range runtimes {
		runtimeByChannel[runtime.ChannelID] = runtime
	}
	strat, _ := s.resolvePlanStrategy(ctx, plan)
	for _, binding := range bindings {
		if binding.State == contracts.BindingRevoked {
			continue
		}
		channel, exists := channelByID[binding.ChannelID]
		if !exists {
			continue
		}
		response.Summary.ManagedBindings++
		if binding.State == contracts.BindingActive {
			response.Summary.SchedulableBindings++
		}
		sourceID := channel.SourceIdentity()
		source := sourceByID[sourceID]
		if source == nil {
			source = &operationsSourceHealth{
				SourceID: sourceID, DisplayName: channel.DisplayName, Models: append([]string(nil), channel.Models...),
				HealthState: contracts.HealthUnknown,
			}
			sourceByID[sourceID] = source
		}
		source.TotalBindings++
		if binding.State == contracts.BindingActive {
			source.Schedulable++
		}

		currentRoutes := operationsCurrentRoutes(bindings, channelByID, binding.ChannelID)
		var evaluation strategy.PenaltyEvaluation
		var evidenceAt *time.Time
		requestCount5m := 0
		fresh, confidence := false, 0.0
		observations, err := s.store.ListChannelObservations(ctx, contracts.ChannelObservationFilter{
			ChannelID: channel.ID, InstanceID: plan.InstanceID, Source: contracts.ObservationPassive,
			Since: now.Add(-contracts.Window5m.Duration()), Until: now,
		})
		if err != nil {
			return err
		}
		requestCount5m = len(observations)
		if snapshot, ok := s.worstSnapshot5m(ctx, plan.InstanceID, channel.ID, strat); ok {
			evaluation = strategy.EvaluatePenalty(strat, strategy.Candidate{Channel: channel, Snapshot: snapshot, State: snapshot.HealthState})
			at := snapshot.CreatedAt
			if at.IsZero() {
				at = snapshot.BucketStart
			}
			if !at.IsZero() {
				evidenceAt = timePointer(at)
				fresh = now.Sub(at) <= 2*contracts.Window5m.Duration()
				if source.EvidenceUpdatedAt == nil || at.After(*source.EvidenceUpdatedAt) {
					source.EvidenceUpdatedAt = timePointer(at)
				}
			}
			minSamples := strat.Thresholds.MinSamples
			if minSamples <= 0 {
				minSamples = 5
			}
			confidence = minFloat64(1, float64(snapshot.QualitySampleCount)/float64(minSamples))
			if fresh {
				score := evaluation.Score
				if source.WorstQualityScore == nil || score < *source.WorstQualityScore {
					source.WorstQualityScore = &score
				}
				source.HealthState = worseOperationsHealthState(source.HealthState, snapshot.HealthState)
			}
		}
		source.EvidenceConfidence += confidence
		source.PassiveRequests5m += requestCount5m
		if fresh {
			source.EvidenceFresh = true
		} else {
			source.Unknown++
			response.Summary.UnknownBindings++
		}

		runtime, hasRuntime := runtimeByChannel[channel.ID]
		status := ""
		if hasRuntime && (runtime.State == contracts.QualityCircuitOpen || runtime.State == contracts.QualityCircuitHalfOpen || runtime.RecoveryReady) {
			switch {
			case runtime.RecoveryReady:
				status = "recovering"
				source.Recovering++
				response.Summary.RecoveringBindings++
			case runtime.State == contracts.QualityCircuitHalfOpen:
				status = "probing"
				source.Recovering++
				response.Summary.RecoveringBindings++
			default:
				status = "isolated"
				source.Isolated++
				response.Summary.IsolatedBindings++
			}
		} else if fresh && evaluation.Eject {
			status = "needs_ejection"
		} else if binding.State == contracts.BindingFailed {
			status = "delivery_failed"
		}
		if status == "" {
			continue
		}
		connector := connectorByInstance[plan.InstanceID]
		recoveryMode := "manual"
		if operationsConnectorSupportsQualityProbe(connector, channel, now) {
			recoveryMode = "automatic"
		} else if status == "isolated" || status == "probing" || status == "recovering" {
			response.Summary.ManualRecovery++
		}
		var score *float64
		if evidenceAt != nil {
			value := evaluation.Score
			score = &value
		}
		incident := operationsIncident{
			PlanID: plan.ID, InstanceID: plan.InstanceID, UserID: plan.UserID,
			ChannelID: channel.ID, SourceID: sourceID, DisplayName: channel.DisplayName,
			Status: status, BindingState: binding.State, QualityScore: score, Penalty: evaluation.Penalties,
			EvidenceUpdatedAt: evidenceAt, EvidenceFresh: fresh, EvidenceConfidence: confidence,
			ConnectorRecoveryMode: recoveryMode, ConnectorLastSeenAt: connector.LastSeenAt,
			CurrentRoutes: currentRoutes,
		}
		if fresh && evaluation.Eject {
			incident.EjectionCohortPercent = strategy.QualityEjectionPercentage(
				s.consecutiveBadQualityWindows(ctx, plan, strat, channel),
			)
			incident.EjectionCohortRole = "unknown"
			if reader, ok := s.autoswitch.(SourceQualityCohortReader); ok {
				selected, known := reader.SourceQualityCohort(ctx, sourceID, incident.EjectionCohortPercent)
				if known {
					if selected[plan.ID] {
						incident.EjectionCohortRole = "selected"
					} else {
						incident.EjectionCohortRole = "holdout"
					}
				}
			}
		}
		if hasRuntime {
			incident.CircuitState = runtime.State
			incident.OpenedAt = runtime.OpenedAt
			incident.NextProbeAt = runtime.ProbeAfter
			incident.LastProbeAt = runtime.LastProbeAt
			incident.SuccessfulProbes = runtime.ConsecutiveProbeSuccesses
			incident.RecoveryStage = runtime.RecoveryStage
			incident.RecoveryObserveAfter = runtime.RecoveryObserveAfter
			incident.Reason = runtime.LastReason
			if runtime.State == contracts.QualityCircuitOpen || runtime.State == contracts.QualityCircuitHalfOpen {
				incident.EjectionCohortRole = "selected"
			}
			if runtime.RecoveryReady {
				if runtime.State == contracts.QualityCircuitClosed && binding.State == contracts.BindingActive {
					incident.RecoveryCohortRole = "canary"
				} else {
					incident.RecoveryCohortRole = "holdout"
				}
			}
		}
		response.Incidents = append(response.Incidents, incident)
		incidentCountBySource[sourceID]++
		incidentRequestsBySource[sourceID] += requestCount5m
	}

	decisions, err := s.store.ListAutoSwitchDecisions(ctx, contracts.AutoSwitchDecisionFilter{PlanID: plan.ID, Limit: 30})
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		response.Timeline = append(response.Timeline, operationsTimelineItem{
			ID: decision.ID, Kind: "decision", Status: string(decision.Status), PlanID: plan.ID,
			InstanceID: plan.InstanceID, UserID: plan.UserID, Title: decision.TriggerReason,
			Detail: firstNonEmpty(decision.ObservationNote, decision.Error, decision.RiskReason), At: decision.UpdatedAt,
		})
	}
	runs, err := s.store.ListReconcileRuns(ctx, plan.ID, 30)
	if err != nil {
		return err
	}
	for _, run := range runs {
		response.Timeline = append(response.Timeline, operationsTimelineItem{
			ID: run.ID, Kind: "gateway_receipt", Status: string(run.Status), PlanID: plan.ID,
			InstanceID: run.InstanceID, UserID: run.UserID,
			Title: string(run.Kind) + " · " + string(run.Trigger), Detail: run.Error, At: run.FinishedAt,
		})
	}
	return nil
}

func operationsConnectorSupportsQualityProbe(
	connector contracts.Connector,
	channel contracts.UpstreamChannel,
	now time.Time,
) bool {
	if connector.Status != contracts.ConnectorStatusOnline || connector.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		connector.Gateway.ProtocolVersion != contracts.ConnectorProtocolVersion || connector.Gateway.GatewayStatus != "ok" ||
		connector.LastSeenAt == nil || connector.LastSeenAt.Before(now.Add(-time.Minute)) ||
		!contracts.IsQualityProbeCapability(channel.ProbeCapability) ||
		!contracts.IsQualityProbeEndpointPath(channel.ProbeEndpointPath) {
		return false
	}
	taskSupported := false
	for _, capability := range connector.Gateway.Capabilities {
		if capability == contracts.ConnectorTaskGatewayQualityProbe {
			taskSupported = true
			break
		}
	}
	probe := connector.Gateway.QualityProbe
	if !taskSupported || probe == nil || !probe.Enabled ||
		probe.RecoveryMode != contracts.QualityProbeRecoveryAutomatic || !probe.FirstTokenMS || !probe.TotalMS {
		return false
	}
	capabilitySupported := false
	for _, capability := range probe.Capabilities {
		if capability == channel.ProbeCapability {
			capabilitySupported = true
			break
		}
	}
	if !capabilitySupported {
		return false
	}
	for _, endpointPath := range probe.EndpointPaths {
		if endpointPath == channel.ProbeEndpointPath {
			return true
		}
	}
	return false
}

func operationsCurrentRoutes(
	bindings []contracts.PublishedBinding,
	channelByID map[string]contracts.UpstreamChannel,
	excludeChannelID string,
) []operationsCurrentRoute {
	routes := make([]operationsCurrentRoute, 0)
	seen := make(map[string]bool)
	for _, binding := range bindings {
		if binding.ChannelID == excludeChannelID || binding.State != contracts.BindingActive {
			continue
		}
		channel, ok := channelByID[binding.ChannelID]
		if !ok {
			continue
		}
		sourceID := channel.SourceIdentity()
		if seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		routes = append(routes, operationsCurrentRoute{SourceID: sourceID, DisplayName: channel.DisplayName})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].SourceID < routes[j].SourceID })
	return routes
}

func operationsIncidentTime(incident operationsIncident) time.Time {
	for _, value := range []*time.Time{incident.OpenedAt, incident.LastProbeAt, incident.EvidenceUpdatedAt} {
		if value != nil {
			return *value
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func minFloat64(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

func worseOperationsHealthState(a, b contracts.HealthState) contracts.HealthState {
	rank := func(value contracts.HealthState) int {
		switch value {
		case contracts.HealthUnhealthy, contracts.HealthQuarantined:
			return 4
		case contracts.HealthDegraded, contracts.HealthRecovering:
			return 3
		case contracts.HealthHealthy:
			return 2
		default:
			return 1
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}
