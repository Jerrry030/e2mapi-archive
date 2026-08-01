package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"e2m.local/contracts"
)

func marshalDryRun(v contracts.ReconcilePlan) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func unmarshalDryRun(raw []byte) contracts.ReconcilePlan {
	var out contracts.ReconcilePlan
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return contracts.ReconcilePlan{}
	}
	return out
}

func (s *PostgresStore) CreateAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error) {
	dec := input
	if dec.ID == "" {
		dec.ID = newID("aswitch")
	}
	now := nowUTC()
	if dec.CreatedAt.IsZero() {
		dec.CreatedAt = now
	}
	if dec.SchedulingGeneration == 0 && dec.PlanID != "" {
		if err := s.pool.QueryRow(ctx, `SELECT scheduling_generation FROM route_plans WHERE id=$1`, dec.PlanID).Scan(&dec.SchedulingGeneration); err != nil {
			return contracts.AutoSwitchDecision{}, mapNotFound(err)
		}
	}
	dec.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auto_switch_decisions
		 (id, user_id, plan_id, instance_id, pool_id, strategy, trigger, trigger_reason,
		  from_channel_id, to_channel_id, risk_level, risk_reason, status, auto_applied,
		  fingerprint, dry_run, error, observation_note, created_at, updated_at,
		  applied_at, observe_until, resolved_at, lease_until, lease_version, scheduling_generation)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		dec.ID, dec.UserID, dec.PlanID, dec.InstanceID, dec.PoolID, string(dec.Strategy), string(dec.Trigger), dec.TriggerReason,
		dec.FromChannelID, dec.ToChannelID, string(dec.RiskLevel), dec.RiskReason, string(dec.Status), dec.AutoApplied,
		dec.Fingerprint, marshalDryRun(dec.DryRunResult), dec.Error, dec.ObservationNote, dec.CreatedAt, dec.UpdatedAt,
		dec.AppliedAt, dec.ObserveUntil, dec.ResolvedAt, dec.LeaseUntil, dec.LeaseVersion, dec.SchedulingGeneration)
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.AutoSwitchDecision{}, ErrDuplicate
		}
		return contracts.AutoSwitchDecision{}, err
	}
	return dec, nil
}

// ClaimAutoSwitchDecision locks the plan before inspecting the active
// fingerprint. Only a real new owner advances the plan generation; a duplicate
// claimant returns the committed owner without superseding it.
func (s *PostgresStore) ClaimAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, bool, error) {
	leaseMicros, err := autoSwitchLeaseMicros(autoSwitchLeaseDuration(input.LeaseUntil, input.CreatedAt))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var generation int64
	if err := tx.QueryRow(ctx,
		`SELECT status, scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, input.PlanID,
	).Scan(&status, &generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	if contracts.RoutePlanStatus(status) != contracts.RoutePlanPublished {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, input.PlanID, "", generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	existing, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions
		 WHERE plan_id=$1 AND fingerprint=$2 AND status IN ('proposed','approved','applying','observing')
		   AND scheduling_generation=$3
		 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, input.PlanID, input.Fingerprint, generation))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return contracts.AutoSwitchDecision{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgxNoRows()) {
		return contracts.AutoSwitchDecision{}, false, err
	}
	generation++
	if _, err := tx.Exec(ctx,
		`UPDATE route_plans SET scheduling_generation=$2, updated_at=statement_timestamp() WHERE id=$1`,
		input.PlanID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, input.PlanID, "", generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	dec := input
	dec.Status = contracts.AutoSwitchApplying
	dec.LeaseVersion = 1
	dec.SchedulingGeneration = generation
	if dec.ID == "" {
		dec.ID = newID("aswitch")
	}
	if dec.CreatedAt.IsZero() {
		dec.CreatedAt = nowUTC()
	}
	dec.UpdatedAt = dec.CreatedAt
	owner, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`INSERT INTO auto_switch_decisions
		 (id, user_id, plan_id, instance_id, pool_id, strategy, trigger, trigger_reason,
		  from_channel_id, to_channel_id, risk_level, risk_reason, status, auto_applied,
		  fingerprint, dry_run, error, observation_note, created_at, updated_at,
		  applied_at, observe_until, resolved_at, lease_until, lease_version, scheduling_generation)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,statement_timestamp(),$20,$21,$22,
		         statement_timestamp()+($23::bigint * interval '1 microsecond'),$24,$25)
		 RETURNING `+autoSwitchColumns,
		dec.ID, dec.UserID, dec.PlanID, dec.InstanceID, dec.PoolID, string(dec.Strategy), string(dec.Trigger), dec.TriggerReason,
		dec.FromChannelID, dec.ToChannelID, string(dec.RiskLevel), dec.RiskReason, string(dec.Status), dec.AutoApplied,
		dec.Fingerprint, marshalDryRun(dec.DryRunResult), dec.Error, dec.ObservationNote, dec.CreatedAt,
		dec.AppliedAt, dec.ObserveUntil, dec.ResolvedAt, leaseMicros, dec.LeaseVersion, dec.SchedulingGeneration))
	if err != nil {
		if isUniqueViolation(err) {
			return contracts.AutoSwitchDecision{}, false, ErrDuplicate
		}
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	return owner, true, nil
}

// ClaimApprovedAutoSwitchDecision serializes operator execution with every
// other scheduling writer for the plan. Approval itself is side-effect free;
// only this fenced claim permits the gateway apply.
func (s *PostgresStore) ClaimApprovedAutoSwitchDecision(ctx context.Context, id string, leaseDuration time.Duration) (contracts.AutoSwitchDecision, bool, error) {
	leaseMicros, err := autoSwitchLeaseMicros(leaseDuration)
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, id).Scan(&planID); err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	var planStatus string
	var generation int64
	if err := tx.QueryRow(ctx,
		`SELECT status, scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID,
	).Scan(&planStatus, &generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	if current.Status != contracts.AutoSwitchApproved {
		if err := tx.Commit(ctx); err != nil {
			return contracts.AutoSwitchDecision{}, false, err
		}
		return current, false, nil
	}
	if contracts.RoutePlanStatus(planStatus) != contracts.RoutePlanPublished || generation != current.SchedulingGeneration {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	generation++
	if _, err := tx.Exec(ctx,
		`UPDATE route_plans SET scheduling_generation=$2, updated_at=statement_timestamp() WHERE id=$1`,
		planID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, planID, current.ID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	claimed, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`UPDATE auto_switch_decisions
		 SET status='applying', updated_at=statement_timestamp(),
		     lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		     lease_version=lease_version+1, scheduling_generation=$3
		 WHERE id=$1 AND status='approved' RETURNING `+autoSwitchColumns,
		id, leaseMicros, generation))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	return claimed, true, nil
}

func (s *PostgresStore) ClaimAutoSwitchObservation(ctx context.Context, input contracts.AutoSwitchDecision, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	leaseMicros, err := autoSwitchLeaseMicros(leaseDuration)
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, input.ID).Scan(&planID); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	var status string
	var generation int64
	if err := tx.QueryRow(ctx,
		`SELECT status, scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID,
	).Scan(&status, &generation); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	if current.PlanID != planID {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	if current.Status != contracts.AutoSwitchObserving || contracts.RoutePlanStatus(status) != contracts.RoutePlanPublished || generation != current.SchedulingGeneration {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	generation++
	if _, err := tx.Exec(ctx, `UPDATE route_plans SET scheduling_generation=$2, updated_at=statement_timestamp() WHERE id=$1`, current.PlanID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, current.PlanID, current.ID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	claimed, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`UPDATE auto_switch_decisions
		 SET status='applying', updated_at=statement_timestamp(),
		     lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		     lease_version=lease_version+1, scheduling_generation=$3
		 WHERE id=$1 AND status='observing' RETURNING `+autoSwitchColumns,
		current.ID, leaseMicros, generation))
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	return claimed, nil
}

func (s *PostgresStore) ClaimExpiredAutoSwitchDecision(ctx context.Context, id string, now, legacyStaleBefore, leaseUntil time.Time) (contracts.AutoSwitchDecision, bool, error) {
	leaseMicros, err := autoSwitchLeaseMicros(leaseUntil.Sub(now))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	legacyAge := now.Sub(legacyStaleBefore)
	if legacyAge < 0 {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, id).Scan(&planID); err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	var planStatus string
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT status, scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&planStatus, &generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, mapNotFound(err)
	}
	if current.PlanID != planID {
		return contracts.AutoSwitchDecision{}, false, ErrConflict
	}
	if current.Status != contracts.AutoSwitchApplying {
		return current, false, nil
	}
	if !routePlanStatusAllowed(contracts.RoutePlanStatus(planStatus), []contracts.RoutePlanStatus{contracts.RoutePlanPublished, contracts.RoutePlanSuspended}) || generation != current.SchedulingGeneration {
		return current, false, nil
	}
	legacyMicros := legacyAge.Microseconds()
	var eligible bool
	if err := tx.QueryRow(ctx,
		`SELECT ((lease_until IS NOT NULL AND lease_until <= statement_timestamp()) OR
		         (lease_until IS NULL AND updated_at <= statement_timestamp()-($2::bigint * interval '1 microsecond')))
		 FROM auto_switch_decisions WHERE id=$1`, id, legacyMicros).Scan(&eligible); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if !eligible {
		return current, false, nil
	}
	generation++
	if _, err := tx.Exec(ctx, `UPDATE route_plans SET scheduling_generation=$2, updated_at=statement_timestamp() WHERE id=$1`, current.PlanID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := supersedeRoutePlanOwnersPostgres(ctx, tx, current.PlanID, current.ID, generation); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	claimed, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`UPDATE auto_switch_decisions
		 SET lease_until=statement_timestamp()+($2::bigint * interval '1 microsecond'),
		     lease_version=lease_version+1, scheduling_generation=$3, updated_at=statement_timestamp()
		 WHERE id=$1 AND status='applying' RETURNING `+autoSwitchColumns,
		id, leaseMicros, generation))
	if err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, false, err
	}
	return claimed, true, nil
}

func (s *PostgresStore) RenewAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64, leaseDuration time.Duration) (contracts.AutoSwitchDecision, error) {
	leaseMicros, err := autoSwitchLeaseMicros(leaseDuration)
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, id).Scan(&planID); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&generation); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	if current.PlanID != planID || current.Status != contracts.AutoSwitchApplying ||
		current.LeaseVersion != leaseVersion || current.LeaseUntil == nil ||
		current.SchedulingGeneration != generation {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	decision, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`UPDATE auto_switch_decisions d
		 SET lease_until=statement_timestamp()+($3::bigint * interval '1 microsecond'),
		     updated_at=statement_timestamp()
		 WHERE d.id=$1 AND d.status='applying' AND d.lease_version=$2
		   AND d.lease_until IS NOT NULL AND d.lease_until > statement_timestamp()
		 RETURNING `+prefixedAutoSwitchColumns("d"), id, leaseVersion, leaseMicros))
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		return contracts.AutoSwitchDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	return decision, nil
}

func (s *PostgresStore) ReleaseAutoSwitchDecisionLease(ctx context.Context, id string, leaseVersion int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, id).Scan(&planID); err != nil {
		return mapNotFound(err)
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&generation); err != nil {
		return mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return mapNotFound(err)
	}
	if current.PlanID != planID || current.Status != contracts.AutoSwitchApplying ||
		current.LeaseVersion != leaseVersion || current.LeaseUntil == nil ||
		current.SchedulingGeneration != generation {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx,
		`UPDATE auto_switch_decisions
		 SET lease_until=statement_timestamp(), updated_at=statement_timestamp()
		 WHERE id=$1 AND status='applying' AND lease_version=$2
		   AND lease_until IS NOT NULL AND lease_until > statement_timestamp()`, id, leaseVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func scanAutoSwitchDecision(row rowScanner) (contracts.AutoSwitchDecision, error) {
	var d contracts.AutoSwitchDecision
	var strategy, trigger, risk, status string
	var dryRun []byte
	if err := row.Scan(&d.ID, &d.UserID, &d.PlanID, &d.InstanceID, &d.PoolID, &strategy, &trigger, &d.TriggerReason,
		&d.FromChannelID, &d.ToChannelID, &risk, &d.RiskReason, &status, &d.AutoApplied,
		&d.Fingerprint, &dryRun, &d.Error, &d.ObservationNote, &d.CreatedAt, &d.UpdatedAt,
		&d.AppliedAt, &d.ObserveUntil, &d.ResolvedAt, &d.LeaseUntil, &d.LeaseVersion, &d.SchedulingGeneration); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	d.Strategy = contracts.RouteStrategyType(strategy)
	d.Trigger = contracts.ReconcileRunTrigger(trigger)
	d.RiskLevel = contracts.RiskLevel(risk)
	d.Status = contracts.AutoSwitchStatus(status)
	d.DryRunResult = unmarshalDryRun(dryRun)
	return d, nil
}

const autoSwitchColumns = `id, user_id, plan_id, instance_id, pool_id, strategy, trigger, trigger_reason,
	from_channel_id, to_channel_id, risk_level, risk_reason, status, auto_applied,
	fingerprint, dry_run, error, observation_note, created_at, updated_at,
	applied_at, observe_until, resolved_at, lease_until, lease_version, scheduling_generation`

func prefixedAutoSwitchColumns(alias string) string {
	return alias + `.id, ` + alias + `.user_id, ` + alias + `.plan_id, ` + alias + `.instance_id, ` + alias + `.pool_id, ` +
		alias + `.strategy, ` + alias + `.trigger, ` + alias + `.trigger_reason, ` + alias + `.from_channel_id, ` +
		alias + `.to_channel_id, ` + alias + `.risk_level, ` + alias + `.risk_reason, ` + alias + `.status, ` +
		alias + `.auto_applied, ` + alias + `.fingerprint, ` + alias + `.dry_run, ` + alias + `.error, ` +
		alias + `.observation_note, ` + alias + `.created_at, ` + alias + `.updated_at, ` + alias + `.applied_at, ` +
		alias + `.observe_until, ` + alias + `.resolved_at, ` + alias + `.lease_until, ` + alias + `.lease_version, ` +
		alias + `.scheduling_generation`
}

func (s *PostgresStore) GetAutoSwitchDecision(ctx context.Context, id string) (contracts.AutoSwitchDecision, error) {
	d, err := scanAutoSwitchDecision(s.pool.QueryRow(ctx, `SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1`, id))
	if err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	return d, nil
}

func (s *PostgresStore) UpdateAutoSwitchDecision(context.Context, contracts.AutoSwitchDecision) (contracts.AutoSwitchDecision, error) {
	return contracts.AutoSwitchDecision{}, ErrConflict
}

func (s *PostgresStore) TransitionAutoSwitchDecision(ctx context.Context, input contracts.AutoSwitchDecision, expected contracts.AutoSwitchStatus) (contracts.AutoSwitchDecision, error) {
	if input.Status == contracts.AutoSwitchApplying {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var planID string
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM auto_switch_decisions WHERE id=$1`, input.ID).Scan(&planID); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT scheduling_generation FROM route_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&generation); err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	current, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	if current.PlanID != planID || current.Status != expected ||
		expected == contracts.AutoSwitchApplying && (current.LeaseVersion != input.LeaseVersion ||
			current.LeaseUntil == nil || generation != current.SchedulingGeneration) {
		return contracts.AutoSwitchDecision{}, ErrConflict
	}
	dec, err := scanAutoSwitchDecision(tx.QueryRow(ctx,
		`UPDATE auto_switch_decisions d SET
		 user_id=$2, instance_id=$3, pool_id=$4, strategy=$5, trigger=$6, trigger_reason=$7,
		 from_channel_id=$8, to_channel_id=$9, risk_level=$10, risk_reason=$11, status=$12, auto_applied=$13,
		 fingerprint=$14, dry_run=$15, error=$16, observation_note=$17, updated_at=statement_timestamp(),
		 applied_at=$18, observe_until=$19, resolved_at=$20, lease_until=$21
		 WHERE d.id=$1 AND d.status=$23
		   AND ($23 <> 'applying' OR (d.lease_version=$22 AND d.lease_until IS NOT NULL AND d.lease_until>statement_timestamp()))
		 RETURNING `+prefixedAutoSwitchColumns("d"),
		input.ID, input.UserID, input.InstanceID, input.PoolID, string(input.Strategy), string(input.Trigger), input.TriggerReason,
		input.FromChannelID, input.ToChannelID, string(input.RiskLevel), input.RiskReason, string(input.Status), input.AutoApplied,
		input.Fingerprint, marshalDryRun(input.DryRunResult), input.Error, input.ObservationNote,
		input.AppliedAt, input.ObserveUntil, input.ResolvedAt, input.LeaseUntil, input.LeaseVersion, string(expected)))
	if err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.AutoSwitchDecision{}, ErrConflict
		}
		return contracts.AutoSwitchDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.AutoSwitchDecision{}, err
	}
	return dec, nil
}

func (s *PostgresStore) ListAutoSwitchDecisions(ctx context.Context, filter contracts.AutoSwitchDecisionFilter) ([]contracts.AutoSwitchDecision, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	statuses := make([]string, 0, len(filter.Statuses))
	for _, status := range filter.Statuses {
		statuses = append(statuses, string(status))
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+autoSwitchColumns+` FROM auto_switch_decisions
		 WHERE ($1='' OR plan_id=$1) AND ($2='' OR instance_id=$2) AND ($3='' OR pool_id=$3)
		   AND ($4=0 OR user_id=$4) AND (COALESCE(cardinality($5::text[]), 0)=0 OR status=ANY($5::text[]))
		 ORDER BY created_at DESC LIMIT $6`,
		filter.PlanID, filter.InstanceID, filter.PoolID, filter.UserID, statuses, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.AutoSwitchDecision
	for rows.Next() {
		d, err := scanAutoSwitchDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) FindActiveAutoSwitchDecisionByFingerprint(ctx context.Context, planID, fingerprint string) (contracts.AutoSwitchDecision, error) {
	d, err := scanAutoSwitchDecision(s.pool.QueryRow(ctx,
		`SELECT `+prefixedAutoSwitchColumns("d")+` FROM auto_switch_decisions d
		 JOIN route_plans p ON p.id=d.plan_id AND p.scheduling_generation=d.scheduling_generation
		 WHERE d.plan_id=$1 AND d.fingerprint=$2 AND d.status IN ('proposed','approved','applying','observing')
		 ORDER BY d.created_at DESC LIMIT 1`, planID, fingerprint))
	if err != nil {
		return contracts.AutoSwitchDecision{}, mapNotFound(err)
	}
	return d, nil
}
