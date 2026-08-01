package store

import (
	"context"

	"e2m.local/contracts"
)

const qualityCircuitColumns = `plan_id, channel_id, state,
	opened_at, probe_after, half_open_since, last_probe_at, last_transition_at,
	open_count, consecutive_probe_successes, last_score,
	last_reason_code, last_reason_text, restore_pending,
	recovery_ready, recovery_stage, recovery_stage_started_at, recovery_observe_after,
	version, created_at, updated_at`

func scanQualityCircuitRuntime(row rowScanner) (contracts.QualityCircuitRuntime, error) {
	var rt contracts.QualityCircuitRuntime
	var state string
	if err := row.Scan(
		&rt.PlanID, &rt.ChannelID, &state,
		&rt.OpenedAt, &rt.ProbeAfter, &rt.HalfOpenSince, &rt.LastProbeAt, &rt.LastTransitionAt,
		&rt.OpenCount, &rt.ConsecutiveProbeSuccesses, &rt.LastScore,
		&rt.LastReason.Code, &rt.LastReason.Text, &rt.RestorePending,
		&rt.RecoveryReady, &rt.RecoveryStage, &rt.RecoveryStageStartedAt, &rt.RecoveryObserveAfter,
		&rt.Version, &rt.CreatedAt, &rt.UpdatedAt,
	); err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	rt.State = contracts.QualityCircuitState(state)
	return rt, nil
}

func (s *PostgresStore) GetQualityCircuitRuntime(ctx context.Context, planID, channelID string) (contracts.QualityCircuitRuntime, error) {
	rt, err := scanQualityCircuitRuntime(s.pool.QueryRow(ctx,
		`SELECT `+qualityCircuitColumns+` FROM quality_circuit_runtimes WHERE plan_id=$1 AND channel_id=$2`,
		planID, channelID))
	if err != nil {
		return contracts.QualityCircuitRuntime{}, mapNotFound(err)
	}
	return rt, nil
}

func (s *PostgresStore) ListQualityCircuitRuntimes(ctx context.Context, filter contracts.QualityCircuitRuntimeFilter) ([]contracts.QualityCircuitRuntime, error) {
	states := make([]string, 0, len(filter.States))
	for _, state := range filter.States {
		states = append(states, string(state))
	}
	limit := filter.Limit
	if limit < 0 {
		limit = 0
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+qualityCircuitColumns+` FROM quality_circuit_runtimes
		 WHERE ($1='' OR plan_id=$1)
		   AND ($2='' OR channel_id=$2)
		   AND (COALESCE(cardinality($3::text[]), 0)=0 OR state=ANY($3::text[]))
		   AND ($4::boolean=false OR (probe_after IS NOT NULL AND probe_after <= $5))
		 ORDER BY probe_after ASC NULLS LAST, plan_id, channel_id
		 LIMIT NULLIF($6, 0)`,
		filter.PlanID, filter.ChannelID, states, !filter.ProbeDueBefore.IsZero(), filter.ProbeDueBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.QualityCircuitRuntime, 0)
	for rows.Next() {
		rt, scanErr := scanQualityCircuitRuntime(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// UpsertQualityCircuitRuntime uses Version as an optimistic concurrency token.
// A zero expected version is create-only; positive versions update exactly one
// existing row and atomically increment its token.
func (s *PostgresStore) UpsertQualityCircuitRuntime(ctx context.Context, input contracts.QualityCircuitRuntime, expectedVersion int64) (contracts.QualityCircuitRuntime, error) {
	if !validQualityCircuitRuntime(input, expectedVersion) {
		return contracts.QualityCircuitRuntime{}, ErrConflict
	}
	if expectedVersion == 0 {
		now := nowUTC()
		rt, err := scanQualityCircuitRuntime(s.pool.QueryRow(ctx,
			`INSERT INTO quality_circuit_runtimes
			 (plan_id, channel_id, state, opened_at, probe_after, half_open_since, last_probe_at, last_transition_at,
			  open_count, consecutive_probe_successes, last_score, last_reason_code, last_reason_text,
			  restore_pending, recovery_ready, recovery_stage, recovery_stage_started_at, recovery_observe_after,
			  version, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,1,$19,$19)
			 ON CONFLICT (plan_id, channel_id) DO NOTHING
			 RETURNING `+qualityCircuitColumns,
			input.PlanID, input.ChannelID, string(input.State), input.OpenedAt, input.ProbeAfter,
			input.HalfOpenSince, input.LastProbeAt, input.LastTransitionAt, input.OpenCount,
			input.ConsecutiveProbeSuccesses, input.LastScore, input.LastReason.Code, input.LastReason.Text,
			input.RestorePending, input.RecoveryReady, input.RecoveryStage,
			input.RecoveryStageStartedAt, input.RecoveryObserveAfter, now))
		if err != nil {
			if mapNotFound(err) == ErrNotFound {
				return contracts.QualityCircuitRuntime{}, ErrConflict
			}
			if isUniqueViolation(err) {
				return contracts.QualityCircuitRuntime{}, ErrConflict
			}
			return contracts.QualityCircuitRuntime{}, err
		}
		return rt, nil
	}

	rt, err := scanQualityCircuitRuntime(s.pool.QueryRow(ctx,
		`UPDATE quality_circuit_runtimes SET
		   state=$3, opened_at=$4, probe_after=$5, half_open_since=$6,
		   last_probe_at=$7, last_transition_at=$8, open_count=$9,
		   consecutive_probe_successes=$10, last_score=$11,
		   last_reason_code=$12, last_reason_text=$13,
		   restore_pending=$14, recovery_ready=$15, recovery_stage=$16,
		   recovery_stage_started_at=$17, recovery_observe_after=$18,
		   version=version+1, updated_at=now()
		 WHERE plan_id=$1 AND channel_id=$2 AND version=$19
		 RETURNING `+qualityCircuitColumns,
		input.PlanID, input.ChannelID, string(input.State), input.OpenedAt, input.ProbeAfter,
		input.HalfOpenSince, input.LastProbeAt, input.LastTransitionAt, input.OpenCount,
		input.ConsecutiveProbeSuccesses, input.LastScore, input.LastReason.Code, input.LastReason.Text,
		input.RestorePending, input.RecoveryReady, input.RecoveryStage,
		input.RecoveryStageStartedAt, input.RecoveryObserveAfter, expectedVersion))
	if err != nil {
		if mapNotFound(err) == ErrNotFound {
			return contracts.QualityCircuitRuntime{}, ErrConflict
		}
		return contracts.QualityCircuitRuntime{}, err
	}
	return rt, nil
}
