package store

import (
	"context"
	"errors"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) GetInstanceMonitorPolicy(ctx context.Context, instanceID string) (contracts.InstanceMonitorPolicy, error) {
	var policy contracts.InstanceMonitorPolicy
	err := s.pool.QueryRow(ctx,
		`SELECT p.instance_id, p.user_id, p.enabled, p.check_interval_seconds,
		        p.fail_streak, p.auto_switch, p.cooldown_seconds,
		        p.drift_detection, p.updated_at
		 FROM instance_monitor_policies p WHERE p.instance_id=$1`, instanceID).Scan(
		&policy.InstanceID, &policy.UserID, &policy.Enabled, &policy.CheckIntervalSeconds,
		&policy.FailStreak, &policy.AutoSwitch, &policy.CooldownSeconds,
		&policy.DriftDetection, &policy.UpdatedAt)
	if err == nil {
		return policy, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.InstanceMonitorPolicy{}, err
	}
	var userID int64
	if err := s.pool.QueryRow(ctx, `SELECT user_id FROM instances WHERE id=$1`, instanceID).Scan(&userID); err != nil {
		return contracts.InstanceMonitorPolicy{}, mapNotFound(err)
	}
	return contracts.DefaultInstanceMonitorPolicy(instanceID, userID), nil
}

func (s *PostgresStore) UpsertInstanceMonitorPolicy(ctx context.Context, input contracts.InstanceMonitorPolicy) (contracts.InstanceMonitorPolicy, error) {
	var policy contracts.InstanceMonitorPolicy
	err := s.pool.QueryRow(ctx,
		`INSERT INTO instance_monitor_policies
		 (instance_id, user_id, enabled, check_interval_seconds, fail_streak,
		  auto_switch, cooldown_seconds, drift_detection, updated_at)
		 SELECT id, user_id, $3, $4, $5, $6, $7, $8, now()
		 FROM instances WHERE id=$1 AND ($2=0 OR user_id=$2)
		 ON CONFLICT (instance_id) DO UPDATE SET
		   user_id=EXCLUDED.user_id, enabled=EXCLUDED.enabled,
		   check_interval_seconds=EXCLUDED.check_interval_seconds,
		   fail_streak=EXCLUDED.fail_streak, auto_switch=EXCLUDED.auto_switch,
		   cooldown_seconds=EXCLUDED.cooldown_seconds,
		   drift_detection=EXCLUDED.drift_detection, updated_at=now()
		 RETURNING instance_id, user_id, enabled, check_interval_seconds,
		           fail_streak, auto_switch, cooldown_seconds, drift_detection, updated_at`,
		input.InstanceID, input.UserID, input.Enabled, input.CheckIntervalSeconds,
		input.FailStreak, input.AutoSwitch, input.CooldownSeconds, input.DriftDetection).Scan(
		&policy.InstanceID, &policy.UserID, &policy.Enabled, &policy.CheckIntervalSeconds,
		&policy.FailStreak, &policy.AutoSwitch, &policy.CooldownSeconds,
		&policy.DriftDetection, &policy.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.InstanceMonitorPolicy{}, ErrNotFound
	}
	return policy, err
}
