package store

import (
	"context"
	"math"
	"strings"
	"time"

	"e2m.local/contracts"
)

// RecommendationRolloutExecutionStatsFilter identifies the exact opt-in
// scope whose execution budget is being evaluated. Since is inclusive and
// must be supplied by the caller (normally the current UTC day boundary).
type RecommendationRolloutExecutionStatsFilter struct {
	UserID int64
	Scope  contracts.RecommendationExecutionScope
	PlanID string
	PoolID string
	Since  time.Time
	// ExcludeRolloutID removes the currently revalidated rollout from both the
	// daily count and cooldown timestamp. Empty is used before Start.
	ExcludeRolloutID string
}

// RecommendationRolloutExecutionStats counts starts, not successful
// completions. A failed rollout consumes the same budget as a successful one,
// so failures cannot be retried for free around the daily cap.
type RecommendationRolloutExecutionStats struct {
	Count         int
	LastStartedAt *time.Time
}

type RecommendationRolloutExecutionStatsStore interface {
	GetRecommendationRolloutExecutionStats(context.Context, RecommendationRolloutExecutionStatsFilter) (RecommendationRolloutExecutionStats, error)
}

var (
	_ RecommendationRolloutExecutionStatsStore = (*MemoryStore)(nil)
	_ RecommendationRolloutExecutionStatsStore = (*PostgresStore)(nil)
)

func (s *MemoryStore) GetRecommendationRolloutExecutionStats(ctx context.Context, filter RecommendationRolloutExecutionStatsFilter) (RecommendationRolloutExecutionStats, error) {
	if err := ctx.Err(); err != nil {
		return RecommendationRolloutExecutionStats{}, err
	}
	if !validRecommendationRolloutExecutionStatsFilter(filter) {
		return RecommendationRolloutExecutionStats{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	planPools := make(map[string]string)
	targetOwned := false
	for _, plan := range s.routePlans {
		if plan.UserID == filter.UserID {
			planPools[plan.ID] = plan.PoolID
			if filter.Scope == contracts.RecommendationExecutionScopePlan && plan.ID == filter.PlanID ||
				filter.Scope == contracts.RecommendationExecutionScopePool && plan.PoolID == filter.PoolID {
				targetOwned = true
			}
		}
	}
	if !targetOwned {
		return RecommendationRolloutExecutionStats{}, ErrNotFound
	}
	result := RecommendationRolloutExecutionStats{}
	for _, rollout := range s.recommendationRollouts {
		if rollout.State.UserID != filter.UserID || rollout.CreatedAt.Before(filter.Since) || rollout.State.ID == filter.ExcludeRolloutID {
			continue
		}
		if filter.Scope == contracts.RecommendationExecutionScopePlan && rollout.State.PlanID != filter.PlanID {
			continue
		}
		if filter.Scope == contracts.RecommendationExecutionScopePool && planPools[rollout.State.PlanID] != filter.PoolID {
			continue
		}
		result.Count++
		if result.LastStartedAt == nil || rollout.CreatedAt.After(*result.LastStartedAt) {
			started := rollout.CreatedAt
			result.LastStartedAt = &started
		}
	}
	return result, nil
}

func (s *PostgresStore) GetRecommendationRolloutExecutionStats(ctx context.Context, filter RecommendationRolloutExecutionStatsFilter) (RecommendationRolloutExecutionStats, error) {
	if !validRecommendationRolloutExecutionStatsFilter(filter) {
		return RecommendationRolloutExecutionStats{}, ErrInvalid
	}
	var result RecommendationRolloutExecutionStats
	var targetOwned bool
	var count int64
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM route_plans AS owned
			WHERE owned.user_id=$1 AND (($3='plan' AND owned.id=$4) OR ($3='pool' AND owned.pool_id=$5))),
		count(*)::bigint,max(rollout.created_at)
		FROM recommendation_rollouts AS rollout
		JOIN route_plans AS plan ON plan.id=rollout.plan_id AND plan.user_id=rollout.user_id
		WHERE rollout.user_id=$1 AND rollout.created_at >= $2 AND ($6='' OR rollout.id<>$6)
		  AND (($3='plan' AND rollout.plan_id=$4) OR ($3='pool' AND plan.pool_id=$5))`,
		filter.UserID, filter.Since, string(filter.Scope), filter.PlanID, filter.PoolID, filter.ExcludeRolloutID).Scan(&targetOwned, &count, &last)
	if err != nil {
		return RecommendationRolloutExecutionStats{}, err
	}
	if !targetOwned {
		return RecommendationRolloutExecutionStats{}, ErrNotFound
	}
	if count < 0 || count > math.MaxInt {
		return RecommendationRolloutExecutionStats{}, ErrConflict
	}
	result.Count = int(count)
	if last != nil {
		normalized := normalizeRecommendationRolloutTime(*last)
		result.LastStartedAt = &normalized
	}
	return result, nil
}

func validRecommendationRolloutExecutionStatsFilter(filter RecommendationRolloutExecutionStatsFilter) bool {
	if filter.UserID <= 0 || filter.Since.IsZero() || !contracts.IsRecommendationExecutionScope(filter.Scope) {
		return false
	}
	planID, poolID, excludeID := strings.TrimSpace(filter.PlanID), strings.TrimSpace(filter.PoolID), strings.TrimSpace(filter.ExcludeRolloutID)
	if planID != filter.PlanID || poolID != filter.PoolID || excludeID != filter.ExcludeRolloutID ||
		excludeID != "" && !validRecommendationExecutionIdentifier(excludeID, maxRecommendationExecutionTargetIDLength, false) {
		return false
	}
	switch filter.Scope {
	case contracts.RecommendationExecutionScopePlan:
		return validRecommendationExecutionIdentifier(planID, maxRecommendationExecutionTargetIDLength, false) && poolID == ""
	case contracts.RecommendationExecutionScopePool:
		return validRecommendationExecutionIdentifier(poolID, maxRecommendationExecutionTargetIDLength, false) && planID == ""
	default:
		return false
	}
}
