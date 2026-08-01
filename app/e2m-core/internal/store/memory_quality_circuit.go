package store

import (
	"context"
	"math"
	"sort"
	"time"

	"e2m.local/contracts"
)

func (s *MemoryStore) GetQualityCircuitRuntime(ctx context.Context, planID, channelID string) (contracts.QualityCircuitRuntime, error) {
	if err := ctx.Err(); err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rt := range s.qualityCircuits {
		if rt.PlanID == planID && rt.ChannelID == channelID {
			return copyQualityCircuitRuntime(rt), nil
		}
	}
	return contracts.QualityCircuitRuntime{}, ErrNotFound
}

func (s *MemoryStore) ListQualityCircuitRuntimes(ctx context.Context, filter contracts.QualityCircuitRuntimeFilter) ([]contracts.QualityCircuitRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]contracts.QualityCircuitRuntime, 0, len(s.qualityCircuits))
	for _, rt := range s.qualityCircuits {
		if filter.Matches(rt) {
			out = append(out, copyQualityCircuitRuntime(rt))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].ProbeAfter, out[j].ProbeAfter
		switch {
		case left != nil && right != nil && !left.Equal(*right):
			return left.Before(*right)
		case left != nil && right == nil:
			return true
		case left == nil && right != nil:
			return false
		case out[i].PlanID != out[j].PlanID:
			return out[i].PlanID < out[j].PlanID
		default:
			return out[i].ChannelID < out[j].ChannelID
		}
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// UpsertQualityCircuitRuntime atomically creates or advances one scoped state.
// expectedVersion=0 is create-only; existing rows always require their current
// positive version so a stale scheduler cannot overwrite a newer transition.
func (s *MemoryStore) UpsertQualityCircuitRuntime(ctx context.Context, input contracts.QualityCircuitRuntime, expectedVersion int64) (contracts.QualityCircuitRuntime, error) {
	if err := ctx.Err(); err != nil {
		return contracts.QualityCircuitRuntime{}, err
	}
	if !validQualityCircuitRuntime(input, expectedVersion) {
		return contracts.QualityCircuitRuntime{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for i, current := range s.qualityCircuits {
		if current.PlanID != input.PlanID || current.ChannelID != input.ChannelID {
			continue
		}
		if expectedVersion == 0 || current.Version != expectedVersion {
			return contracts.QualityCircuitRuntime{}, ErrConflict
		}
		updated := copyQualityCircuitRuntime(input)
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = now
		updated.Version = current.Version + 1
		s.qualityCircuits[i] = updated
		return copyQualityCircuitRuntime(updated), nil
	}
	if expectedVersion != 0 {
		return contracts.QualityCircuitRuntime{}, ErrConflict
	}
	created := copyQualityCircuitRuntime(input)
	created.Version = 1
	created.CreatedAt = now
	created.UpdatedAt = now
	s.qualityCircuits = append(s.qualityCircuits, created)
	return copyQualityCircuitRuntime(created), nil
}

func validQualityCircuitRuntime(rt contracts.QualityCircuitRuntime, expectedVersion int64) bool {
	return rt.PlanID != "" && rt.ChannelID != "" && rt.State.Valid() && expectedVersion >= 0 &&
		rt.OpenCount >= 0 && rt.ConsecutiveProbeSuccesses >= 0 && !math.IsNaN(rt.LastScore) &&
		!math.IsInf(rt.LastScore, 0) && rt.LastScore >= 0 && rt.LastScore <= 100 &&
		validRecoveryStage(rt.RecoveryStage)
}

func validRecoveryStage(stage int) bool {
	switch stage {
	case 0, 10, 25, 50, 100:
		return true
	default:
		return false
	}
}

func copyQualityCircuitRuntime(rt contracts.QualityCircuitRuntime) contracts.QualityCircuitRuntime {
	rt.OpenedAt = copyTimePointer(rt.OpenedAt)
	rt.ProbeAfter = copyTimePointer(rt.ProbeAfter)
	rt.HalfOpenSince = copyTimePointer(rt.HalfOpenSince)
	rt.LastProbeAt = copyTimePointer(rt.LastProbeAt)
	rt.LastTransitionAt = copyTimePointer(rt.LastTransitionAt)
	rt.RecoveryStageStartedAt = copyTimePointer(rt.RecoveryStageStartedAt)
	rt.RecoveryObserveAfter = copyTimePointer(rt.RecoveryObserveAfter)
	return rt
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
