package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/store"
)

// fakeAdapter records SetSchedulable calls and can be told to fail on a target.
type fakeAdapter struct {
	calls       []call
	failOn      string
	provisioned []contracts.GatewayAccountSpec
	updated     []contracts.GatewayAccountSpec
	deleted     []string
}

type call struct {
	accountID   string
	schedulable bool
}

func (f *fakeAdapter) Kind() contracts.InstanceKind                { return contracts.InstanceKindSub2API }
func (f *fakeAdapter) Capabilities() []contracts.AdapterCapability { return nil }
func (f *fakeAdapter) ListAccounts(context.Context, contracts.Instance) ([]contracts.GatewayAccount, error) {
	return []contracts.GatewayAccount{{ID: "1", Schedulable: true}}, nil
}
func (f *fakeAdapter) SetSchedulable(_ context.Context, _ contracts.Instance, accountID string, schedulable bool) error {
	f.calls = append(f.calls, call{accountID, schedulable})
	if accountID == f.failOn {
		return errors.New("boom")
	}
	return nil
}
func (f *fakeAdapter) ProvisionAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	f.provisioned = append(f.provisioned, spec)
	if spec.ChannelID == f.failOn {
		return contracts.GatewayProvisionResult{}, errors.New("boom")
	}
	id := spec.RemoteID
	if id == "" {
		id = "prov-" + spec.ChannelID
	}
	return contracts.GatewayProvisionResult{RemoteID: id, Created: true}, nil
}
func (f *fakeAdapter) UpdateAccount(_ context.Context, _ contracts.Instance, spec contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error) {
	f.updated = append(f.updated, spec)
	if spec.RemoteID == f.failOn {
		return contracts.GatewayProvisionResult{}, errors.New("boom")
	}
	return contracts.GatewayProvisionResult{RemoteID: spec.RemoteID}, nil
}
func (f *fakeAdapter) DeleteAccount(_ context.Context, _ contracts.Instance, accountID string) error {
	f.deleted = append(f.deleted, accountID)
	if accountID == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func setup(t *testing.T, adapter adapters.GatewayAdapter) (*Orchestrator, store.Store, contracts.Instance) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	inst, err := st.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "s", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	o := New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{contracts.InstanceKindSub2API: adapter})
	return o, st, inst
}

func seedManagedAccount(t *testing.T, st store.Store, inst contracts.Instance, remoteID string, state contracts.PublishedBindingState) contracts.RoutePlan {
	return seedAccountBinding(t, st, inst, remoteID, state, contracts.GatewayAccountPlatformManaged, "")
}

func seedAccountBinding(
	t *testing.T,
	st store.Store,
	inst contracts.Instance,
	remoteID string,
	state contracts.PublishedBindingState,
	ownership contracts.GatewayAccountOwnership,
	lastError string,
) contracts.RoutePlan {
	t.Helper()
	ctx := context.Background()
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: inst.UserID, InstanceID: inst.ID,
		PoolID: "pool-" + remoteID + "-" + string(ownership) + "-" + string(state),
		Status: contracts.RoutePlanPublished,
	})
	if err != nil {
		t.Fatalf("create managed route plan: %v", err)
	}
	plan, err = st.ClaimRoutePlanScheduling(ctx, plan.ID, contracts.RoutePlanPublished)
	if err != nil {
		t.Fatalf("claim managed route plan: %v", err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: inst.ID,
		ChannelID: "channel-" + remoteID + "-" + string(ownership) + "-" + string(state),
		RemoteID:  remoteID, AccountOwnership: ownership, State: state,
		LastError: lastError, SchedulingGeneration: plan.SchedulingGeneration,
	}); err != nil {
		t.Fatalf("create managed binding: %v", err)
	}
	return plan
}

func TestSwitchUpstreamDisablesThenEnablesAndAudits(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)

	err := o.SwitchUpstream(ctx, contracts.AccountSwitch{
		InstanceID:       inst.ID,
		DisableAccountID: "1",
		EnableAccountID:  "2",
		Reason:           "acc-1 连续 429",
	})
	if err != nil {
		t.Fatalf("switch: %v", err)
	}

	// order: disable 1 (false), then enable 2 (true)
	if len(fa.calls) != 2 || fa.calls[0] != (call{"1", false}) || fa.calls[1] != (call{"2", true}) {
		t.Fatalf("unexpected calls: %+v", fa.calls)
	}

	audits, err := st.ListAudits(ctx, 101)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(audits) != 2 {
		t.Fatalf("expected 2 audits, got %d", len(audits))
	}
	for _, a := range audits {
		if a.RiskLevel != contracts.RiskLevelL1 || a.Result != "accepted" || a.TargetType != "account" {
			t.Fatalf("unexpected audit: %+v", a)
		}
		if a.RequestHash != auditReasonHash("acc-1 连续 429") || a.RequestHash == "acc-1 连续 429" {
			t.Fatalf("audit must store only the reason hash: %+v", a)
		}
	}
}

func TestSwitchUpstreamPartialFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{failOn: "2"} // disable ok, enable backup fails
	o, st, inst := setup(t, fa)

	err := o.SwitchUpstream(ctx, contracts.AccountSwitch{
		InstanceID:       inst.ID,
		DisableAccountID: "1",
		EnableAccountID:  "2",
	})
	if err == nil {
		t.Fatal("expected error when enabling backup fails")
	}

	audits, _ := st.ListAudits(ctx, 101)
	// one accepted (disable), one failed (enable)
	var accepted, failed int
	for _, a := range audits {
		switch a.Result {
		case "accepted":
			accepted++
		case "failed":
			failed++
		}
	}
	if accepted != 1 || failed != 1 {
		t.Fatalf("expected 1 accepted + 1 failed audit, got accepted=%d failed=%d", accepted, failed)
	}
}

func TestSetSchedulableAudited(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)

	if err := o.SetSchedulable(ctx, inst.ID, "1", false, "手动停用"); err != nil {
		t.Fatalf("set schedulable: %v", err)
	}
	audits, _ := st.ListAudits(ctx, 101)
	if len(audits) != 1 || audits[0].Action != "account.disable_schedulable" {
		t.Fatalf("unexpected audits: %+v", audits)
	}
}

func TestSetSchedulableRejectsManagedAccountWithoutCurrentFence(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)
	plan := seedManagedAccount(t, st, inst, "managed-account", contracts.BindingActive)

	for name, callCtx := range map[string]context.Context{
		"missing": ctx,
		"stale": contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration - 1,
		}),
		"wrong scope": contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/another-plan", Version: plan.SchedulingGeneration,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			err := o.SetSchedulable(callCtx, inst.ID, "managed-account", false, "manual")
			if !errors.Is(err, ErrManagedAccountSchedulingFence) {
				t.Fatalf("error=%v, want ErrManagedAccountSchedulingFence", err)
			}
		})
	}
	if len(fa.calls) != 0 {
		t.Fatalf("managed account reached adapter without a current fence: %+v", fa.calls)
	}
}

func TestSetSchedulableAllowsManagedAccountWithCurrentFence(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)
	plan := seedManagedAccount(t, st, inst, "managed-account", contracts.BindingDisabled)
	ctx = contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration,
	})

	if err := o.SetSchedulable(ctx, inst.ID, "managed-account", true, "route plan recovery"); err != nil {
		t.Fatalf("set managed account with current fence: %v", err)
	}
	if len(fa.calls) != 1 || fa.calls[0] != (call{"managed-account", true}) {
		t.Fatalf("unexpected adapter calls: %+v", fa.calls)
	}
}

func TestSetSchedulableKeepsUserOwnedAccountsManual(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, _, inst := setup(t, fa)

	if err := o.SetSchedulable(ctx, inst.ID, "user-owned-account", false, "owner update"); err != nil {
		t.Fatalf("set user-owned account: %v", err)
	}
	if len(fa.calls) != 1 || fa.calls[0].accountID != "user-owned-account" {
		t.Fatalf("manual account calls=%+v, want user-owned account", fa.calls)
	}
}

func TestSetSchedulableStillProtectsRevokedManagedRemoteAccount(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)
	seedManagedAccount(t, st, inst, "former-managed-account", contracts.BindingRevoked)

	err := o.SetSchedulable(ctx, inst.ID, "former-managed-account", true, "manual restore")
	if !errors.Is(err, ErrManagedAccountSchedulingFence) {
		t.Fatalf("revoked managed account error=%v, want fence error", err)
	}
	if len(fa.calls) != 0 {
		t.Fatalf("revoked managed account reached adapter: %+v", fa.calls)
	}
}

func TestSetSchedulableIgnoresFailedOwnerMetadataBindingThatSharesManagedRemote(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	o, st, inst := setup(t, fa)
	managedPlan := seedManagedAccount(t, st, inst, "shared-remote", contracts.BindingActive)
	seedAccountBinding(t, st, inst, "shared-remote", contracts.BindingFailed,
		contracts.GatewayAccountOwnerProvided, ErrManagedAccountSchedulingFence.Error()+
			": account shared-remote belongs to route plan stale-owner at generation 2")
	ctx = contracts.WithGatewaySchedulingFence(ctx, contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + managedPlan.ID, Version: managedPlan.SchedulingGeneration,
	})

	if err := o.SetSchedulable(ctx, inst.ID, "shared-remote", false, "retire managed account"); err != nil {
		t.Fatalf("managed plan was poisoned by failed owner metadata binding: %v", err)
	}
	if len(fa.calls) != 1 || fa.calls[0] != (call{"shared-remote", false}) {
		t.Fatalf("managed scheduling call = %+v", fa.calls)
	}
}

func TestBindingRequiresSchedulingFenceOnlyExemptsFailedOwnerMetadata(t *testing.T) {
	tests := []struct {
		name      string
		ownership contracts.GatewayAccountOwnership
		state     contracts.PublishedBindingState
		lastError string
		want      bool
	}{
		{"owner failed at legacy core fence", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed,
			ErrManagedAccountSchedulingFence.Error() + ": account 9 belongs to route plan plan-owner at generation 2", false},
		{"owner failed with explicit no-dispatch proof", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed,
			contracts.OwnerMetadataUpdateNotDispatchedMarker + ": connector gateway: channel_id is required", false},
		{"owner failed with embedded fence text", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed,
			"gateway timeout after: " + ErrManagedAccountSchedulingFence.Error(), true},
		{"owner failed with incomplete legacy shape", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed,
			ErrManagedAccountSchedulingFence.Error() + ": account 9 timed out", true},
		{"owner failed with timeout", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed,
			"connector gateway: wait for task: context deadline exceeded", true},
		{"owner failed without evidence", contracts.GatewayAccountOwnerProvided, contracts.BindingFailed, "", true},
		{"owner pending", contracts.GatewayAccountOwnerProvided, contracts.BindingPending, "", true},
		{"owner active", contracts.GatewayAccountOwnerProvided, contracts.BindingActive, "", true},
		{"owner disabled", contracts.GatewayAccountOwnerProvided, contracts.BindingDisabled, "", true},
		{"owner revoked", contracts.GatewayAccountOwnerProvided, contracts.BindingRevoked, "", true},
		{"platform failed at core fence", contracts.GatewayAccountPlatformManaged, contracts.BindingFailed,
			ErrManagedAccountSchedulingFence.Error(), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := (contracts.PublishedBinding{
				AccountOwnership: test.ownership,
				State:            test.state,
				LastError:        test.lastError,
			}).RequiresSchedulingFence()
			if got != test.want {
				t.Fatalf("RequiresSchedulingFence() = %v, want %v", got, test.want)
			}
		})
	}
}

type failingManagedLookupStore struct {
	store.Store
	err error
}

func (s failingManagedLookupStore) ListPublishedBindings(context.Context, string) ([]contracts.PublishedBinding, error) {
	return nil, s.err
}

func TestSetSchedulableFailsClosedWhenManagedOwnershipCannotBeRead(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAdapter{}
	baseOrch, st, inst := setup(t, fa)
	_ = baseOrch
	lookupErr := errors.New("route plan store unavailable")
	o := New(failingManagedLookupStore{Store: st, err: lookupErr}, map[contracts.InstanceKind]adapters.GatewayAdapter{
		contracts.InstanceKindSub2API: fa,
	})

	err := o.SetSchedulable(ctx, inst.ID, "account", false, "manual")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("error=%v, want lookup failure", err)
	}
	if len(fa.calls) != 0 {
		t.Fatalf("adapter called after ownership lookup failure: %+v", fa.calls)
	}
}

func TestUnknownInstance(t *testing.T) {
	ctx := context.Background()
	o, _, _ := setup(t, &fakeAdapter{})
	_, err := o.ListAccounts(ctx, "inst-nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
