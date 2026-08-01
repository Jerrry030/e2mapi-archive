package recommendationrollout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type workerStoreFixture struct {
	mu          sync.Mutex
	rollout     contracts.RecommendationRollout
	operation   contracts.RecommendationRolloutOperation
	claimed     bool
	renewCalls  int
	failRenewAt int
	completion  *contracts.RecommendationRolloutCompletion
}

func (s *workerStoreFixture) CreateRecommendationRollout(context.Context, contracts.RecommendationRolloutCreate) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, errors.New("unused")
}
func (s *workerStoreFixture) GetRecommendationRollout(context.Context, string) (contracts.RecommendationRollout, error) {
	return s.rollout, nil
}
func (s *workerStoreFixture) ListRecommendationRollouts(context.Context, contracts.RecommendationRolloutFilter) ([]contracts.RecommendationRollout, error) {
	return nil, nil
}
func (s *workerStoreFixture) ListRecommendationRolloutOperations(context.Context, string) ([]contracts.RecommendationRolloutOperation, error) {
	return nil, nil
}
func (s *workerStoreFixture) TransitionRecommendationRolloutState(context.Context, string, int64, contracts.RecommendationRolloutState) (contracts.RecommendationRollout, error) {
	return contracts.RecommendationRollout{}, errors.New("unused")
}
func (s *workerStoreFixture) EnqueueRecommendationRolloutOperation(context.Context, string, int64, contracts.RecommendationRolloutState, contracts.RecommendationRolloutOperationAction, contracts.RecommendationRolloutStage) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, errors.New("unused")
}
func (s *workerStoreFixture) ClaimRecommendationRolloutOperation(context.Context, string, time.Duration) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return contracts.RecommendationRollout{}, contracts.RecommendationRolloutOperation{}, false, nil
	}
	s.claimed = true
	return cloneWorkerRollout(s.rollout), s.operation, true, nil
}
func (s *workerStoreFixture) RenewRecommendationRolloutOperation(_ context.Context, _ string, _ string, expected int64, _ time.Duration) (contracts.RecommendationRolloutOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewCalls++
	if s.failRenewAt > 0 && s.renewCalls >= s.failRenewAt {
		return contracts.RecommendationRolloutOperation{}, store.ErrConflict
	}
	s.operation.Version = expected + 1
	return s.operation, nil
}
func (s *workerStoreFixture) CompleteRecommendationRolloutOperation(_ context.Context, input contracts.RecommendationRolloutCompletion) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyInput := input
	s.completion = &copyInput
	return s.rollout, s.operation, nil
}

type workerGatewayFixture struct {
	mu          sync.Mutex
	weights     []contracts.RecommendationRolloutAccountWeight
	listErr     error
	listHook    func(*workerGatewayFixture)
	writeErrAt  int
	writes      []workerWrite
	listCalls   int
	wrongReadAt int
}

type workerWrite struct {
	AccountID string
	Weight    int
	Fence     contracts.GatewaySchedulingFence
}

func (g *workerGatewayFixture) ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listCalls++
	if g.listHook != nil {
		g.listHook(g)
	}
	if g.listErr != nil {
		return nil, g.listErr
	}
	accounts := make([]contracts.GatewayAccount, 0, len(g.weights))
	for _, value := range g.weights {
		weight := value.Weight
		accounts = append(accounts, contracts.GatewayAccount{ID: value.AccountID, CurrentWeight: &weight})
	}
	return accounts, nil
}
func (g *workerGatewayFixture) SetTrafficShare(ctx context.Context, _ string, accountID string, weight int, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	fence, _ := contracts.GatewaySchedulingFenceFromContext(ctx)
	g.writes = append(g.writes, workerWrite{AccountID: accountID, Weight: weight, Fence: fence})
	if g.writeErrAt > 0 && len(g.writes) == g.writeErrAt {
		return errors.New("gateway write failed")
	}
	for index := range g.weights {
		if g.weights[index].AccountID == accountID {
			g.weights[index].Weight = weight
			return nil
		}
	}
	return errors.New("unknown account")
}

type workerRevalidatorFixture struct {
	value          contracts.RecommendationRolloutRevalidation
	err            error
	revalidateCall int
}

func (r *workerRevalidatorFixture) Revalidate(context.Context, contracts.RecommendationRollout) (contracts.RecommendationRolloutRevalidation, error) {
	r.revalidateCall++
	return r.value, r.err
}
func TestWorkerAppliesEveryStageAndRetainsExplicitZero(t *testing.T) {
	for _, stage := range []contracts.RecommendationRolloutStage{10, 25, 50, 100} {
		t.Run(stageName(stage), func(t *testing.T) {
			rollout, operation, revalidation := workerFixture(stage, contracts.RecommendationRolloutOperationApplyStage)
			gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
			st, validator := runWorkerFixture(t, rollout, operation, gateway, revalidation)
			assertWorkerSucceeded(t, st)
			moved := (90*int(stage) + 50) / 100
			want := []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 90 - moved}, {AccountID: "idle", Weight: 0}, {AccountID: "to", Weight: 10 + moved}}
			if !sameWeightSet(gateway.weights, want) {
				t.Fatalf("stage %d weights=%v want=%v", stage, gateway.weights, want)
			}
			if got, ok := weightFor(gateway.weights, "idle"); !ok || got != 0 {
				t.Fatalf("unrelated baseline weight lost: %v", gateway.weights)
			}
			if validator.revalidateCall != 1 {
				t.Fatalf("forward revalidation calls=%d", validator.revalidateCall)
			}
			for _, write := range gateway.writes {
				if write.Fence.Scope != "auto-switch/plan/plan-1" || write.Fence.Version != rollout.State.SchedulingGeneration {
					t.Fatalf("write fence=%+v", write.Fence)
				}
			}
		})
	}
}

func TestWorkerPreservesNonZeroUnrelatedBaseline(t *testing.T) {
	rollout, operation, revalidation := workerFixture(50, contracts.RecommendationRolloutOperationApplyStage)
	rollout.BaselineWeights = []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 70}, {AccountID: "idle", Weight: 20}, {AccountID: "to", Weight: 10}}
	gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
	assertWorkerSucceeded(t, st)
	want := []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 35}, {AccountID: "idle", Weight: 20}, {AccountID: "to", Weight: 45}}
	if !sameWeightSet(gateway.weights, want) {
		t.Fatalf("weights=%v want=%v", gateway.weights, want)
	}
}

func TestWorkerPairPercentageStagesPreserveUnrelatedAndDrainSourceAtHundred(t *testing.T) {
	baseline := []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 55}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 15}}
	wantByStage := map[contracts.RecommendationRolloutStage][]contracts.RecommendationRolloutAccountWeight{
		10:  {{AccountID: "from", Weight: 49}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 21}},
		25:  {{AccountID: "from", Weight: 41}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 29}},
		50:  {{AccountID: "from", Weight: 27}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 43}},
		100: {{AccountID: "from", Weight: 0}, {AccountID: "idle", Weight: 30}, {AccountID: "to", Weight: 70}},
	}
	for _, stage := range []contracts.RecommendationRolloutStage{10, 25, 50, 100} {
		t.Run(stageName(stage), func(t *testing.T) {
			rollout, operation, revalidation := workerFixture(stage, contracts.RecommendationRolloutOperationApplyStage)
			rollout.BaselineWeights = cloneWorkerWeights(baseline)
			gateway := &workerGatewayFixture{weights: cloneWorkerWeights(baseline)}
			st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
			assertWorkerSucceeded(t, st)
			if !sameWeightSet(gateway.weights, wantByStage[stage]) {
				t.Fatalf("stage=%d weights=%v want=%v", stage, gateway.weights, wantByStage[stage])
			}
		})
	}
}

func TestWorkerRetainsExplicitZeroAndAllowsIntermediateNonHundredReadback(t *testing.T) {
	rollout, operation, revalidation := workerFixture(25, contracts.RecommendationRolloutOperationApplyStage)
	rollout.BaselineWeights = []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 80}, {AccountID: "idle", Weight: 0}, {AccountID: "to", Weight: 20}}
	gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
	assertWorkerSucceeded(t, st)
	if got, ok := weightFor(gateway.weights, "idle"); !ok || got != 0 {
		t.Fatalf("explicit zero missing: %v", gateway.weights)
	}
	if len(gateway.writes) != 2 {
		t.Fatalf("writes=%v; expected two-account convergence through a non-100 intermediate set", gateway.writes)
	}
}

func TestWorkerClassifiesInitialInventoryFailures(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workerGatewayFixture)
		want contracts.RecommendationRolloutOperationErrorCode
	}{
		{"gateway unavailable", func(g *workerGatewayFixture) { g.listErr = errors.New("offline") }, contracts.RecommendationRolloutOperationErrorGatewayUnavailable},
		{"new account", func(g *workerGatewayFixture) {
			g.weights = append(g.weights, contracts.RecommendationRolloutAccountWeight{AccountID: "new", Weight: 0})
		}, contracts.RecommendationRolloutOperationErrorBaselineChanged},
		{"missing account", func(g *workerGatewayFixture) { g.weights = g.weights[:2] }, contracts.RecommendationRolloutOperationErrorBaselineChanged},
		{"duplicate account", func(g *workerGatewayFixture) { g.weights = append(g.weights, g.weights[0]) }, contracts.RecommendationRolloutOperationErrorBaselineChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rollout, operation, revalidation := workerFixture(10, contracts.RecommendationRolloutOperationApplyStage)
			gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
			test.edit(gateway)
			st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
			if st.completion == nil || st.completion.OperationStatus != contracts.RecommendationRolloutOperationFailed || st.completion.ErrorCode != test.want {
				t.Fatalf("completion=%+v want=%s", st.completion, test.want)
			}
			if len(gateway.writes) != 0 {
				t.Fatalf("invalid inventory dispatched writes=%v", gateway.writes)
			}
		})
	}
}

func TestWorkerRejectsNilWeight(t *testing.T) {
	rollout, operation, revalidation := workerFixture(10, contracts.RecommendationRolloutOperationApplyStage)
	gateway := &nilWeightGateway{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	st := &workerStoreFixture{rollout: rollout, operation: operation}
	validator := &workerRevalidatorFixture{value: revalidation}
	worker, err := NewWorker(st, gateway, validator, "worker-1", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return workerNow() }
	worker.RunOnce(context.Background())
	if st.completion == nil || st.completion.ErrorCode != contracts.RecommendationRolloutOperationErrorWeightUnknown || len(gateway.writes) != 0 {
		t.Fatalf("completion=%+v writes=%v", st.completion, gateway.writes)
	}
}

type nilWeightGateway struct {
	weights []contracts.RecommendationRolloutAccountWeight
	writes  []workerWrite
}

func (g *nilWeightGateway) ListAccounts(context.Context, string) ([]contracts.GatewayAccount, error) {
	accounts := make([]contracts.GatewayAccount, 0, len(g.weights))
	for index, value := range g.weights {
		var weight *int
		if index != 0 {
			copyWeight := value.Weight
			weight = &copyWeight
		}
		accounts = append(accounts, contracts.GatewayAccount{ID: value.AccountID, CurrentWeight: weight})
	}
	return accounts, nil
}
func (g *nilWeightGateway) SetTrafficShare(context.Context, string, string, int, string) error {
	g.writes = append(g.writes, workerWrite{})
	return nil
}

func TestWorkerLostLeaseStopsBeforeNextSideEffect(t *testing.T) {
	rollout, operation, revalidation := workerFixture(25, contracts.RecommendationRolloutOperationApplyStage)
	gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	st := &workerStoreFixture{rollout: rollout, operation: operation, failRenewAt: 4}
	runWorker(t, st, gateway, &workerRevalidatorFixture{value: revalidation})
	if len(gateway.writes) != 1 || st.completion != nil {
		t.Fatalf("writes=%v completion=%+v; stale lease must stop without another side effect or stale completion", gateway.writes, st.completion)
	}
}

func TestWorkerPartialConvergenceIsReclaimedAndCompleted(t *testing.T) {
	rollout, operation, revalidation := workerFixture(25, contracts.RecommendationRolloutOperationApplyStage)
	gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	first := &workerStoreFixture{rollout: rollout, operation: operation, failRenewAt: 4}
	runWorker(t, first, gateway, &workerRevalidatorFixture{value: revalidation})
	if len(gateway.writes) != 1 {
		t.Fatalf("first worker writes=%v", gateway.writes)
	}
	second := &workerStoreFixture{rollout: rollout, operation: operation}
	runWorker(t, second, gateway, &workerRevalidatorFixture{value: revalidation})
	assertWorkerSucceeded(t, second)
	if len(gateway.writes) != 2 {
		t.Fatalf("reclaim should only dispatch remaining write: %v", gateway.writes)
	}
}

func TestWorkerWriteAndReadbackFailuresRequireRollback(t *testing.T) {
	t.Run("write failure", func(t *testing.T) {
		rollout, operation, revalidation := workerFixture(25, contracts.RecommendationRolloutOperationApplyStage)
		gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights), writeErrAt: 1}
		st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
		assertWorkerFailure(t, st, contracts.RecommendationRolloutOperationErrorWriteFailed)
	})
	t.Run("readback mismatch", func(t *testing.T) {
		rollout, operation, revalidation := workerFixture(25, contracts.RecommendationRolloutOperationApplyStage)
		gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
		gateway.listHook = func(g *workerGatewayFixture) {
			if g.listCalls >= 2 {
				// Undo every successful write before its full-set read-back.
				g.weights = cloneWorkerWeights(rollout.BaselineWeights)
			}
		}
		st, _ := runWorkerFixture(t, rollout, operation, gateway, revalidation)
		assertWorkerFailure(t, st, contracts.RecommendationRolloutOperationErrorVerificationFailed)
	})
}

func TestWorkerForwardRevalidationBlocksBeforeWrite(t *testing.T) {
	rollout, operation, revalidation := workerFixture(10, contracts.RecommendationRolloutOperationApplyStage)
	revalidation.Gates[0].Status = contracts.RecommendationRolloutGateBlocked
	gateway := &workerGatewayFixture{weights: cloneWorkerWeights(rollout.BaselineWeights)}
	st, validator := runWorkerFixture(t, rollout, operation, gateway, revalidation)
	assertWorkerFailure(t, st, contracts.RecommendationRolloutOperationErrorRevalidationBlocked)
	if validator.revalidateCall != 1 || len(gateway.writes) != 0 {
		t.Fatalf("revalidate=%d writes=%v", validator.revalidateCall, gateway.writes)
	}
}

func TestWorkerRollbackRestoresExactBaselineWithoutForwardGates(t *testing.T) {
	rollout, operation, _ := workerFixture(0, contracts.RecommendationRolloutOperationRollback)
	rollout.State.Status = contracts.RecommendationRolloutRollbackRequired
	rollout.State.Stage = contracts.RecommendationRolloutStage50
	rollout.State.PendingStage = contracts.RecommendationRolloutStageNone
	rollout.State.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedGate}
	baselineFingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(rollout.BaselineWeights)
	if err != nil {
		t.Fatal(err)
	}
	rollout.State.BaselineFingerprint = baselineFingerprint
	gateway := &workerGatewayFixture{weights: []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 30}, {AccountID: "idle", Weight: 20}, {AccountID: "to", Weight: 50}}}
	validator := &workerRevalidatorFixture{err: errors.New("stale recommendation / kill switch")}
	st := &workerStoreFixture{rollout: rollout, operation: operation}
	runWorker(t, st, gateway, validator)
	assertWorkerSucceeded(t, st)
	if !sameWeightSet(gateway.weights, rollout.BaselineWeights) {
		t.Fatalf("rollback weights=%v baseline=%v", gateway.weights, rollout.BaselineWeights)
	}
	if validator.revalidateCall != 0 {
		t.Fatalf("rollback revalidate=%d", validator.revalidateCall)
	}
	after := st.completion.NextState.LastAfterEvidence
	if after == nil || after.BaselineFingerprint != baselineFingerprint || len(after.EvidenceIDs) != 1 || after.EvidenceIDs[0] != "weight-set-sha256:"+baselineFingerprint {
		t.Fatalf("rollback read-back proof=%+v", after)
	}
}

func TestWorkerUsesClaimedRolloutGenerationAfterRollbackTakeover(t *testing.T) {
	rollout, operation, _ := workerFixture(0, contracts.RecommendationRolloutOperationRollback)
	rollout.State.Status = contracts.RecommendationRolloutRollbackRequired
	rollout.State.Stage = contracts.RecommendationRolloutStage25
	rollout.State.PendingStage = 0
	rollout.State.RollbackReasons = []contracts.RecommendationRolloutBlockReason{contracts.RecommendationRolloutBlockedGate}
	rollout.State.SchedulingGeneration = 77
	baselineFingerprint, err := contracts.RecommendationRolloutBaselineFingerprint(rollout.BaselineWeights)
	if err != nil {
		t.Fatal(err)
	}
	rollout.State.BaselineFingerprint = baselineFingerprint
	gateway := &workerGatewayFixture{weights: []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 65}, {AccountID: "idle", Weight: 20}, {AccountID: "to", Weight: 15}}}
	st := &workerStoreFixture{rollout: rollout, operation: operation}
	runWorker(t, st, gateway, &workerRevalidatorFixture{})
	assertWorkerSucceeded(t, st)
	for _, write := range gateway.writes {
		if write.Fence.Version != 77 {
			t.Fatalf("write used stale generation: %+v", write)
		}
	}
}

func workerFixture(stage contracts.RecommendationRolloutStage, action contracts.RecommendationRolloutOperationAction) (contracts.RecommendationRollout, contracts.RecommendationRolloutOperation, contracts.RecommendationRolloutRevalidation) {
	now := workerNow()
	currentStage := contracts.RecommendationRolloutStageNone
	switch stage {
	case contracts.RecommendationRolloutStage25:
		currentStage = contracts.RecommendationRolloutStage10
	case contracts.RecommendationRolloutStage50:
		currentStage = contracts.RecommendationRolloutStage25
	case contracts.RecommendationRolloutStage100:
		currentStage = contracts.RecommendationRolloutStage50
	}
	state := contracts.RecommendationRolloutState{
		ID: "rollout-1", UserID: 42, PlanID: "plan-1", RecommendationID: "rec-1",
		RecommendationFingerprint: "fingerprint", FactVersion: 7, EvidenceIDs: []string{"quality", "price"},
		BaselineFingerprint: "baseline", SchedulingGeneration: 17, Status: contracts.RecommendationRolloutApplying,
		Stage: currentStage, PendingStage: stage, ObservationSeconds: 60,
		RecommendationExpiresAt: now.Add(time.Hour), StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	rollout := contracts.RecommendationRollout{
		State: state, InstanceID: "instance-1", FromAccountID: "from", ToAccountID: "to", Version: 9,
		BaselineWeights: []contracts.RecommendationRolloutAccountWeight{{AccountID: "from", Weight: 90}, {AccountID: "idle", Weight: 0}, {AccountID: "to", Weight: 10}},
	}
	operation := contracts.RecommendationRolloutOperation{ID: "operation-1", RolloutID: state.ID, Action: action, TargetStage: stage, Version: 3}
	gates := make([]contracts.RecommendationRolloutGate, 0, len(contracts.RecommendationRolloutRequiredGates()))
	for _, kind := range contracts.RecommendationRolloutRequiredGates() {
		gates = append(gates, contracts.RecommendationRolloutGate{Kind: kind, Status: contracts.RecommendationRolloutGatePassed})
	}
	revalidation := contracts.RecommendationRolloutRevalidation{
		UserID: state.UserID, PlanID: state.PlanID, RecommendationID: state.RecommendationID,
		RecommendationFingerprint: state.RecommendationFingerprint, FactVersion: state.FactVersion,
		EvidenceIDs: append([]string(nil), state.EvidenceIDs...), EvidenceObservedAt: now.Add(-time.Minute), EvidenceFreshUntil: now.Add(time.Minute),
		RecommendationExpiresAt: state.RecommendationExpiresAt, SchedulingGeneration: state.SchedulingGeneration, Gates: gates,
	}
	return rollout, operation, revalidation
}

func runWorkerFixture(t *testing.T, rollout contracts.RecommendationRollout, operation contracts.RecommendationRolloutOperation, gateway *workerGatewayFixture, revalidation contracts.RecommendationRolloutRevalidation) (*workerStoreFixture, *workerRevalidatorFixture) {
	t.Helper()
	st := &workerStoreFixture{rollout: rollout, operation: operation}
	validator := &workerRevalidatorFixture{value: revalidation}
	runWorker(t, st, gateway, validator)
	return st, validator
}

func runWorker(t *testing.T, st store.RecommendationRolloutStore, gateway Gateway, validator Revalidator) {
	t.Helper()
	worker, err := NewWorker(st, gateway, validator, "worker-1", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return workerNow() }
	worker.RunOnce(context.Background())
}

func assertWorkerSucceeded(t *testing.T, st *workerStoreFixture) {
	t.Helper()
	if st.completion == nil || st.completion.OperationStatus != contracts.RecommendationRolloutOperationSucceeded || st.completion.ErrorCode != "" {
		t.Fatalf("completion=%+v", st.completion)
	}
}

func assertWorkerFailure(t *testing.T, st *workerStoreFixture, code contracts.RecommendationRolloutOperationErrorCode) {
	t.Helper()
	if st.completion == nil || st.completion.OperationStatus != contracts.RecommendationRolloutOperationFailed || st.completion.ErrorCode != code || st.completion.NextState.Status != contracts.RecommendationRolloutRollbackRequired {
		t.Fatalf("completion=%+v want failure=%s rollback_required", st.completion, code)
	}
}

func cloneWorkerWeights(values []contracts.RecommendationRolloutAccountWeight) []contracts.RecommendationRolloutAccountWeight {
	return append([]contracts.RecommendationRolloutAccountWeight(nil), values...)
}

func cloneWorkerRollout(input contracts.RecommendationRollout) contracts.RecommendationRollout {
	input.BaselineWeights = cloneWorkerWeights(input.BaselineWeights)
	input.State.EvidenceIDs = append([]string(nil), input.State.EvidenceIDs...)
	input.State.RollbackReasons = append([]contracts.RecommendationRolloutBlockReason(nil), input.State.RollbackReasons...)
	return input
}

func workerNow() time.Time {
	return time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
}

func stageName(stage contracts.RecommendationRolloutStage) string {
	return "stage-" + time.Duration(stage).String()
}

var _ store.RecommendationRolloutStore = (*workerStoreFixture)(nil)
var _ Gateway = (*workerGatewayFixture)(nil)
var _ Gateway = (*nilWeightGateway)(nil)
var _ Revalidator = (*workerRevalidatorFixture)(nil)
