// Package recommendationrollout implements the pure staged traffic state
// machine for cost optimization. It emits reconcile intents but performs no
// store, gateway, HTTP, or autoswitch mutation.
package recommendationrollout

import (
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

const maxObservationSeconds = 7 * 24 * 60 * 60

// Advance evaluates one event. All returned slices are deterministic and the
// input is copied before transition, so callers can safely retain prior state.
func Advance(current contracts.RecommendationRolloutState, event contracts.RecommendationRolloutEvent) contracts.RecommendationRolloutDecision {
	next := cloneState(current)
	decision := &contracts.RecommendationRolloutDecision{
		State:       next,
		Action:      contracts.RecommendationRolloutAction{Kind: contracts.RecommendationRolloutActionNone},
		Reasons:     []contracts.RecommendationRolloutBlockReason{},
		FailedGates: []contracts.RecommendationRolloutGateKind{},
	}
	addReason := func(reason contracts.RecommendationRolloutBlockReason) {
		for _, existing := range decision.Reasons {
			if existing == reason {
				return
			}
		}
		decision.Reasons = append(decision.Reasons, reason)
	}

	if !validState(current) {
		addReason(contracts.RecommendationRolloutBlockedInvalidState)
		return block(decision, event.Now)
	}
	if !validEvent(event) {
		addReason(contracts.RecommendationRolloutBlockedInvalidEvent)
		return block(decision, event.Now)
	}
	if event.UserID != current.UserID || event.PlanID != current.PlanID || event.RecommendationID != current.RecommendationID {
		addReason(contracts.RecommendationRolloutBlockedOwnerScope)
		return block(decision, event.Now)
	}

	switch event.Type {
	case contracts.RecommendationRolloutEventEvaluate:
		return evaluate(decision, event, addReason)
	case contracts.RecommendationRolloutEventStageApplied:
		return stageApplied(decision, event, addReason)
	case contracts.RecommendationRolloutEventStageApplyFailed:
		return stageApplyFailed(decision, event, addReason)
	case contracts.RecommendationRolloutEventObserve:
		return observe(decision, event, addReason)
	case contracts.RecommendationRolloutEventRetryRollback:
		return retryRollback(decision, event, addReason)
	case contracts.RecommendationRolloutEventRollbackApplied:
		return rollbackApplied(decision, event, addReason)
	case contracts.RecommendationRolloutEventRollbackFailed:
		return rollbackFailed(decision, event, addReason)
	default:
		addReason(contracts.RecommendationRolloutBlockedInvalidEvent)
		return block(decision, event.Now)
	}
}

func evaluate(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	state := &decision.State
	if state.Status != contracts.RecommendationRolloutReady {
		add(contracts.RecommendationRolloutBlockedInvalidState)
		return block(decision, event.Now)
	}
	revalidation := event.Revalidation
	failedGates := validateRevalidation(*state, revalidation, event.Now, add)
	decision.FailedGates = failedGates
	if len(decision.Reasons) > 0 {
		return block(decision, event.Now)
	}
	target := nextStage(state.Stage)
	if target == contracts.RecommendationRolloutStageNone {
		state.Status = contracts.RecommendationRolloutCompleted
		state.UpdatedAt = event.Now.UTC()
		decision.State = *state
		return *decision
	}
	state.Status = contracts.RecommendationRolloutApplying
	state.PendingStage = target
	state.UpdatedAt = event.Now.UTC()
	decision.State = *state
	decision.Action = stageAction(*state, contracts.RecommendationRolloutActionApplyStage, target)
	return *decision
}

func stageApplied(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	state := &decision.State
	if state.Status != contracts.RecommendationRolloutApplying || state.PendingStage == contracts.RecommendationRolloutStageNone || event.AppliedStage != state.PendingStage {
		add(contracts.RecommendationRolloutBlockedStageMismatch)
		return block(decision, event.Now)
	}
	if !eventIdentityMatches(*state, event, add) {
		return rollbackRequired(decision, event.Now)
	}
	state.Stage = state.PendingStage
	state.PendingStage = contracts.RecommendationRolloutStageNone
	state.Status = contracts.RecommendationRolloutObserving
	started := event.Now.UTC()
	observeUntil := started.Add(time.Duration(state.ObservationSeconds) * time.Second)
	state.StageStartedAt = &started
	state.ObserveUntil = &observeUntil
	state.UpdatedAt = started
	decision.State = *state
	return *decision
}

func stageApplyFailed(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	state := decision.State
	if state.Status != contracts.RecommendationRolloutApplying || state.PendingStage == contracts.RecommendationRolloutStageNone || event.AppliedStage != state.PendingStage {
		add(contracts.RecommendationRolloutBlockedStageMismatch)
		return block(decision, event.Now)
	}
	add(contracts.RecommendationRolloutBlockedApplyFailed)
	return rollbackRequired(decision, event.Now)

}

func observe(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	state := &decision.State
	if state.Status != contracts.RecommendationRolloutObserving || state.ObserveUntil == nil || state.Stage == contracts.RecommendationRolloutStageNone {
		add(contracts.RecommendationRolloutBlockedInvalidState)
		return block(decision, event.Now)
	}
	if event.Now.Before(*state.ObserveUntil) {
		add(contracts.RecommendationRolloutBlockedInvalidEvent)
		return *decision
	}
	revalidationFailed := validateRevalidation(*state, event.Revalidation, event.Now, add)
	decision.FailedGates = revalidationFailed
	after := event.AfterEvidence
	if !validAfterEvidence(*state, after, event.Now) {
		add(contracts.RecommendationRolloutBlockedAfterEvidence)
	} else {
		state.LastAfterEvidence = cloneAfterEvidence(after)
		if after.Callability != contracts.RecommendationRolloutGatePassed {
			add(contracts.RecommendationRolloutBlockedCallability)
		}
		if after.Quality != contracts.RecommendationRolloutGatePassed {
			add(contracts.RecommendationRolloutBlockedQuality)
		}
	}
	if len(decision.Reasons) > 0 {
		return rollbackRequired(decision, event.Now)
	}
	state.ObserveUntil = nil
	state.Status = contracts.RecommendationRolloutReady
	state.UpdatedAt = event.Now.UTC()
	if state.Stage == contracts.RecommendationRolloutStage100 {
		state.Status = contracts.RecommendationRolloutCompleted
	}
	decision.State = *state
	return *decision
}

func retryRollback(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	if decision.State.Status != contracts.RecommendationRolloutRollbackRequired {
		add(contracts.RecommendationRolloutBlockedInvalidState)
		return block(decision, event.Now)
	}
	decision.Action = rollbackAction(decision.State)
	decision.State.UpdatedAt = event.Now.UTC()
	return *decision
}

func rollbackApplied(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	state := &decision.State
	if state.Status != contracts.RecommendationRolloutRollbackRequired || event.AppliedStage != contracts.RecommendationRolloutStageNone ||
		event.RecommendationFingerprint != state.RecommendationFingerprint || event.SchedulingGeneration != state.SchedulingGeneration {
		add(contracts.RecommendationRolloutBlockedStageMismatch)
		return rollbackRequired(decision, event.Now)
	}
	after := event.AfterEvidence
	if after == nil || after.Stage != contracts.RecommendationRolloutStageNone || after.BaselineFingerprint != state.BaselineFingerprint ||
		after.SchedulingGeneration != state.SchedulingGeneration || after.Callability != contracts.RecommendationRolloutGateUnknown ||
		after.Quality != contracts.RecommendationRolloutGateUnknown || !freshWindow(after.ObservedAt, after.FreshUntil, event.Now) ||
		!validRollbackWeightSetEvidence(after.EvidenceIDs, state.BaselineFingerprint) {
		add(contracts.RecommendationRolloutBlockedAfterEvidence)
		return rollbackRequired(decision, event.Now)
	}
	state.Stage = contracts.RecommendationRolloutStageNone
	state.PendingStage = contracts.RecommendationRolloutStageNone
	state.Status = contracts.RecommendationRolloutRolledBack
	state.StageStartedAt = nil
	state.ObserveUntil = nil
	state.LastAfterEvidence = cloneAfterEvidence(after)
	state.UpdatedAt = event.Now.UTC()
	decision.State = *state
	return *decision
}

func validRollbackWeightSetEvidence(values []string, fingerprint string) bool {
	return len(values) == 1 && values[0] == "weight-set-sha256:"+fingerprint
}

func rollbackFailed(decision *contracts.RecommendationRolloutDecision, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) contracts.RecommendationRolloutDecision {
	if decision.State.Status != contracts.RecommendationRolloutRollbackRequired {
		add(contracts.RecommendationRolloutBlockedInvalidState)
		return block(decision, event.Now)
	}
	add(contracts.RecommendationRolloutBlockedRollbackFailed)
	return rollbackRequired(decision, event.Now)
}

func validateRevalidation(state contracts.RecommendationRolloutState, value *contracts.RecommendationRolloutRevalidation, now time.Time, add func(contracts.RecommendationRolloutBlockReason)) []contracts.RecommendationRolloutGateKind {
	failed := make([]contracts.RecommendationRolloutGateKind, 0)
	if value == nil {
		add(contracts.RecommendationRolloutBlockedMissingGate)
		return contracts.RecommendationRolloutRequiredGates()
	}
	if value.UserID != state.UserID || value.PlanID != state.PlanID || value.RecommendationID != state.RecommendationID {
		add(contracts.RecommendationRolloutBlockedOwnerScope)
	}
	if value.RecommendationFingerprint != state.RecommendationFingerprint {
		add(contracts.RecommendationRolloutBlockedFingerprintChanged)
	}
	if value.FactVersion != state.FactVersion {
		add(contracts.RecommendationRolloutBlockedFactVersionChanged)
	}
	if !sameIDs(value.EvidenceIDs, state.EvidenceIDs) {
		add(contracts.RecommendationRolloutBlockedEvidenceChanged)
	}
	if value.SchedulingGeneration != state.SchedulingGeneration {
		add(contracts.RecommendationRolloutBlockedGenerationChanged)
	}
	if value.RecommendationExpiresAt.IsZero() || !value.RecommendationExpiresAt.Equal(state.RecommendationExpiresAt) || !now.Before(value.RecommendationExpiresAt) {
		add(contracts.RecommendationRolloutBlockedRecommendationExpired)
	}
	if !freshWindow(value.EvidenceObservedAt, value.EvidenceFreshUntil, now) {
		add(contracts.RecommendationRolloutBlockedEvidenceStale)
	}

	seen := make(map[contracts.RecommendationRolloutGateKind]contracts.RecommendationRolloutGateStatus)
	for _, gate := range value.Gates {
		if !contracts.IsRecommendationRolloutGateKind(gate.Kind) || !contracts.IsRecommendationRolloutGateStatus(gate.Status) {
			add(contracts.RecommendationRolloutBlockedUnknownGate)
			continue
		}
		if _, exists := seen[gate.Kind]; exists {
			add(contracts.RecommendationRolloutBlockedDuplicateGate)
			failed = append(failed, gate.Kind)
			continue
		}
		seen[gate.Kind] = gate.Status
		if gate.Status == contracts.RecommendationRolloutGateBlocked {
			add(contracts.RecommendationRolloutBlockedGate)
			failed = append(failed, gate.Kind)
		} else if gate.Status == contracts.RecommendationRolloutGateUnknown {
			add(contracts.RecommendationRolloutBlockedUnknownGate)
			failed = append(failed, gate.Kind)
		}
	}
	for _, required := range contracts.RecommendationRolloutRequiredGates() {
		if _, exists := seen[required]; !exists {
			add(contracts.RecommendationRolloutBlockedMissingGate)
			failed = append(failed, required)
		}
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i] < failed[j] })
	return uniqueGates(failed)
}

func eventIdentityMatches(state contracts.RecommendationRolloutState, event contracts.RecommendationRolloutEvent, add func(contracts.RecommendationRolloutBlockReason)) bool {
	valid := true
	if event.RecommendationFingerprint != state.RecommendationFingerprint {
		add(contracts.RecommendationRolloutBlockedFingerprintChanged)
		valid = false
	}
	if event.FactVersion != state.FactVersion {
		add(contracts.RecommendationRolloutBlockedFactVersionChanged)
		valid = false
	}
	if event.SchedulingGeneration != state.SchedulingGeneration {
		add(contracts.RecommendationRolloutBlockedGenerationChanged)
		valid = false
	}
	return valid
}

func validAfterEvidence(state contracts.RecommendationRolloutState, after *contracts.RecommendationRolloutAfterEvidence, now time.Time) bool {
	return after != nil && after.Stage == state.Stage && after.RecommendationFingerprint == state.RecommendationFingerprint &&
		after.SchedulingGeneration == state.SchedulingGeneration && len(normalizeIDs(after.EvidenceIDs)) > 0 &&
		contracts.IsRecommendationRolloutGateStatus(after.Callability) && contracts.IsRecommendationRolloutGateStatus(after.Quality) &&
		freshWindow(after.ObservedAt, after.FreshUntil, now)
}

func validState(state contracts.RecommendationRolloutState) bool {
	if strings.TrimSpace(state.ID) == "" || state.UserID <= 0 || strings.TrimSpace(state.PlanID) == "" ||
		strings.TrimSpace(state.RecommendationID) == "" || strings.TrimSpace(state.RecommendationFingerprint) == "" ||
		state.FactVersion <= 0 || len(normalizeIDs(state.EvidenceIDs)) == 0 || strings.TrimSpace(state.BaselineFingerprint) == "" ||
		state.SchedulingGeneration <= 0 || !contracts.IsRecommendationRolloutStatus(state.Status) ||
		!contracts.IsRecommendationRolloutStage(state.Stage) || !contracts.IsRecommendationRolloutStage(state.PendingStage) ||
		state.ObservationSeconds <= 0 || state.ObservationSeconds > maxObservationSeconds || state.RecommendationExpiresAt.IsZero() ||
		state.StartedAt.IsZero() {
		return false
	}
	switch state.Status {
	case contracts.RecommendationRolloutApplying:
		return state.PendingStage == nextStage(state.Stage) && state.PendingStage != contracts.RecommendationRolloutStageNone && state.ObserveUntil == nil
	case contracts.RecommendationRolloutObserving:
		return state.PendingStage == contracts.RecommendationRolloutStageNone && state.Stage != contracts.RecommendationRolloutStageNone && state.StageStartedAt != nil && state.ObserveUntil != nil && state.ObserveUntil.After(*state.StageStartedAt)
	case contracts.RecommendationRolloutCompleted:
		return state.Stage == contracts.RecommendationRolloutStage100 && state.PendingStage == contracts.RecommendationRolloutStageNone
	case contracts.RecommendationRolloutRolledBack:
		return state.Stage == contracts.RecommendationRolloutStageNone && state.PendingStage == contracts.RecommendationRolloutStageNone
	case contracts.RecommendationRolloutRollbackRequired, contracts.RecommendationRolloutBlocked, contracts.RecommendationRolloutReady:
		return state.PendingStage == contracts.RecommendationRolloutStageNone
	default:
		return false
	}
}

func validEvent(event contracts.RecommendationRolloutEvent) bool {
	return contracts.IsRecommendationRolloutEventType(event.Type) && event.UserID > 0 && strings.TrimSpace(event.PlanID) != "" &&
		strings.TrimSpace(event.RecommendationID) != "" && !event.Now.IsZero()
}

func nextStage(current contracts.RecommendationRolloutStage) contracts.RecommendationRolloutStage {
	switch current {
	case contracts.RecommendationRolloutStageNone:
		return contracts.RecommendationRolloutStage10
	case contracts.RecommendationRolloutStage10:
		return contracts.RecommendationRolloutStage25
	case contracts.RecommendationRolloutStage25:
		return contracts.RecommendationRolloutStage50
	case contracts.RecommendationRolloutStage50:
		return contracts.RecommendationRolloutStage100
	default:
		return contracts.RecommendationRolloutStageNone
	}
}

func block(decision *contracts.RecommendationRolloutDecision, now time.Time) contracts.RecommendationRolloutDecision {
	decision.State.Status = contracts.RecommendationRolloutBlocked
	if !now.IsZero() {
		decision.State.UpdatedAt = now.UTC()
	}
	sortReasons(decision)
	return *decision
}

func rollbackRequired(decision *contracts.RecommendationRolloutDecision, now time.Time) contracts.RecommendationRolloutDecision {
	decision.State.Status = contracts.RecommendationRolloutRollbackRequired
	decision.State.PendingStage = contracts.RecommendationRolloutStageNone
	decision.State.ObserveUntil = nil
	decision.State.RollbackReasons = appendUniqueReasons(decision.State.RollbackReasons, decision.Reasons...)
	decision.State.UpdatedAt = now.UTC()
	decision.Action = rollbackAction(decision.State)
	sortReasons(decision)
	return *decision
}

func stageAction(state contracts.RecommendationRolloutState, kind contracts.RecommendationRolloutActionKind, target contracts.RecommendationRolloutStage) contracts.RecommendationRolloutAction {
	return contracts.RecommendationRolloutAction{
		Kind: kind, TargetStage: target, ExpectedSchedulingGeneration: state.SchedulingGeneration,
		RecommendationFingerprint: state.RecommendationFingerprint,
	}
}

func rollbackAction(state contracts.RecommendationRolloutState) contracts.RecommendationRolloutAction {
	action := stageAction(state, contracts.RecommendationRolloutActionPlanRollback, contracts.RecommendationRolloutStageNone)
	action.BaselineFingerprint = state.BaselineFingerprint
	return action
}

func freshWindow(observedAt, freshUntil, now time.Time) bool {
	return !now.IsZero() && !observedAt.IsZero() && !freshUntil.IsZero() && !freshUntil.Before(observedAt) && !now.Before(observedAt) && now.Before(freshUntil)
}

func normalizeIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameIDs(left, right []string) bool {
	l, r := normalizeIDs(left), normalizeIDs(right)
	if len(l) != len(r) || len(l) != len(left) || len(r) != len(right) {
		return false
	}
	for index := range l {
		if l[index] != r[index] {
			return false
		}
	}
	return true
}

func uniqueGates(values []contracts.RecommendationRolloutGateKind) []contracts.RecommendationRolloutGateKind {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func appendUniqueReasons(target []contracts.RecommendationRolloutBlockReason, values ...contracts.RecommendationRolloutBlockReason) []contracts.RecommendationRolloutBlockReason {
	for _, value := range values {
		found := false
		for _, existing := range target {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			target = append(target, value)
		}
	}
	sort.Slice(target, func(i, j int) bool { return target[i] < target[j] })
	return target
}

func sortReasons(decision *contracts.RecommendationRolloutDecision) {
	sort.Slice(decision.Reasons, func(i, j int) bool { return decision.Reasons[i] < decision.Reasons[j] })
}

func cloneState(state contracts.RecommendationRolloutState) contracts.RecommendationRolloutState {
	state.EvidenceIDs = append([]string(nil), state.EvidenceIDs...)
	state.RollbackReasons = append([]contracts.RecommendationRolloutBlockReason(nil), state.RollbackReasons...)
	state.StageStartedAt = cloneTime(state.StageStartedAt)
	state.ObserveUntil = cloneTime(state.ObserveUntil)
	state.LastAfterEvidence = cloneAfterEvidence(state.LastAfterEvidence)
	return state
}

func cloneAfterEvidence(value *contracts.RecommendationRolloutAfterEvidence) *contracts.RecommendationRolloutAfterEvidence {
	if value == nil {
		return nil
	}
	copyValue := *value
	copyValue.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	return &copyValue
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
