package recommendationrollout

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

const (
	DefaultWorkerInterval = time.Second
	// One convergence step can contain one Connector mutation followed by one
	// full read-back. Each synchronous Connector call is bounded at 30 seconds,
	// so keep enough ownership margin for store renewals and scheduling jitter.
	DefaultWorkerLease = 2 * time.Minute
)

// Gateway is the exact numerical scheduling surface. Orchestrator satisfies
// it and retains capability checks, managed-account fencing and L1 audit.
type Gateway interface {
	ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error)
	SetTrafficShare(context.Context, string, string, int, string) error
}

// Revalidator produces current gate/after evidence. Rollback intentionally
// does not call it: stale recommendations, kill switch, or stale intelligence
// must never prevent restoration of a persisted exact baseline.
type Revalidator interface {
	Revalidate(context.Context, contracts.RecommendationRollout) (contracts.RecommendationRolloutRevalidation, error)
}

// Worker claims durable operations and converges the entire account-weight
// set. A restart always reads current gateway state first, so a crash between
// two writes resumes convergence instead of assuming the first write failed.
type Worker struct {
	store       store.RecommendationRolloutStore
	gateway     Gateway
	revalidator Revalidator
	workerID    string
	interval    time.Duration
	lease       time.Duration
	now         func() time.Time
}

func NewWorker(st store.RecommendationRolloutStore, gateway Gateway, revalidator Revalidator, workerID string, interval, lease time.Duration) (*Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if st == nil || gateway == nil || revalidator == nil || workerID == "" || len(workerID) > 128 || contracts.LooksLikeConnectorSensitiveValue(workerID) {
		return nil, errors.New("recommendation rollout worker: invalid dependency or worker id")
	}
	if interval <= 0 {
		interval = DefaultWorkerInterval
	}
	if lease <= 0 {
		lease = DefaultWorkerLease
	}
	return &Worker{store: st, gateway: gateway, revalidator: revalidator, workerID: workerID, interval: interval, lease: lease, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	w.RunOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	for ctx.Err() == nil {
		rollout, operation, claimed, err := w.store.ClaimRecommendationRolloutOperation(ctx, w.workerID, w.lease)
		if err != nil {
			log.Printf("recommendation rollout worker: claim failed: %v", err)
			return
		}
		if !claimed {
			return
		}
		w.process(ctx, rollout, operation)
	}
}

func (w *Worker) process(ctx context.Context, rollout contracts.RecommendationRollout, operation contracts.RecommendationRolloutOperation) {
	operationVersion := operation.Version
	renew := func() error {
		owned, err := w.store.RenewRecommendationRolloutOperation(ctx, operation.ID, w.workerID, operationVersion, w.lease)
		if err != nil {
			return err
		}
		operationVersion = owned.Version
		return nil
	}

	current, readCode := w.readExactWeights(ctx, rollout)
	if readCode != "" {
		w.completeFailure(ctx, rollout, operation, operationVersion, readCode)
		return
	}
	target, err := targetWeights(rollout, operation)
	if err != nil {
		w.completeFailure(ctx, rollout, operation, operationVersion, contracts.RecommendationRolloutOperationErrorBaselineChanged)
		return
	}
	if operation.Action == contracts.RecommendationRolloutOperationApplyStage {
		if err := renew(); err != nil {
			return
		}
		// This check is deliberately adjacent to the first possible forward
		// write. The controller's earlier validation is not a side-effect fence:
		// price, quality, authorization, or the kill switch can change while an
		// operation waits for a worker. Rollback never enters this branch.
		revalidation, err := w.revalidator.Revalidate(ctx, rollout)
		if err != nil || !revalidationAllowsForward(rollout, revalidation, w.now().UTC()) {
			w.completeFailure(ctx, rollout, operation, operationVersion, contracts.RecommendationRolloutOperationErrorRevalidationBlocked)
			return
		}
	}
	if err := renew(); err != nil {
		return
	}
	if !sameWeightSet(current, target) {
		fenced := contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/" + rollout.State.PlanID, Version: rollout.State.SchedulingGeneration,
		})
		for _, desired := range target {
			if currentWeight, ok := weightFor(current, desired.AccountID); ok && currentWeight == desired.Weight {
				continue
			}
			if err := renew(); err != nil {
				return
			}
			if err := w.gateway.SetTrafficShare(fenced, rollout.InstanceID, desired.AccountID, desired.Weight, rolloutReason(operation)); err != nil {
				w.completeFailure(ctx, rollout, operation, operationVersion, contracts.RecommendationRolloutOperationErrorWriteFailed)
				return
			}
			// Read back the full set after every single write. This makes a crash
			// between accounts recoverable and never coerces an unknown into zero.
			current, readCode = w.readExactWeights(fenced, rollout)
			if readCode != "" {
				w.completeFailure(ctx, rollout, operation, operationVersion, readbackErrorCode(readCode))
				return
			}
		}
	}
	if err := renew(); err != nil {
		return
	}
	verified, readCode := w.readExactWeights(ctx, rollout)
	if readCode != "" || !sameWeightSet(verified, target) {
		w.completeFailure(ctx, rollout, operation, operationVersion, contracts.RecommendationRolloutOperationErrorVerificationFailed)
		return
	}

	next, errorCode := w.successState(rollout, operation, verified)
	if errorCode != "" {
		w.completeFailure(ctx, rollout, operation, operationVersion, errorCode)
		return
	}
	_, _, err = w.store.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: w.workerID, ExpectedOperationVersion: operationVersion,
		ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationSucceeded,
		NextState: next,
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("recommendation rollout worker: record success failed: %v", err)
	}
}

func (w *Worker) successState(rollout contracts.RecommendationRollout, operation contracts.RecommendationRolloutOperation, verified []contracts.RecommendationRolloutAccountWeight) (contracts.RecommendationRolloutState, contracts.RecommendationRolloutOperationErrorCode) {
	now := w.now().UTC()
	event := contracts.RecommendationRolloutEvent{
		UserID: rollout.State.UserID, PlanID: rollout.State.PlanID, RecommendationID: rollout.State.RecommendationID,
		Now: now, RecommendationFingerprint: rollout.State.RecommendationFingerprint, FactVersion: rollout.State.FactVersion,
		SchedulingGeneration: rollout.State.SchedulingGeneration,
	}
	if operation.Action == contracts.RecommendationRolloutOperationRollback {
		// This proof is derived only after Worker has read the complete gateway
		// account set and compared it with the persisted baseline above. The
		// fingerprint is content-addressed evidence, never a synthetic probe ID.
		fingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(verified)
		if err != nil || fingerprint != rollout.State.BaselineFingerprint {
			return rollout.State, contracts.RecommendationRolloutOperationErrorVerificationFailed
		}
		after := contracts.RecommendationRolloutAfterEvidence{
			Stage: contracts.RecommendationRolloutStageNone, RecommendationFingerprint: rollout.State.RecommendationFingerprint,
			BaselineFingerprint: fingerprint, SchedulingGeneration: rollout.State.SchedulingGeneration,
			EvidenceIDs: []string{"weight-set-sha256:" + fingerprint}, ObservedAt: now,
			FreshUntil: now.Add(time.Minute), Callability: contracts.RecommendationRolloutGateUnknown, Quality: contracts.RecommendationRolloutGateUnknown,
		}
		event.Type, event.AppliedStage, event.AfterEvidence = contracts.RecommendationRolloutEventRollbackApplied, contracts.RecommendationRolloutStageNone, &after
	} else {
		event.Type, event.AppliedStage = contracts.RecommendationRolloutEventStageApplied, operation.TargetStage
	}
	decision := Advance(rollout.State, event)
	if len(decision.Reasons) != 0 || operation.Action == contracts.RecommendationRolloutOperationRollback && decision.State.Status != contracts.RecommendationRolloutRolledBack ||
		operation.Action == contracts.RecommendationRolloutOperationApplyStage && decision.State.Status != contracts.RecommendationRolloutObserving {
		return rollout.State, contracts.RecommendationRolloutOperationErrorInternal
	}
	return decision.State, ""
}

func (w *Worker) completeFailure(ctx context.Context, rollout contracts.RecommendationRollout, operation contracts.RecommendationRolloutOperation, operationVersion int64, code contracts.RecommendationRolloutOperationErrorCode) {
	now := w.now().UTC()
	eventType := contracts.RecommendationRolloutEventStageApplyFailed
	if operation.Action == contracts.RecommendationRolloutOperationRollback {
		eventType = contracts.RecommendationRolloutEventRollbackFailed
	}
	event := contracts.RecommendationRolloutEvent{
		Type: eventType, UserID: rollout.State.UserID, PlanID: rollout.State.PlanID, RecommendationID: rollout.State.RecommendationID,
		Now: now, AppliedStage: operation.TargetStage, RecommendationFingerprint: rollout.State.RecommendationFingerprint,
		FactVersion: rollout.State.FactVersion, SchedulingGeneration: rollout.State.SchedulingGeneration,
	}
	decision := Advance(rollout.State, event)
	if decision.State.Status != contracts.RecommendationRolloutRollbackRequired {
		decision.State = rollout.State
		decision.State.Status = contracts.RecommendationRolloutRollbackRequired
		decision.State.PendingStage = contracts.RecommendationRolloutStageNone
		decision.State.ObserveUntil = nil
		decision.State.UpdatedAt = now
	}
	_, _, err := w.store.CompleteRecommendationRolloutOperation(ctx, contracts.RecommendationRolloutCompletion{
		OperationID: operation.ID, WorkerID: w.workerID, ExpectedOperationVersion: operationVersion,
		ExpectedRolloutVersion: rollout.Version, OperationStatus: contracts.RecommendationRolloutOperationFailed,
		ErrorCode: code, NextState: decision.State,
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("recommendation rollout worker: record failure failed: %v", err)
	}
}

func (w *Worker) readExactWeights(ctx context.Context, rollout contracts.RecommendationRollout) ([]contracts.RecommendationRolloutAccountWeight, contracts.RecommendationRolloutOperationErrorCode) {
	accounts, err := w.gateway.ListAccounts(ctx, rollout.InstanceID)
	if err != nil {
		return nil, contracts.RecommendationRolloutOperationErrorGatewayUnavailable
	}
	if len(accounts) == 0 {
		return nil, contracts.RecommendationRolloutOperationErrorBaselineChanged
	}
	weights := make([]contracts.RecommendationRolloutAccountWeight, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		id := strings.TrimSpace(account.ID)
		if id == "" || account.CurrentWeight == nil || *account.CurrentWeight < 0 || *account.CurrentWeight > 100 {
			return nil, contracts.RecommendationRolloutOperationErrorWeightUnknown
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, contracts.RecommendationRolloutOperationErrorBaselineChanged
		}
		seen[id] = struct{}{}
		weights = append(weights, contracts.RecommendationRolloutAccountWeight{AccountID: id, Weight: *account.CurrentWeight})
	}
	canonical, err := canonicalObservedWeights(weights)
	if err != nil {
		return nil, contracts.RecommendationRolloutOperationErrorWeightUnknown
	}
	// Exact set equality is critical: a new account discovered after start is
	// unknown intent, never an implicit zero.
	if len(canonical) != len(rollout.BaselineWeights) {
		return nil, contracts.RecommendationRolloutOperationErrorBaselineChanged
	}
	for _, baseline := range rollout.BaselineWeights {
		if _, ok := seen[baseline.AccountID]; !ok {
			return nil, contracts.RecommendationRolloutOperationErrorBaselineChanged
		}
	}
	return canonical, ""
}

func targetWeights(rollout contracts.RecommendationRollout, operation contracts.RecommendationRolloutOperation) ([]contracts.RecommendationRolloutAccountWeight, error) {
	baseline, err := contracts.CanonicalRecommendationRolloutWeights(rollout.BaselineWeights)
	if err != nil {
		return nil, err
	}
	if operation.Action == contracts.RecommendationRolloutOperationRollback {
		return baseline, nil
	}
	if operation.Action != contracts.RecommendationRolloutOperationApplyStage || operation.TargetStage != 10 && operation.TargetStage != 25 && operation.TargetStage != 50 && operation.TargetStage != 100 {
		return nil, errors.New("invalid stage action")
	}
	foundFrom, foundTo := false, false
	target := make([]contracts.RecommendationRolloutAccountWeight, 0, len(baseline))
	baselineFrom, baselineTo := 0, 0
	for _, value := range baseline {
		switch value.AccountID {
		case rollout.FromAccountID:
			baselineFrom, foundFrom = value.Weight, true
		case rollout.ToAccountID:
			baselineTo, foundTo = value.Weight, true
		}
	}
	if !foundFrom || !foundTo || baselineFrom <= 0 {
		return nil, errors.New("rollout pair cannot fund target stage")
	}
	// A rollout stage is the percentage of the original source weight moved to
	// the destination. The shared helper keeps Start's representability check
	// and Worker execution on exactly the same integer rounding rule.
	moved, err := sourceBaselineMoved(baselineFrom, operation.TargetStage)
	if err != nil || moved > baselineFrom || baselineTo+moved > 100 {
		return nil, errors.New("rollout pair cannot fund target stage")
	}
	for _, value := range baseline {
		weight := value.Weight
		switch value.AccountID {
		case rollout.FromAccountID:
			weight = baselineFrom - moved
		case rollout.ToAccountID:
			weight = baselineTo + moved
		}
		target = append(target, contracts.RecommendationRolloutAccountWeight{AccountID: value.AccountID, Weight: weight})
	}
	return contracts.CanonicalRecommendationRolloutWeights(target)
}

// Gateway writes are applied one account at a time, so an intermediate
// read-back can legitimately total less or more than 100. We still require a
// complete, unique, bounded set and reserve the total=100 invariant for the
// persisted baseline and final target.
func canonicalObservedWeights(values []contracts.RecommendationRolloutAccountWeight) ([]contracts.RecommendationRolloutAccountWeight, error) {
	if len(values) < 2 || len(values) > 4096 {
		return nil, errors.New("invalid account count")
	}
	out := append([]contracts.RecommendationRolloutAccountWeight(nil), values...)
	for index := range out {
		out[index].AccountID = strings.TrimSpace(out[index].AccountID)
		if out[index].AccountID == "" || len(out[index].AccountID) > 256 ||
			contracts.LooksLikeConnectorSensitiveValue(out[index].AccountID) || out[index].Weight < 0 || out[index].Weight > 100 {
			return nil, errors.New("invalid observed weight")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	for index := 1; index < len(out); index++ {
		if out[index-1].AccountID == out[index].AccountID {
			return nil, errors.New("duplicate observed account")
		}
	}
	return out, nil
}

func revalidationAllowsForward(rollout contracts.RecommendationRollout, value contracts.RecommendationRolloutRevalidation, now time.Time) bool {
	if value.UserID != rollout.State.UserID || value.PlanID != rollout.State.PlanID || value.RecommendationID != rollout.State.RecommendationID ||
		value.RecommendationFingerprint != rollout.State.RecommendationFingerprint || value.FactVersion != rollout.State.FactVersion ||
		value.SchedulingGeneration != rollout.State.SchedulingGeneration || !value.RecommendationExpiresAt.Equal(rollout.State.RecommendationExpiresAt) ||
		!now.Before(value.RecommendationExpiresAt) || value.EvidenceObservedAt.IsZero() || value.EvidenceFreshUntil.IsZero() ||
		now.Before(value.EvidenceObservedAt) || !now.Before(value.EvidenceFreshUntil) || !sameNormalizedIDs(value.EvidenceIDs, rollout.State.EvidenceIDs) {
		return false
	}
	seen := make(map[contracts.RecommendationRolloutGateKind]struct{}, len(value.Gates))
	for _, gate := range value.Gates {
		if !contracts.IsRecommendationRolloutGateKind(gate.Kind) || gate.Status != contracts.RecommendationRolloutGatePassed {
			return false
		}
		if _, duplicate := seen[gate.Kind]; duplicate {
			return false
		}
		seen[gate.Kind] = struct{}{}
	}
	for _, required := range contracts.RecommendationRolloutRequiredGates() {
		if _, ok := seen[required]; !ok {
			return false
		}
	}
	return true
}

func sameNormalizedIDs(left, right []string) bool {
	normalize := func(values []string) ([]string, bool) {
		result := make([]string, 0, len(values))
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil, false
			}
			if _, duplicate := seen[value]; duplicate {
				return nil, false
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
		sort.Strings(result)
		return result, true
	}
	a, aOK := normalize(left)
	b, bOK := normalize(right)
	if !aOK || !bOK || len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func readbackErrorCode(code contracts.RecommendationRolloutOperationErrorCode) contracts.RecommendationRolloutOperationErrorCode {
	if code == contracts.RecommendationRolloutOperationErrorBaselineChanged || code == contracts.RecommendationRolloutOperationErrorWeightUnknown {
		return code
	}
	return contracts.RecommendationRolloutOperationErrorReadbackFailed
}

func sameWeightSet(left, right []contracts.RecommendationRolloutAccountWeight) bool {
	if len(left) != len(right) {
		return false
	}
	left, leftErr := contracts.CanonicalRecommendationRolloutWeights(left)
	right, rightErr := contracts.CanonicalRecommendationRolloutWeights(right)
	if leftErr != nil || rightErr != nil || len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func weightFor(values []contracts.RecommendationRolloutAccountWeight, accountID string) (int, bool) {
	index := sort.Search(len(values), func(index int) bool { return values[index].AccountID >= accountID })
	if index < len(values) && values[index].AccountID == accountID {
		return values[index].Weight, true
	}
	return 0, false
}

func rolloutReason(operation contracts.RecommendationRolloutOperation) string {
	return fmt.Sprintf("recommendation-rollout:%s:%s:%d", operation.RolloutID, operation.Action, operation.TargetStage)
}
