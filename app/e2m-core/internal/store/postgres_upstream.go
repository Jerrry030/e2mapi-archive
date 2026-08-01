package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

// PostgresStore implementations for the platform-managed upstream layer. String
// slices (models/groups) are stored as JSONB to match the labels convention.

func marshalStrings(v []string) []byte {
	if len(v) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func unmarshalStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *PostgresStore) CreateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error) {
	p := input
	p.ResourceClass = contracts.NormalizePlatformResourceClass(p.ResourceClass)
	p.DeliveryMode = p.DeliveryMode.Normalize()
	if !p.ResourceClass.IsPlatformSupply() || !p.DeliveryMode.Valid() {
		return contracts.UpstreamPool{}, ErrInvalid
	}
	if p.ID == "" {
		p.ID = newID("pool")
	}
	if p.Status == "" {
		p.Status = contracts.UpstreamPoolActive
	}
	now := nowUTC()
	p.CreatedAt, p.UpdatedAt = now, now
	labels, err := marshalLabels(p.Labels)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO upstream_pools (id, resource_class, delivery_mode, name, provider, models, region, status, description, labels, safety_stock_threshold, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, string(p.ResourceClass), string(p.DeliveryMode), p.Name, p.Provider, marshalStrings(p.Models), p.Region, string(p.Status), p.Description, labels, p.SafetyStockThreshold, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	return p, nil
}

func scanPool(row rowScanner) (contracts.UpstreamPool, error) {
	var p contracts.UpstreamPool
	var status string
	var models, labels []byte
	var resourceClass, deliveryMode string
	if err := row.Scan(&p.ID, &resourceClass, &deliveryMode, &p.Name, &p.Provider, &models, &p.Region, &status, &p.Description, &labels, &p.SafetyStockThreshold, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return contracts.UpstreamPool{}, err
	}
	p.Status = contracts.UpstreamPoolStatus(status)
	p.ResourceClass = contracts.ResourceClass(resourceClass)
	p.DeliveryMode = contracts.UpstreamDeliveryMode(deliveryMode)
	p.Models = unmarshalStrings(models)
	p.Labels = unmarshalLabels(labels)
	return p, nil
}

func (s *PostgresStore) GetUpstreamPool(ctx context.Context, id string) (contracts.UpstreamPool, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, resource_class, delivery_mode, name, provider, models, region, status, description, labels, safety_stock_threshold, created_at, updated_at
		 FROM upstream_pools WHERE id=$1`, id)
	p, err := scanPool(row)
	if err != nil {
		return contracts.UpstreamPool{}, mapNotFound(err)
	}
	return p, nil
}

func (s *PostgresStore) ListUpstreamPools(ctx context.Context) ([]contracts.UpstreamPool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, resource_class, delivery_mode, name, provider, models, region, status, description, labels, safety_stock_threshold, created_at, updated_at
		 FROM upstream_pools ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.UpstreamPool
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateUpstreamPool(ctx context.Context, input contracts.UpstreamPool) (contracts.UpstreamPool, error) {
	input.ResourceClass = contracts.NormalizePlatformResourceClass(input.ResourceClass)
	input.DeliveryMode = input.DeliveryMode.Normalize()
	if !input.ResourceClass.IsPlatformSupply() || !input.DeliveryMode.Valid() {
		return contracts.UpstreamPool{}, ErrInvalid
	}
	input.UpdatedAt = nowUTC()
	labels, err := marshalLabels(input.Labels)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentStatus contracts.UpstreamPoolStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM upstream_pools WHERE id=$1 FOR UPDATE`, input.ID).Scan(&currentStatus); err != nil {
		return contracts.UpstreamPool{}, mapNotFound(err)
	}
	if currentStatus != contracts.UpstreamPoolRetired && input.Status == contracts.UpstreamPoolRetired {
		return contracts.UpstreamPool{}, ErrConflict
	}
	if currentStatus == contracts.UpstreamPoolMaintenance && input.Status == contracts.UpstreamPoolActive {
		var retiring bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM pool_retirement_jobs
			    WHERE pool_id=$1 AND status IN ('pending','running','partial','finalizing','cleanup')
			)`, input.ID,
		).Scan(&retiring); err != nil {
			return contracts.UpstreamPool{}, err
		}
		if retiring {
			return contracts.UpstreamPool{}, ErrConflict
		}
	}
	tag, err := tx.Exec(ctx,
		`UPDATE upstream_pools SET resource_class=$2, delivery_mode=$3, name=$4, provider=$5, models=$6, region=$7, status=$8, description=$9, labels=$10, safety_stock_threshold=$11, updated_at=$12
		 WHERE id=$1`,
		input.ID, string(input.ResourceClass), string(input.DeliveryMode), input.Name, input.Provider, marshalStrings(input.Models), input.Region, string(input.Status), input.Description, labels, input.SafetyStockThreshold, input.UpdatedAt)
	if err != nil {
		return contracts.UpstreamPool{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.UpstreamPool{}, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamPool{}, err
	}
	return input, nil
}

func (s *PostgresStore) CreateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error) {
	c := input
	if c.SourceID != "" && !contracts.IsUpstreamSourceIdentity(c.SourceID) {
		return contracts.UpstreamChannel{}, ErrInvalid
	}
	if c.ID == "" {
		c.ID = newID("uchan")
	}
	if c.Status == "" {
		c.Status = contracts.UpstreamChannelActive
	}
	if c.InventoryState == "" {
		// Direct store callers and legacy fixtures predate inventory admission;
		// preserve them as ready. Product/API creation explicitly supplies draft.
		c.InventoryState = contracts.UpstreamInventoryReady
	}
	now := nowUTC()
	c.CreatedAt, c.UpdatedAt = now, now
	labels, err := marshalLabels(c.Labels)
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	c.AccountOwnership = c.AccountOwnership.Normalize()
	if !c.AccountOwnership.Valid() {
		return contracts.UpstreamChannel{}, ErrConflict
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO upstream_channels (id, pool_id, source_id, account_ownership, display_name, provider, models, probe_capability, probe_endpoint_path, groups, credential_binding_id, proxy_binding_id, priority, weight, cost_hint, status, inventory_state, labels, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		c.ID, c.PoolID, c.SourceID, string(c.AccountOwnership), c.DisplayName, c.Provider, marshalStrings(c.Models), string(c.ProbeCapability), c.ProbeEndpointPath, marshalStrings(c.Groups), c.CredentialBindingID, c.ProxyBindingID, c.Priority, c.Weight, c.CostHint, string(c.Status), string(c.InventoryState), labels, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	return c, nil
}

func scanChannel(row rowScanner) (contracts.UpstreamChannel, error) {
	var c contracts.UpstreamChannel
	var ownership, status, inventoryState string
	var models, groups, labels []byte
	if err := row.Scan(&c.ID, &c.PoolID, &c.SourceID, &ownership, &c.DisplayName, &c.Provider, &models, &c.ProbeCapability, &c.ProbeEndpointPath, &groups, &c.CredentialBindingID, &c.ProxyBindingID, &c.Priority, &c.Weight, &c.CostHint, &status, &inventoryState, &labels, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	c.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
	c.Status = contracts.UpstreamChannelStatus(status)
	c.InventoryState = contracts.UpstreamInventoryState(inventoryState)
	c.Models = unmarshalStrings(models)
	c.Groups = unmarshalStrings(groups)
	c.Labels = unmarshalLabels(labels)
	return c, nil
}

const channelCols = `id, pool_id, source_id, account_ownership, display_name, provider, models, probe_capability, probe_endpoint_path, groups, credential_binding_id, proxy_binding_id, priority, weight, cost_hint, status, inventory_state, labels, created_at, updated_at`

func (s *PostgresStore) GetUpstreamChannel(ctx context.Context, id string) (contracts.UpstreamChannel, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+channelCols+` FROM upstream_channels WHERE id=$1`, id)
	c, err := scanChannel(row)
	if err != nil {
		return contracts.UpstreamChannel{}, mapNotFound(err)
	}
	return c, nil
}

func (s *PostgresStore) ListUpstreamChannels(ctx context.Context, poolID string) ([]contracts.UpstreamChannel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+channelCols+` FROM upstream_channels WHERE ($1='' OR pool_id=$1) ORDER BY created_at`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.UpstreamChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateUpstreamChannel(ctx context.Context, input contracts.UpstreamChannel) (contracts.UpstreamChannel, error) {
	if input.SourceID != "" && !contracts.IsUpstreamSourceIdentity(input.SourceID) {
		return contracts.UpstreamChannel{}, ErrInvalid
	}
	input.AccountOwnership = input.AccountOwnership.Normalize()
	if !input.AccountOwnership.Valid() {
		return contracts.UpstreamChannel{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanChannel(tx.QueryRow(ctx, `SELECT `+channelCols+` FROM upstream_channels WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return contracts.UpstreamChannel{}, mapNotFound(err)
	}
	var allocated bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_channel_allocations WHERE channel_id=$1)`, input.ID).Scan(&allocated); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	if current.AccountOwnership.Normalize() != input.AccountOwnership || allocated &&
		(current.SourceIdentity() != input.SourceIdentity() || current.PoolID != input.PoolID || input.InventoryState == contracts.UpstreamInventoryRetired) {
		return contracts.UpstreamChannel{}, ErrConflict
	}
	if upstreamChannelSemanticEqual(current, input) {
		if err := tx.Commit(ctx); err != nil {
			return contracts.UpstreamChannel{}, err
		}
		return current, nil
	}
	input.UpdatedAt = nowUTC()
	labels, err := marshalLabels(input.Labels)
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE upstream_channels SET pool_id=$2, source_id=$3, display_name=$4, provider=$5, models=$6, probe_capability=$7, probe_endpoint_path=$8, groups=$9, credential_binding_id=$10, proxy_binding_id=$11, priority=$12, weight=$13, cost_hint=$14, status=$15, labels=$16, updated_at=$17, inventory_state=$19
		 WHERE id=$1
		   AND account_ownership=$18
		   AND (NOT EXISTS (SELECT 1 FROM upstream_channel_allocations WHERE channel_id=$1)
		        OR (COALESCE(NULLIF(BTRIM(source_id), ''), id)=COALESCE(NULLIF(BTRIM($3), ''), id)
		            AND pool_id=$2 AND $19 <> 'retired'))`,
		input.ID, input.PoolID, input.SourceID, input.DisplayName, input.Provider, marshalStrings(input.Models), string(input.ProbeCapability), input.ProbeEndpointPath, marshalStrings(input.Groups), input.CredentialBindingID, input.ProxyBindingID, input.Priority, input.Weight, input.CostHint, string(input.Status), labels, input.UpdatedAt, string(input.AccountOwnership), string(input.InventoryState))
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	if tag.RowsAffected() == 0 {
		return contracts.UpstreamChannel{}, ErrConflict
	}
	rows, err := tx.Query(ctx, `UPDATE route_plans AS plan SET scheduling_generation=plan.scheduling_generation+1,updated_at=$2
		WHERE EXISTS (SELECT 1 FROM published_bindings AS binding WHERE binding.plan_id=plan.id AND binding.channel_id=$1)
		RETURNING plan.id,plan.scheduling_generation`, input.ID, input.UpdatedAt)
	if err != nil {
		return contracts.UpstreamChannel{}, err
	}
	type advancedPlan struct {
		id         string
		generation int64
	}
	advanced := make([]advancedPlan, 0)
	for rows.Next() {
		var plan advancedPlan
		if err := rows.Scan(&plan.id, &plan.generation); err != nil {
			rows.Close()
			return contracts.UpstreamChannel{}, err
		}
		advanced = append(advanced, plan)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return contracts.UpstreamChannel{}, err
	}
	rows.Close()
	// pgx cannot issue another statement on this transaction while RETURNING
	// rows are still open. Collect every advanced plan first, then supersede
	// its old decision ownership inside the same transaction.
	for _, plan := range advanced {
		if err := supersedeRoutePlanOwnersPostgres(ctx, tx, plan.id, "", plan.generation); err != nil {
			return contracts.UpstreamChannel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.UpstreamChannel{}, err
	}
	return input, nil
}

func (s *PostgresStore) CreateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error) {
	p := input
	if p.ID == "" {
		p.ID = newID("plan")
	}
	if p.Status == "" {
		p.Status = contracts.RoutePlanDraft
	}
	now := nowUTC()
	p.CreatedAt, p.UpdatedAt = now, now
	labels, err := marshalLabels(p.Labels)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.RoutePlan{}, ErrDuplicate
		}
		return contracts.RoutePlan{}, err
	}
	rollout := string(p.Rollout)
	if rollout == "" {
		rollout = string(contracts.RolloutImmediate)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if strings.TrimSpace(p.PoolID) != "" {
		var poolStatus string
		lockErr := tx.QueryRow(ctx, `SELECT status FROM upstream_pools WHERE id=$1 FOR SHARE`, p.PoolID).Scan(&poolStatus)
		if lockErr == nil && contracts.UpstreamPoolStatus(poolStatus) != contracts.UpstreamPoolActive {
			return contracts.RoutePlan{}, ErrConflict
		}
		if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
			return contracts.RoutePlan{}, lockErr
		}
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO route_plans (id, user_id, instance_id, pool_id, tier, status, max_channels, rollout, rollout_batch_size, rollout_canary_count, labels, scheduling_generation, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		p.ID, p.UserID, p.InstanceID, p.PoolID, p.Tier, string(p.Status), p.MaxChannels, rollout, p.RolloutBatchSize, p.RolloutCanaryCount, labels, p.SchedulingGeneration, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.RoutePlan{}, ErrDuplicate
		}
		return contracts.RoutePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RoutePlan{}, err
	}
	return p, nil
}

func scanPlan(row rowScanner) (contracts.RoutePlan, error) {
	var p contracts.RoutePlan
	var status, rollout string
	var labels []byte
	if err := row.Scan(&p.ID, &p.UserID, &p.InstanceID, &p.PoolID, &p.Tier, &status, &p.MaxChannels, &rollout, &p.RolloutBatchSize, &p.RolloutCanaryCount, &labels, &p.SchedulingGeneration, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return contracts.RoutePlan{}, err
	}
	p.Status = contracts.RoutePlanStatus(status)
	p.Rollout = contracts.RolloutMode(rollout)
	p.Labels = unmarshalLabels(labels)
	return p, nil
}

const planCols = `id, user_id, instance_id, pool_id, tier, status, max_channels, rollout, rollout_batch_size, rollout_canary_count, labels, scheduling_generation, created_at, updated_at`

func (s *PostgresStore) GetRoutePlan(ctx context.Context, id string) (contracts.RoutePlan, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1`, id)
	p, err := scanPlan(row)
	if err != nil {
		return contracts.RoutePlan{}, mapNotFound(err)
	}
	return p, nil
}

func (s *PostgresStore) ListRoutePlans(ctx context.Context, userID int64) ([]contracts.RoutePlan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+planCols+` FROM route_plans WHERE ($1=0 OR user_id=$1) ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.RoutePlan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateRoutePlan(ctx context.Context, input contracts.RoutePlan) (contracts.RoutePlan, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return contracts.RoutePlan{}, mapNotFound(err)
	}
	if input.SchedulingGeneration != current.SchedulingGeneration ||
		input.UserID != current.UserID || input.InstanceID != current.InstanceID || input.PoolID != current.PoolID {
		return contracts.RoutePlan{}, ErrConflict
	}
	if routePlanDesiredStateEqual(current, input) {
		if err := tx.Commit(ctx); err != nil {
			return contracts.RoutePlan{}, err
		}
		return current, nil
	}
	input.UpdatedAt = nowUTC()
	labels, err := marshalLabels(input.Labels)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	rollout := string(input.Rollout)
	if rollout == "" {
		rollout = string(contracts.RolloutImmediate)
	}
	row := tx.QueryRow(ctx,
		`UPDATE route_plans SET tier=$2, status=$3, max_channels=$4, rollout=$5, rollout_batch_size=$6, rollout_canary_count=$7,
		 labels=$8, scheduling_generation=scheduling_generation+1, updated_at=$9
		 WHERE id=$1 AND scheduling_generation=$10 RETURNING `+planCols,
		input.ID, input.Tier, string(input.Status), input.MaxChannels, rollout, input.RolloutBatchSize,
		input.RolloutCanaryCount, labels, input.UpdatedAt, input.SchedulingGeneration)
	updated, err := scanPlan(row)
	if err != nil {
		if mapNotFound(err) == ErrNotFound {
			return contracts.RoutePlan{}, ErrConflict
		}
		return contracts.RoutePlan{}, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, input.ID, "", updated.SchedulingGeneration); err != nil {
		return contracts.RoutePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RoutePlan{}, err
	}
	return updated, nil
}

func (s *PostgresStore) CompleteRoutePlanPublish(ctx context.Context, id string, expectedSchedulingGeneration int64) (contracts.RoutePlan, error) {
	if id == "" || expectedSchedulingGeneration <= 0 {
		return contracts.RoutePlan{}, ErrInvalid
	}
	updated, err := scanPlan(s.pool.QueryRow(ctx,
		`UPDATE route_plans SET status='published',updated_at=statement_timestamp()
		 WHERE id=$1 AND status='draft' AND scheduling_generation=$2 RETURNING `+planCols,
		id, expectedSchedulingGeneration))
	if err == nil {
		return updated, nil
	}
	if mapNotFound(err) != ErrNotFound {
		return contracts.RoutePlan{}, err
	}
	if _, getErr := s.GetRoutePlan(ctx, id); getErr != nil {
		return contracts.RoutePlan{}, getErr
	}
	return contracts.RoutePlan{}, ErrConflict
}

func (s *PostgresStore) ClaimRoutePlanScheduling(ctx context.Context, id string, allowedStatuses ...contracts.RoutePlanStatus) (contracts.RoutePlan, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.RoutePlan{}, mapNotFound(err)
	}
	if !routePlanStatusAllowed(current.Status, allowedStatuses) {
		return contracts.RoutePlan{}, ErrConflict
	}
	claimed, err := scanPlan(tx.QueryRow(ctx,
		`UPDATE route_plans SET scheduling_generation=scheduling_generation+1, updated_at=statement_timestamp()
		 WHERE id=$1 RETURNING `+planCols, id))
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, id, "", claimed.SchedulingGeneration); err != nil {
		return contracts.RoutePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RoutePlan{}, err
	}
	return claimed, nil
}

func (s *PostgresStore) TransitionRoutePlanScheduling(ctx context.Context, id string, expected, target contracts.RoutePlanStatus) (contracts.RoutePlan, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.RoutePlan{}, mapNotFound(err)
	}
	if current.Status != expected {
		return contracts.RoutePlan{}, ErrConflict
	}
	transitioned, err := scanPlan(tx.QueryRow(ctx,
		`UPDATE route_plans SET status=$2, scheduling_generation=scheduling_generation+1, updated_at=statement_timestamp()
		 WHERE id=$1 RETURNING `+planCols, id, string(target)))
	if err != nil {
		return contracts.RoutePlan{}, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, id, "", transitioned.SchedulingGeneration); err != nil {
		return contracts.RoutePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RoutePlan{}, err
	}
	return transitioned, nil
}

func (s *PostgresStore) ClaimPlanChannels(ctx context.Context, planID string) ([]contracts.UpstreamChannel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	plan, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planCols+` FROM route_plans WHERE id=$1 FOR UPDATE`, planID))
	if err != nil {
		return nil, mapNotFound(err)
	}
	var poolStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM upstream_pools WHERE id=$1 FOR SHARE`, plan.PoolID).Scan(&poolStatus); err != nil {
		return nil, mapNotFound(err)
	}
	if contracts.UpstreamPoolStatus(poolStatus) != contracts.UpstreamPoolActive {
		return nil, ErrConflict
	}
	// Serialize allocation decisions for the shared catalog. Row locks alone do
	// not protect an empty set, while this transaction-scoped lock ensures two
	// Core workers cannot both select the same currently-unallocated Key.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1162173774)`); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT `+channelCols+` FROM upstream_channels WHERE pool_id=$1 FOR SHARE`, plan.PoolID)
	if err != nil {
		return nil, err
	}
	channels := make([]contracts.UpstreamChannel, 0)
	for rows.Next() {
		channel, scanErr := scanChannel(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	allocationRows, err := tx.Query(ctx, `SELECT channel_id, source_id, user_id FROM upstream_channel_allocations FOR SHARE`)
	if err != nil {
		return nil, err
	}
	view := planChannelAllocationView{
		owners: make(map[string]int64), userSource: make(map[string]struct{}),
	}
	for allocationRows.Next() {
		var channelID, sourceID string
		var userID int64
		if err := allocationRows.Scan(&channelID, &sourceID, &userID); err != nil {
			allocationRows.Close()
			return nil, err
		}
		view.owners[channelID] = userID
		if userID == plan.UserID {
			view.userSource[sourceID] = struct{}{}
		}
	}
	if err := allocationRows.Err(); err != nil {
		allocationRows.Close()
		return nil, err
	}
	allocationRows.Close()

	selected := selectClaimablePlanChannels(channels, plan.MaxChannels, plan.UserID, view)
	for _, channel := range selected {
		if _, exists := view.owners[channel.ID]; !exists {
			if _, err := tx.Exec(ctx,
				`INSERT INTO upstream_channel_allocations (channel_id, source_id, user_id, first_plan_id, created_at)
				 VALUES ($1,$2,$3,$4,statement_timestamp())`,
				channel.ID, channel.SourceIdentity(), plan.UserID, plan.ID); err != nil {
				if isUniqueViolation(err) {
					return nil, ErrConflict
				}
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO published_bindings
			 (id, plan_id, instance_id, channel_id, remote_id, account_ownership,
			  state, last_error, scheduling_generation, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,'',$5,$6,'',$7,statement_timestamp(),statement_timestamp())
			 ON CONFLICT (plan_id, channel_id) DO NOTHING`,
			newID("bind"), plan.ID, plan.InstanceID, channel.ID,
			string(channel.AccountOwnership.Normalize()), string(contracts.BindingPending),
			plan.SchedulingGeneration); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *PostgresStore) UpsertPublishedBinding(ctx context.Context, input contracts.PublishedBinding) (contracts.PublishedBinding, error) {
	b := input
	bindingIDProvided := strings.TrimSpace(b.ID) != ""
	instanceIDProvided := strings.TrimSpace(b.InstanceID) != ""
	if b.ID == "" {
		b.ID = newID("bind")
	}
	if b.State == "" {
		b.State = contracts.BindingPending
	}
	if b.VerificationStatus == "" {
		b.VerificationStatus = contracts.BindingVerificationPublishedPending
		b.VerificationSource = contracts.BindingVerificationSourcePublish
	}
	now := nowUTC()
	b.UpdatedAt = now
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.PublishedBinding{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID, planGeneration int64
	var planInstanceID string
	if err := tx.QueryRow(ctx, `SELECT user_id, instance_id, scheduling_generation FROM route_plans WHERE id=$1 FOR SHARE`, b.PlanID).Scan(&ownerID, &planInstanceID, &planGeneration); err != nil {
		return contracts.PublishedBinding{}, mapNotFound(err)
	}
	if planGeneration > 0 && b.SchedulingGeneration != planGeneration {
		return contracts.PublishedBinding{}, ErrConflict
	}
	if b.InstanceID == "" {
		b.InstanceID = planInstanceID
	} else if planInstanceID != "" && b.InstanceID != planInstanceID {
		return contracts.PublishedBinding{}, ErrConflict
	}
	if bindingIDProvided {
		var boundPlanID, boundChannelID string
		err := tx.QueryRow(ctx, `SELECT plan_id,channel_id FROM published_bindings WHERE id=$1 FOR SHARE`, b.ID).
			Scan(&boundPlanID, &boundChannelID)
		if err == nil && (boundPlanID != b.PlanID || boundChannelID != b.ChannelID) {
			return contracts.PublishedBinding{}, ErrConflict
		}
		if err != nil && !errors.Is(err, pgxNoRows()) {
			return contracts.PublishedBinding{}, err
		}
	}
	var sourceID string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(BTRIM(source_id),''), id)
		   FROM upstream_channels WHERE id=$1 FOR SHARE`,
		b.ChannelID,
	).Scan(&sourceID); err != nil {
		if !errors.Is(err, pgxNoRows()) {
			return contracts.PublishedBinding{}, err
		}
		// Orphan/legacy bindings remain independently allocatable by their
		// concrete channel identity.
		sourceID = b.ChannelID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO upstream_channel_allocations (channel_id, source_id, user_id, first_plan_id, created_at)
		 VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		b.ChannelID, sourceID, ownerID, b.PlanID, b.CreatedAt); err != nil {
		return contracts.PublishedBinding{}, err
	}
	var allocatedOwnerID int64
	if err := tx.QueryRow(ctx,
		`SELECT user_id FROM upstream_channel_allocations WHERE channel_id=$1`, b.ChannelID,
	).Scan(&allocatedOwnerID); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.PublishedBinding{}, ErrDuplicate
		}
		return contracts.PublishedBinding{}, err
	}
	if allocatedOwnerID != ownerID {
		return contracts.PublishedBinding{}, ErrDuplicate
	}

	// The permanent allocation claim and binding upsert share one transaction:
	// neither can survive alone when persistence fails.
	var ownership string
	if err := tx.QueryRow(ctx,
		`SELECT account_ownership FROM upstream_channels WHERE id=$1`, b.ChannelID,
	).Scan(&ownership); err != nil {
		if !errors.Is(err, pgxNoRows()) {
			return contracts.PublishedBinding{}, err
		}
		ownership = string(b.AccountOwnership.Normalize())
	}
	b.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
	if !b.AccountOwnership.Valid() {
		return contracts.PublishedBinding{}, ErrConflict
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO published_bindings (id, plan_id, instance_id, channel_id, remote_id, account_ownership, state, last_error, scheduling_generation,
		 verification_status, verification_source, verified_at, verification_error_code, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 ON CONFLICT (plan_id, channel_id) DO UPDATE SET
		   instance_id=published_bindings.instance_id, remote_id=EXCLUDED.remote_id, state=EXCLUDED.state,
		   account_ownership=published_bindings.account_ownership,
		   last_error=EXCLUDED.last_error, scheduling_generation=EXCLUDED.scheduling_generation,
		   verification_status=CASE
		     WHEN published_bindings.remote_id<>EXCLUDED.remote_id AND published_bindings.scheduling_generation<EXCLUDED.scheduling_generation THEN 'published_pending'
		     WHEN EXCLUDED.verification_status='published_pending' THEN published_bindings.verification_status
		     ELSE EXCLUDED.verification_status END,
		   verification_source=CASE
		     WHEN published_bindings.remote_id<>EXCLUDED.remote_id AND published_bindings.scheduling_generation<EXCLUDED.scheduling_generation THEN 'publish'
		     WHEN EXCLUDED.verification_status='published_pending' THEN published_bindings.verification_source
		     ELSE EXCLUDED.verification_source END,
		   verified_at=CASE
		     WHEN published_bindings.remote_id<>EXCLUDED.remote_id AND published_bindings.scheduling_generation<EXCLUDED.scheduling_generation THEN NULL
		     WHEN EXCLUDED.verification_status='published_pending' THEN published_bindings.verified_at
		     ELSE EXCLUDED.verified_at END,
		   verification_error_code=CASE
		     WHEN published_bindings.remote_id<>EXCLUDED.remote_id AND published_bindings.scheduling_generation<EXCLUDED.scheduling_generation THEN ''
		     WHEN EXCLUDED.verification_status='published_pending' THEN published_bindings.verification_error_code
		     ELSE EXCLUDED.verification_error_code END,
		   updated_at=EXCLUDED.updated_at
		 WHERE published_bindings.scheduling_generation <= EXCLUDED.scheduling_generation
		   AND (NOT $17::boolean OR published_bindings.instance_id=EXCLUDED.instance_id)
		   AND (NOT $16::boolean OR published_bindings.id=EXCLUDED.id)
		   AND (
		     published_bindings.remote_id=EXCLUDED.remote_id OR
		     (EXCLUDED.remote_id<>'' AND (
		       published_bindings.remote_id='' OR
		       published_bindings.scheduling_generation<EXCLUDED.scheduling_generation
		     ))
		   )
		 RETURNING id, verification_status, verification_source, verified_at, verification_error_code, created_at`,
		b.ID, b.PlanID, b.InstanceID, b.ChannelID, b.RemoteID, string(b.AccountOwnership), string(b.State), b.LastError, b.SchedulingGeneration,
		string(b.VerificationStatus), string(b.VerificationSource), b.VerifiedAt, b.VerificationErrorCode, b.CreatedAt, b.UpdatedAt,
		bindingIDProvided, instanceIDProvided)
	var verificationStatus, verificationSource string
	if err := row.Scan(&b.ID, &verificationStatus, &verificationSource, &b.VerifiedAt, &b.VerificationErrorCode, &b.CreatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.PublishedBinding{}, ErrConflict
		}
		if isUniqueViolation(err) || isCheckViolation(err) {
			return contracts.PublishedBinding{}, ErrConflict
		}
		return contracts.PublishedBinding{}, err
	}
	b.VerificationStatus = contracts.PublishedBindingVerificationStatus(verificationStatus)
	b.VerificationSource = contracts.PublishedBindingVerificationSource(verificationSource)
	if err := tx.Commit(ctx); err != nil {
		return contracts.PublishedBinding{}, err
	}
	return b, nil
}

func (s *PostgresStore) ListPublishedBindings(ctx context.Context, planID string) ([]contracts.PublishedBinding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, plan_id, instance_id, channel_id, remote_id, account_ownership, state, last_error, scheduling_generation,
		        verification_status, verification_source, verified_at, verification_error_code, created_at, updated_at
		 FROM published_bindings WHERE ($1='' OR plan_id=$1) ORDER BY created_at`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.PublishedBinding
	for rows.Next() {
		var b contracts.PublishedBinding
		var ownership, state, verificationStatus, verificationSource string
		if err := rows.Scan(&b.ID, &b.PlanID, &b.InstanceID, &b.ChannelID, &b.RemoteID, &ownership, &state, &b.LastError, &b.SchedulingGeneration,
			&verificationStatus, &verificationSource, &b.VerifiedAt, &b.VerificationErrorCode, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
		b.State = contracts.PublishedBindingState(state)
		b.VerificationStatus = contracts.PublishedBindingVerificationStatus(verificationStatus)
		b.VerificationSource = contracts.PublishedBindingVerificationSource(verificationSource)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RecordPublishedBindingVerification(ctx context.Context, planID, channelID string, status contracts.PublishedBindingVerificationStatus, source contracts.PublishedBindingVerificationSource, observedAt time.Time, errorCode string) (contracts.PublishedBinding, error) {
	if !validBindingVerification(status, source) {
		return contracts.PublishedBinding{}, ErrInvalid
	}
	if observedAt.IsZero() {
		observedAt = nowUTC()
	}
	verifiedAt := any(nil)
	if isBindingVerified(status) {
		verifiedAt = observedAt.UTC()
		errorCode = ""
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE published_bindings
		    SET verification_status=$3, verification_source=$4, verified_at=$5,
		        verification_error_code=$6, updated_at=statement_timestamp()
		  WHERE plan_id=$1 AND channel_id=$2
		    AND (verification_status NOT IN ('probe_verified','passive_verified') OR $3 IN ('probe_verified','passive_verified'))
		RETURNING id, plan_id, instance_id, channel_id, remote_id, account_ownership, state, last_error, scheduling_generation,
		          verification_status, verification_source, verified_at, verification_error_code, created_at, updated_at`,
		planID, channelID, string(status), string(source), verifiedAt, errorCode)
	var b contracts.PublishedBinding
	var ownership, state, gotStatus, gotSource string
	if err := row.Scan(&b.ID, &b.PlanID, &b.InstanceID, &b.ChannelID, &b.RemoteID, &ownership, &state, &b.LastError, &b.SchedulingGeneration,
		&gotStatus, &gotSource, &b.VerifiedAt, &b.VerificationErrorCode, &b.CreatedAt, &b.UpdatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			bindings, listErr := s.ListPublishedBindings(ctx, planID)
			if listErr != nil {
				return contracts.PublishedBinding{}, listErr
			}
			for _, existing := range bindings {
				if existing.ChannelID == channelID {
					return existing, nil
				}
			}
			return contracts.PublishedBinding{}, ErrNotFound
		}
		return contracts.PublishedBinding{}, err
	}
	b.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
	b.State = contracts.PublishedBindingState(state)
	b.VerificationStatus = contracts.PublishedBindingVerificationStatus(gotStatus)
	b.VerificationSource = contracts.PublishedBindingVerificationSource(gotSource)
	return b, nil
}

// marshalActions stores reconcile actions as JSONB; nil becomes an empty array
// so the column is never NULL.
func marshalActions(v []contracts.ReconcileAction) []byte {
	if len(v) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func unmarshalActions(raw []byte) []contracts.ReconcileAction {
	if len(raw) == 0 {
		return nil
	}
	var out []contracts.ReconcileAction
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func (s *PostgresStore) AppendReconcileRun(ctx context.Context, input contracts.ReconcileRun) (contracts.ReconcileRun, error) {
	run := input
	if run.ID == "" {
		run.ID = newID("rcrun")
	}
	now := nowUTC()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = now
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.ReconcileRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx,
		`INSERT INTO reconcile_runs (id, plan_id, instance_id, user_id, kind, trigger, actor_type, actor_id, status, actions, error, started_at, finished_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		run.ID, run.PlanID, run.InstanceID, run.UserID, string(run.Kind), string(run.Trigger),
		run.ActorType, run.ActorID, string(run.Status), marshalActions(run.Actions), run.Error, run.StartedAt, run.FinishedAt)
	if err != nil {
		return contracts.ReconcileRun{}, err
	}
	if err := recordOperationalMetricTx(ctx, tx, "reconcile_runs", string(run.Kind), 1); err != nil {
		return contracts.ReconcileRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ReconcileRun{}, err
	}
	return run, nil
}

func scanReconcileRun(row rowScanner) (contracts.ReconcileRun, error) {
	var run contracts.ReconcileRun
	var kind, trigger, status string
	var actions []byte
	if err := row.Scan(&run.ID, &run.PlanID, &run.InstanceID, &run.UserID, &kind, &trigger,
		&run.ActorType, &run.ActorID, &status, &actions, &run.Error, &run.StartedAt, &run.FinishedAt); err != nil {
		return contracts.ReconcileRun{}, err
	}
	run.Kind = contracts.ReconcileRunKind(kind)
	run.Trigger = contracts.ReconcileRunTrigger(trigger)
	run.Status = contracts.ReconcileRunStatus(status)
	run.Actions = unmarshalActions(actions)
	return run, nil
}

func (s *PostgresStore) ListReconcileRuns(ctx context.Context, planID string, limit int) ([]contracts.ReconcileRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, plan_id, instance_id, user_id, kind, trigger, actor_type, actor_id, status, actions, error, started_at, finished_at
		 FROM reconcile_runs WHERE ($1='' OR plan_id=$1) ORDER BY started_at DESC LIMIT $2`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.ReconcileRun
	for rows.Next() {
		run, err := scanReconcileRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
