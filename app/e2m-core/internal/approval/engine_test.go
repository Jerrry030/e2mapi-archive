package approval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/store"
)

type scriptedAdapter struct {
	mu    sync.Mutex
	calls []struct {
		id  string
		val bool
	}
	failFor map[string]bool
}

func seedManagedApprovalAccount(t *testing.T, st store.Store, inst contracts.Instance, remoteID string) contracts.RoutePlan {
	t.Helper()
	ctx := context.Background()
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID, PoolID: "pool-" + remoteID,
		Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatalf("claim route plan: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: inst.ID, ChannelID: "channel-" + remoteID,
		RemoteID: remoteID, State: contracts.BindingActive, SchedulingGeneration: plan.SchedulingGeneration,
	}); err != nil {
		t.Fatalf("create published binding: %v", err)
	}
	return plan
}

func (s *scriptedAdapter) Kind() contracts.InstanceKind                { return contracts.InstanceKindSub2API }
func (s *scriptedAdapter) Capabilities() []contracts.AdapterCapability { return nil }
func (s *scriptedAdapter) ListAccounts(context.Context, contracts.Instance) ([]contracts.GatewayAccount, error) {
	return nil, nil
}
func (s *scriptedAdapter) SetSchedulable(_ context.Context, _ contracts.Instance, id string, val bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFor[id] {
		return context.DeadlineExceeded
	}
	s.calls = append(s.calls, struct {
		id  string
		val bool
	}{id, val})
	return nil
}

func (s *scriptedAdapter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}
func (s *scriptedAdapter) ProvisionAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	id := spec.RemoteID
	if id == "" {
		id = "prov-" + spec.ChannelID
	}
	return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
}
func (s *scriptedAdapter) UpdateAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID}, nil
}
func (s *scriptedAdapter) DeleteAccount(_ context.Context, _ contracts.Instance, _ string) error {
	return nil
}

func setup(t *testing.T, adapter *scriptedAdapter) (*Engine, store.Store, contracts.Instance) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC))
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "i", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindSub2API: adapter})
	return New(st, orch, nil), st, inst
}

func boolPtr(v bool) *bool { return &v }

func TestSubmitApproveExecutes(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, st, inst := setup(t, adapter)

	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID:  inst.ID,
		Action:      contracts.ApprovalActionBatchSchedulable,
		AccountIDs:  []string{"1", "2", "3"},
		Schedulable: boolPtr(false),
		Reason:      "批量下线过期账号",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ap.Status != contracts.ApprovalPending || ap.RiskLevel != contracts.RiskLevelL2 {
		t.Fatalf("submit state wrong: %+v", ap)
	}
	// Nothing executed yet — the gate holds.
	if len(adapter.calls) != 0 {
		t.Fatalf("no execution before approval, got %v", adapter.calls)
	}

	done, err := eng.Approve(ctx, ap.ID, "老板")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != contracts.ApprovalExecuted || done.DecidedBy != "老板" {
		t.Fatalf("approve state wrong: %+v", done)
	}
	if len(adapter.calls) != 3 {
		t.Fatalf("expected 3 batch calls, got %v", adapter.calls)
	}

	// Audits: submit + approve + execute (approval-level) + 3 per-account.
	audits, _ := st.ListAudits(ctx, 101)
	var withApprovalID int
	for _, a := range audits {
		if a.ApprovalID == ap.ID {
			withApprovalID++
		}
	}
	if withApprovalID < 3 {
		t.Fatalf("approval audits missing, got %d with approval_id", withApprovalID)
	}
}

func TestRejectNeverExecutes(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, _, inst := setup(t, adapter)

	ap, _ := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"1"}, Schedulable: boolPtr(false),
	})
	rej, err := eng.Reject(ctx, ap.ID, "老板", "先不动")
	if err != nil {
		t.Fatal(err)
	}
	if rej.Status != contracts.ApprovalRejected {
		t.Fatalf("want rejected, got %s", rej.Status)
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("rejected request must not execute, got %v", adapter.calls)
	}
	// Double-decide is blocked.
	if _, err := eng.Approve(ctx, ap.ID, "别人"); err == nil {
		t.Fatal("approving a rejected request must fail")
	}
}

func TestSubmitRejectsManagedAccountButKeepsUserOwnedAccounts(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, st, inst := setup(t, adapter)
	seedManagedApprovalAccount(t, st, inst, "managed-account")

	_, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"user-owned-account", "managed-account"}, Schedulable: boolPtr(false),
	})
	if !errors.Is(err, orchestrator.ErrManagedAccountSchedulingFence) {
		t.Fatalf("submit error=%v, want managed account fence error", err)
	}
	approvals, listErr := st.ListApprovals(ctx, inst.UserID, "")
	if listErr != nil || len(approvals) != 0 {
		t.Fatalf("rejected submit persisted approvals=%+v err=%v", approvals, listErr)
	}

	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"user-owned-account"}, Schedulable: boolPtr(false),
	})
	if err != nil || ap.Status != contracts.ApprovalPending {
		t.Fatalf("user-owned account submit=%+v err=%v", ap, err)
	}
}

func TestApproveRechecksAccountThatBecameManagedWhilePending(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, st, inst := setup(t, adapter)
	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"late-managed-account"}, Schedulable: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("submit before account is managed: %v", err)
	}
	seedManagedApprovalAccount(t, st, inst, "late-managed-account")

	done, err := eng.Approve(ctx, ap.ID, "operator")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if done.Status != contracts.ApprovalFailed || !strings.Contains(done.ResultNote, orchestrator.ErrManagedAccountSchedulingFence.Error()) {
		t.Fatalf("approval should fail managed ownership recheck: %+v", done)
	}
	if adapter.callCount() != 0 {
		t.Fatalf("managed account reached adapter after approval: %+v", adapter.calls)
	}
}

type racingManagedStore struct {
	store.Store
	instance         contracts.Instance
	mu               sync.Mutex
	bindingListCalls int
	seeded           bool
}

func (s *racingManagedStore) ListPublishedBindings(ctx context.Context, planID string) ([]contracts.PublishedBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingListCalls++
	// Submit performs one preflight and approval performs a second. Seed the
	// managed binding when SetSchedulable starts its third lookup so only its
	// side-effect-boundary check can catch the ownership change.
	if s.bindingListCalls == 3 && !s.seeded {
		s.seeded = true
		plan, err := s.Store.CreateRoutePlan(ctx, contracts.RoutePlan{
			UserID: s.instance.UserID, InstanceID: s.instance.ID, PoolID: "racing-pool",
			Status: contracts.RoutePlanPublished,
		})
		if err != nil {
			return nil, err
		}
		plan, err = s.Store.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
		if err != nil {
			return nil, err
		}
		if _, err := s.Store.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: s.instance.ID, ChannelID: "racing-channel",
			RemoteID: "racing-account", State: contracts.BindingActive,
			SchedulingGeneration: plan.SchedulingGeneration,
		}); err != nil {
			return nil, err
		}
	}
	return s.Store.ListPublishedBindings(ctx, planID)
}

func TestSetSchedulableBoundaryCatchesOwnershipChangeAfterApprovalPreflight(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	baseStore := store.NewMemoryStore(time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC))
	inst, err := baseStore.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "i", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	racingStore := &racingManagedStore{Store: baseStore, instance: inst}
	orch := orchestrator.New(racingStore, map[contracts.InstanceKind]adapters.GatewayAdapter{
		contracts.InstanceKindSub2API: adapter,
	})
	eng := New(racingStore, orch, nil)
	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"racing-account"}, Schedulable: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	done, err := eng.Approve(ctx, ap.ID, "operator")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if done.Status != contracts.ApprovalFailed || adapter.callCount() != 0 {
		t.Fatalf("side-effect boundary did not fence race: approval=%+v calls=%+v", done, adapter.calls)
	}
}

func TestPartialFailureMarksFailed(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{failFor: map[string]bool{"2": true}}
	eng, _, inst := setup(t, adapter)

	ap, _ := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"1", "2"}, Schedulable: boolPtr(false),
	})
	done, err := eng.Approve(ctx, ap.ID, "老板")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != contracts.ApprovalFailed {
		t.Fatalf("partial failure must mark failed, got %s", done.Status)
	}
}

func TestConcurrentApprovalDecisionExecutesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, st, inst := setup(t, adapter)
	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"1"}, Schedulable: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	const callers = 12
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, approveErr := eng.Approve(ctx, ap.ID, "operator")
			errs <- approveErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded, conflicted := 0, 0
	for approveErr := range errs {
		switch {
		case approveErr == nil:
			succeeded++
		case errors.Is(approveErr, store.ErrConflict) || approveErr != nil:
			conflicted++
		}
	}
	if succeeded != 1 || conflicted != callers-1 {
		t.Fatalf("approve results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if got := adapter.callCount(); got != 1 {
		t.Fatalf("approval side effects=%d, want 1", got)
	}
	stored, err := st.GetApproval(ctx, ap.ID)
	if err != nil || stored.Status != contracts.ApprovalExecuted {
		t.Fatalf("stored approval=%+v err=%v", stored, err)
	}
}

func TestConcurrentApproveRejectHasOneDecision(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{}
	eng, st, inst := setup(t, adapter)
	ap, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"1"}, Schedulable: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, approveErr := eng.Approve(ctx, ap.ID, "approver")
		errs <- approveErr
	}()
	go func() {
		<-start
		_, rejectErr := eng.Reject(ctx, ap.ID, "rejector", "stop")
		errs <- rejectErr
	}()
	close(start)
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one decision must succeed: first=%v second=%v", first, second)
	}
	stored, err := st.GetApproval(ctx, ap.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if stored.Status != contracts.ApprovalExecuted && stored.Status != contracts.ApprovalRejected {
		t.Fatalf("unexpected terminal status %s", stored.Status)
	}
	wantCalls := 0
	if stored.Status == contracts.ApprovalExecuted {
		wantCalls = 1
	}
	if got := adapter.callCount(); got != wantCalls {
		t.Fatalf("side effects=%d, want %d for status %s", got, wantCalls, stored.Status)
	}
}

func TestSubmitValidation(t *testing.T) {
	ctx := context.Background()
	eng, _, inst := setup(t, &scriptedAdapter{})

	if _, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: "rm -rf", AccountIDs: []string{"1"}, Schedulable: boolPtr(false),
	}); err == nil {
		t.Fatal("unknown action must be rejected")
	}
	if _, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: inst.ID, Action: contracts.ApprovalActionBatchSchedulable, Schedulable: boolPtr(false),
	}); err == nil {
		t.Fatal("empty account_ids must be rejected")
	}
	if _, err := eng.Submit(ctx, contracts.ApprovalRequest{
		InstanceID: "inst-nope", Action: contracts.ApprovalActionBatchSchedulable,
		AccountIDs: []string{"1"}, Schedulable: boolPtr(false),
	}); err == nil {
		t.Fatal("missing instance must be rejected")
	}
}
