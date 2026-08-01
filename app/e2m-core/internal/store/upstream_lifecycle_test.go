package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

func newLifecycleFixture(t *testing.T) (*MemoryStore, context.Context, contracts.UpstreamPool) {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore(time.Now().UTC())
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "managed", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, pool
}

func TestInventoryClaimRequiresReadyPlatformManagedStock(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	draft, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "draft", DisplayName: "draft", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryDraft, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	ownerProvided, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "owner", DisplayName: "owner", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryReady, AccountOwnership: contracts.GatewayAccountOwnerProvided,
	})
	ready, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "ready", DisplayName: "ready", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryReady, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "inst", PoolID: pool.ID})
	selected, err := st.ClaimPlanChannels(ctx, plan.ID)
	if err != nil || len(selected) != 1 || selected[0].ID != ready.ID {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	if _, ok := st.channelAllocations[draft.ID]; ok {
		t.Fatal("draft inventory was allocated")
	}
	if _, ok := st.channelAllocations[ownerProvided.ID]; ok {
		t.Fatal("owner-provided account was allocated from platform stock")
	}
}

func TestMemoryBulkInventoryImportIsAllOrNothingAndStartsDraft(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	invalid := []contracts.UpstreamInventoryImportEntry{
		{Channel: contracts.UpstreamChannel{SourceID: "a", DisplayName: "A", AccountOwnership: contracts.GatewayAccountPlatformManaged}, SecretRef: "ref:a", MaskedValue: "***a"},
		{Channel: contracts.UpstreamChannel{SourceID: "b", DisplayName: "B", AccountOwnership: contracts.GatewayAccountPlatformManaged}, SecretRef: "", MaskedValue: "***b"},
	}
	if _, err := st.ImportUpstreamInventory(ctx, pool.ID, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid import err=%v", err)
	}
	channels, _ := st.ListUpstreamChannels(ctx, pool.ID)
	if len(channels) != 0 {
		t.Fatalf("partial batch survived: %+v", channels)
	}

	result, err := st.ImportUpstreamInventory(ctx, pool.ID, []contracts.UpstreamInventoryImportEntry{
		{Channel: contracts.UpstreamChannel{SourceID: "a", DisplayName: "A", AccountOwnership: contracts.GatewayAccountPlatformManaged}, SecretRef: "ref:a", MaskedValue: "***a"},
		{Channel: contracts.UpstreamChannel{SourceID: "b", DisplayName: "B", AccountOwnership: contracts.GatewayAccountPlatformManaged}, SecretRef: "ref:b", MaskedValue: "***b"},
	})
	if err != nil || result.Imported != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, imported := range result.Channels {
		channel, _ := st.GetUpstreamChannel(ctx, imported.ID)
		if channel.InventoryState != contracts.UpstreamInventoryDraft || channel.Status != contracts.UpstreamChannelMaintenance {
			t.Fatalf("imported channel was allocatable: %+v", channel)
		}
		delivery, getErr := st.GetUpstreamKeyDelivery(ctx, channel.ID)
		if getErr != nil || delivery.KeyVersion != 1 || delivery.SecretRef == "" {
			t.Fatalf("delivery=%+v err=%v", delivery, getErr)
		}
	}
}

func TestMemoryInventorySafetyStockAndAllocatedRetirementGuard(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	result, err := st.ImportUpstreamInventory(ctx, pool.ID, []contracts.UpstreamInventoryImportEntry{
		{Channel: contracts.UpstreamChannel{SourceID: "a", DisplayName: "A", AccountOwnership: contracts.GatewayAccountPlatformManaged}, SecretRef: "ref:a", MaskedValue: "***a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, _ := st.GetUpstreamChannel(ctx, result.Channels[0].ID)
	if _, err := st.SetUpstreamInventoryState(ctx, channel.ID, contracts.UpstreamInventoryReady); err != nil {
		t.Fatal(err)
	}
	channel, _ = st.GetUpstreamChannel(ctx, channel.ID)
	channel.Status = contracts.UpstreamChannelActive
	if _, err := st.UpdateUpstreamChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpstreamPoolSafetyStock(ctx, pool.ID, 2); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.GetUpstreamInventory(ctx, pool.ID)
	if err != nil || len(snapshot.Pools) != 1 || snapshot.Pools[0].Available != 1 || !snapshot.Pools[0].BelowSafetyStock || len(snapshot.Alerts) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "inst", PoolID: pool.ID})
	if _, err := st.ClaimPlanChannels(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUpstreamInventoryState(ctx, channel.ID, contracts.UpstreamInventoryRetired); !errors.Is(err, ErrConflict) {
		t.Fatalf("allocated retirement err=%v", err)
	}
}

func TestMemoryInventoryAvailableCountsOnlyPlatformManagedReadyStock(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	for _, ownership := range []contracts.GatewayAccountOwnership{
		contracts.GatewayAccountPlatformManaged,
		contracts.GatewayAccountOwnerProvided,
	} {
		_, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
			PoolID: pool.ID, SourceID: string(ownership), DisplayName: string(ownership),
			Status: contracts.UpstreamChannelActive, InventoryState: contracts.UpstreamInventoryReady,
			AccountOwnership: ownership,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := st.GetUpstreamInventory(ctx, pool.ID)
	if err != nil || len(snapshot.Pools) != 1 || snapshot.Pools[0].Ready != 2 || snapshot.Pools[0].Available != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestMemoryExplicitMigrationAndDurableRetirementJob(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	target, _ := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "target", Status: contracts.UpstreamPoolMaintenance})
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "a", DisplayName: "A", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryReady, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "inst", PoolID: pool.ID})
	if _, err := st.ClaimPlanChannels(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	ordinary := channel
	ordinary.PoolID = target.ID
	if _, err := st.UpdateUpstreamChannel(ctx, ordinary); !errors.Is(err, ErrConflict) {
		t.Fatalf("ordinary pool move err=%v", err)
	}
	migration, err := st.MigrateUpstreamChannel(ctx, channel.ID, target.ID, "operator approved migration", 7)
	if err != nil || migration.FromPoolID != pool.ID || migration.ToPoolID != target.ID {
		t.Fatalf("migration=%+v err=%v", migration, err)
	}
	moved, _ := st.GetUpstreamChannel(ctx, channel.ID)
	if moved.PoolID != target.ID {
		t.Fatalf("channel not moved: %+v", moved)
	}

	job, err := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	if err != nil || job.TotalPlans != 1 || job.Status != contracts.PoolRetirementPending {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	item, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID)
	if err != nil || !claimed || item.PlanID != plan.ID {
		t.Fatalf("item=%+v claimed=%v err=%v", item, claimed, err)
	}
	completed, err := st.CompletePoolRetirementItem(ctx, job.ID, plan.ID, item.Attempts, "")
	if err != nil || completed.Status != contracts.PoolRetirementFinalizing || completed.CompletedPlans != 1 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	completed, err = st.FinalizePoolRetirementJob(ctx, job.ID)
	if err != nil || completed.Status != contracts.PoolRetirementCleanup {
		t.Fatalf("finalize=%+v err=%v", completed, err)
	}
	cleanupItem, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID)
	if err != nil || !claimed || cleanupItem.PlanID != plan.ID || cleanupItem.CleanupAttempts != 1 {
		t.Fatalf("cleanup item=%+v claimed=%v err=%v", cleanupItem, claimed, err)
	}
	completed, err = st.CompletePoolRetirementCleanupItem(ctx, job.ID, plan.ID, cleanupItem.CleanupAttempts, "")
	if err != nil || completed.Status != contracts.PoolRetirementCompleted || completed.CleanupCompletedPlans != 1 {
		t.Fatalf("cleanup completed=%+v err=%v", completed, err)
	}
	loaded, _ := st.GetPoolRetirementJob(ctx, job.ID)
	if loaded.Status != contracts.PoolRetirementCompleted || len(loaded.Items) != 1 {
		t.Fatalf("job was not durable: %+v", loaded)
	}
}

func TestMemoryKeyRotationRequiresEveryTargetAndRollbackAdvancesVersion(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	instances := make([]contracts.Instance, 2)
	for i := range instances {
		instances[i], _ = st.CreateInstance(ctx, contracts.Instance{UserID: 42, Name: "gateway", Kind: contracts.InstanceKindSub2API})
	}
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "a", DisplayName: "A", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryReady, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	for _, instance := range instances {
		plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: instance.ID, PoolID: pool.ID})
		_, _ = st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
			PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
			AccountOwnership: contracts.GatewayAccountPlatformManaged, State: contracts.BindingActive,
		})
	}
	initial, err := st.StartUpstreamKeyRotation(ctx, channel.ID, "ref:v1", "***v1")
	if err != nil || initial.Status != contracts.KeyRotationStable || initial.CurrentKeyVersion != 1 {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	rotating, err := st.StartUpstreamKeyRotation(ctx, channel.ID, "ref:v2", "***v2")
	if err != nil || rotating.Status != contracts.KeyRotationDeploying || rotating.PreviousKeyVersion != 1 || rotating.CurrentKeyVersion != 2 {
		t.Fatalf("rotating=%+v err=%v", rotating, err)
	}
	for i, instance := range instances {
		_, _ = st.UpsertUpstreamKeyProofReceipt(ctx, contracts.UpstreamKeyProofReceipt{
			ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: 2,
			ConnectorID: "connector", Status: contracts.DeliveryKeyProofVerified,
		})
		if i == 0 {
			_, _ = st.UpsertUpstreamKeyDeployment(ctx, contracts.UpstreamKeyDeployment{
				ChannelID: channel.ID, InstanceID: instance.ID, KeyVersion: 2,
				ConnectorID: "connector", Status: contracts.DeliveryKeyDeploymentDeployed,
			})
		}
	}
	if _, err := st.BeginUpstreamKeyRotationFinalize(ctx, channel.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial confirmation finalize err=%v", err)
	}
	rolledBack, err := st.BeginUpstreamKeyRotationRollback(ctx, channel.ID)
	if err != nil || rolledBack.Rotation.CurrentKeyVersion != 3 || rolledBack.Rotation.CurrentMaskedValue != "***v1" || rolledBack.Rotation.Status != contracts.KeyRotationRollingBack {
		t.Fatalf("rollback=%+v err=%v", rolledBack, err)
	}
}

func TestMemoryKeyRotationFinalizeAbortRestoresRollbackStatus(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	channel, _ := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, SourceID: "a", DisplayName: "A", Status: contracts.UpstreamChannelActive,
		InventoryState: contracts.UpstreamInventoryReady, AccountOwnership: contracts.GatewayAccountPlatformManaged,
	})
	_, _ = st.StartUpstreamKeyRotation(ctx, channel.ID, "ref:v1", "***v1")
	_, _ = st.StartUpstreamKeyRotation(ctx, channel.ID, "ref:v2", "***v2")
	rolledBack, err := st.BeginUpstreamKeyRotationRollback(ctx, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := st.BeginUpstreamKeyRotationFinalize(ctx, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AbortUpstreamKeyRotationFinalize(ctx, channel.ID, barrier.Rotation.CurrentKeyVersion); err != nil {
		t.Fatal(err)
	}
	view, err := st.GetUpstreamKeyRotation(ctx, channel.ID)
	if err != nil || view.Status != contracts.KeyRotationRollingBack || view.CurrentKeyVersion != rolledBack.Rotation.CurrentKeyVersion {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestMemoryRetirementExpiredRunningItemCanBeReclaimed(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	clock := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return clock }
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "inst", PoolID: pool.ID})
	job, err := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	first, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID)
	if err != nil || !claimed || first.PlanID != plan.ID || first.Attempts != 1 || first.LeaseUntil == nil {
		t.Fatalf("first=%+v claimed=%v err=%v", first, claimed, err)
	}
	if _, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID); err != nil || claimed {
		t.Fatalf("live lease claimed=%v err=%v", claimed, err)
	}
	clock = first.LeaseUntil.Add(time.Second)
	second, claimed, err := st.ClaimPoolRetirementItem(ctx, job.ID)
	if err != nil || !claimed || second.Attempts != 2 {
		t.Fatalf("second=%+v claimed=%v err=%v", second, claimed, err)
	}
	staleGuardCtx := contracts.WithReconcileSideEffectGuard(ctx, func(guardCtx context.Context) error {
		_, guardErr := st.RenewPoolRetirementItem(guardCtx, job.ID, plan.ID, first.Attempts, time.Minute)
		return guardErr
	})
	if err := contracts.RunReconcileSideEffectGuard(staleGuardCtx); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale drain side-effect guard error=%v, want ErrConflict", err)
	}
	if _, err := st.CompletePoolRetirementItem(ctx, job.ID, plan.ID, first.Attempts, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale drain complete error=%v, want ErrConflict", err)
	}
	renewed, err := st.RenewPoolRetirementItem(ctx, job.ID, plan.ID, second.Attempts, time.Minute)
	if err != nil || renewed.Attempts != second.Attempts || renewed.LeaseUntil == nil {
		t.Fatalf("renew second drain claim=%+v err=%v", renewed, err)
	}
	completed, err := st.CompletePoolRetirementItem(ctx, job.ID, plan.ID, second.Attempts, "")
	if err != nil || completed.Status != contracts.PoolRetirementFinalizing {
		t.Fatalf("complete second drain claim=%+v err=%v", completed, err)
	}
}

func TestMemoryRetirementExpiredCleanupItemCanBeReclaimed(t *testing.T) {
	st, ctx, pool := newLifecycleFixture(t)
	clock := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return clock }
	plan, _ := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: 42, InstanceID: "inst", PoolID: pool.ID})
	job, _ := st.CreatePoolRetirementJob(ctx, pool.ID, 7)
	item, _, _ := st.ClaimPoolRetirementItem(ctx, job.ID)
	_, _ = st.CompletePoolRetirementItem(ctx, job.ID, item.PlanID, item.Attempts, "")
	_, _ = st.FinalizePoolRetirementJob(ctx, job.ID)
	first, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID)
	if err != nil || !claimed || first.PlanID != plan.ID || first.CleanupAttempts != 1 || first.CleanupLeaseUntil == nil {
		t.Fatalf("first=%+v claimed=%v err=%v", first, claimed, err)
	}
	if _, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID); err != nil || claimed {
		t.Fatalf("live cleanup lease claimed=%v err=%v", claimed, err)
	}
	clock = first.CleanupLeaseUntil.Add(time.Second)
	second, claimed, err := st.ClaimPoolRetirementCleanupItem(ctx, job.ID)
	if err != nil || !claimed || second.CleanupAttempts != 2 {
		t.Fatalf("second=%+v claimed=%v err=%v", second, claimed, err)
	}
	staleGuardCtx := contracts.WithReconcileSideEffectGuard(ctx, func(guardCtx context.Context) error {
		_, guardErr := st.RenewPoolRetirementCleanupItem(guardCtx, job.ID, plan.ID, first.CleanupAttempts, time.Minute)
		return guardErr
	})
	if err := contracts.RunReconcileSideEffectGuard(staleGuardCtx); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup side-effect guard error=%v, want ErrConflict", err)
	}
	if _, err := st.CompletePoolRetirementCleanupItem(ctx, job.ID, plan.ID, first.CleanupAttempts, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cleanup complete error=%v, want ErrConflict", err)
	}
	renewed, err := st.RenewPoolRetirementCleanupItem(ctx, job.ID, plan.ID, second.CleanupAttempts, time.Minute)
	if err != nil || renewed.CleanupAttempts != second.CleanupAttempts || renewed.CleanupLeaseUntil == nil {
		t.Fatalf("renew second cleanup claim=%+v err=%v", renewed, err)
	}
	completed, err := st.CompletePoolRetirementCleanupItem(ctx, job.ID, plan.ID, second.CleanupAttempts, "")
	if err != nil || completed.Status != contracts.PoolRetirementCompleted {
		t.Fatalf("complete second cleanup claim=%+v err=%v", completed, err)
	}
}
