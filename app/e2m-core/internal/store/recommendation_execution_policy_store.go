package store

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
)

const (
	maxRecommendationExecutionPolicyIDLength  = 256
	maxRecommendationExecutionTargetIDLength  = 256
	maxRecommendationExecutionDailyCap        = 1<<31 - 1
	maxRecommendationExecutionCooldownSeconds = 365 * 24 * 60 * 60
)

// RecommendationExecutionPolicyStore is the narrow persistence boundary for
// an owner's explicit intelligence auto-apply opt-ins. A missing row is
// equivalent to disabled; the store never synthesizes an enabled policy.
type RecommendationExecutionPolicyStore interface {
	GetRecommendationExecutionPolicy(context.Context, int64, contracts.RecommendationExecutionScope, string) (contracts.RecommendationExecutionPolicy, error)
	ListRecommendationExecutionPolicies(context.Context, int64) ([]contracts.RecommendationExecutionPolicy, error)
	UpsertRecommendationExecutionPolicy(context.Context, contracts.RecommendationExecutionPolicy, int64) (contracts.RecommendationExecutionPolicy, error)
}

var (
	_ RecommendationExecutionPolicyStore = (*MemoryStore)(nil)
	_ RecommendationExecutionPolicyStore = (*PostgresStore)(nil)
)

// GetRecommendationExecutionPolicy resolves exactly one owner and semantic
// target. Callers cannot look up a policy by its globally opaque ID and then
// accidentally cross an owner boundary.
func (s *MemoryStore) GetRecommendationExecutionPolicy(ctx context.Context, userID int64, scope contracts.RecommendationExecutionScope, targetID string) (contracts.RecommendationExecutionPolicy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationExecutionPolicy{}, err
	}
	key, valid := recommendationExecutionPolicyKey(userID, scope, targetID)
	if !valid {
		return contracts.RecommendationExecutionPolicy{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, policy := range s.recommendationExecutionPolicies {
		if storedKey, ok := recommendationExecutionPolicyInputKey(policy); ok && storedKey == key {
			return cloneRecommendationExecutionPolicy(policy), nil
		}
	}
	return contracts.RecommendationExecutionPolicy{}, ErrNotFound
}

func (s *MemoryStore) ListRecommendationExecutionPolicies(ctx context.Context, userID int64) ([]contracts.RecommendationExecutionPolicy, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.RecommendationExecutionPolicy, 0)
	for _, policy := range s.recommendationExecutionPolicies {
		if policy.UserID == userID {
			out = append(out, cloneRecommendationExecutionPolicy(policy))
		}
	}
	sortRecommendationExecutionPolicies(out)
	return out, nil
}

// UpsertRecommendationExecutionPolicy is a version compare-and-swap.
// expectedVersion=0 is create-only; a positive version updates exactly the
// same owner+scope target and preserves its server-issued identity.
func (s *MemoryStore) UpsertRecommendationExecutionPolicy(ctx context.Context, input contracts.RecommendationExecutionPolicy, expectedVersion int64) (contracts.RecommendationExecutionPolicy, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RecommendationExecutionPolicy{}, err
	}
	key, valid := recommendationExecutionPolicyInputKey(input)
	if !validRecommendationExecutionPolicyInput(input, expectedVersion) || !valid {
		return contracts.RecommendationExecutionPolicy{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !memoryRecommendationExecutionTargetOwned(s, input) {
		return contracts.RecommendationExecutionPolicy{}, ErrConflict
	}

	for i, current := range s.recommendationExecutionPolicies {
		currentKey, ok := recommendationExecutionPolicyInputKey(current)
		if !ok || currentKey != key {
			continue
		}
		if expectedVersion == 0 || current.Version != expectedVersion || input.ID != "" && input.ID != current.ID {
			return contracts.RecommendationExecutionPolicy{}, ErrConflict
		}
		updated := cloneRecommendationExecutionPolicy(input)
		updated.ID = current.ID
		updated.Version = current.Version + 1
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = normalizeRecommendationExecutionPolicyTime(s.now())
		s.recommendationExecutionPolicies[i] = cloneRecommendationExecutionPolicy(updated)
		return cloneRecommendationExecutionPolicy(updated), nil
	}
	if expectedVersion != 0 {
		return contracts.RecommendationExecutionPolicy{}, ErrConflict
	}

	created := cloneRecommendationExecutionPolicy(input)
	if created.ID == "" {
		created.ID = s.nextID("rec-policy")
	}
	for _, current := range s.recommendationExecutionPolicies {
		if current.ID == created.ID {
			return contracts.RecommendationExecutionPolicy{}, ErrConflict
		}
	}
	now := normalizeRecommendationExecutionPolicyTime(s.now())
	created.Version = 1
	created.CreatedAt = now
	created.UpdatedAt = now
	s.recommendationExecutionPolicies = append(s.recommendationExecutionPolicies, cloneRecommendationExecutionPolicy(created))
	return cloneRecommendationExecutionPolicy(created), nil
}

type recommendationExecutionPolicyScopeKey struct {
	userID   int64
	scope    contracts.RecommendationExecutionScope
	targetID string
}

func recommendationExecutionPolicyInputKey(policy contracts.RecommendationExecutionPolicy) (recommendationExecutionPolicyScopeKey, bool) {
	switch policy.Scope {
	case contracts.RecommendationExecutionScopePlan:
		if policy.PoolID != "" {
			return recommendationExecutionPolicyScopeKey{}, false
		}
		return recommendationExecutionPolicyKey(policy.UserID, policy.Scope, policy.PlanID)
	case contracts.RecommendationExecutionScopePool:
		if policy.PlanID != "" {
			return recommendationExecutionPolicyScopeKey{}, false
		}
		return recommendationExecutionPolicyKey(policy.UserID, policy.Scope, policy.PoolID)
	default:
		return recommendationExecutionPolicyScopeKey{}, false
	}
}

func recommendationExecutionPolicyKey(userID int64, scope contracts.RecommendationExecutionScope, targetID string) (recommendationExecutionPolicyScopeKey, bool) {
	if userID <= 0 || !contracts.IsRecommendationExecutionScope(scope) || !validRecommendationExecutionIdentifier(targetID, maxRecommendationExecutionTargetIDLength, false) {
		return recommendationExecutionPolicyScopeKey{}, false
	}
	return recommendationExecutionPolicyScopeKey{userID: userID, scope: scope, targetID: targetID}, true
}

func validRecommendationExecutionPolicyInput(policy contracts.RecommendationExecutionPolicy, expectedVersion int64) bool {
	if expectedVersion < 0 || !validRecommendationExecutionIdentifier(policy.ID, maxRecommendationExecutionPolicyIDLength, true) ||
		policy.Version != 0 || !policy.CreatedAt.IsZero() || !policy.UpdatedAt.IsZero() ||
		policy.DailyExecutionCap <= 0 || policy.DailyExecutionCap > maxRecommendationExecutionDailyCap ||
		policy.CooldownSeconds < 0 || policy.CooldownSeconds > maxRecommendationExecutionCooldownSeconds {
		return false
	}
	minimum, err := policy.MinimumSavings.Rat()
	return err == nil && minimum.Sign() >= 0
}

func memoryRecommendationExecutionTargetOwned(s *MemoryStore, policy contracts.RecommendationExecutionPolicy) bool {
	switch policy.Scope {
	case contracts.RecommendationExecutionScopePlan:
		for _, plan := range s.routePlans {
			if plan.ID == policy.PlanID {
				return plan.UserID == policy.UserID
			}
		}
	case contracts.RecommendationExecutionScopePool:
		for _, target := range s.poolRolloutTargets {
			if target.PoolID == policy.PoolID && target.UserID == policy.UserID && target.Enabled {
				return true
			}
		}
	}
	return false
}

func validRecommendationExecutionIdentifier(value string, maxLength int, optional bool) bool {
	if value == "" {
		return optional
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func recommendationExecutionPolicyTarget(policy contracts.RecommendationExecutionPolicy) string {
	if policy.Scope == contracts.RecommendationExecutionScopePlan {
		return policy.PlanID
	}
	return policy.PoolID
}

func sortRecommendationExecutionPolicies(policies []contracts.RecommendationExecutionPolicy) {
	sort.SliceStable(policies, func(i, j int) bool {
		if policies[i].Scope != policies[j].Scope {
			return policies[i].Scope < policies[j].Scope
		}
		left, right := recommendationExecutionPolicyTarget(policies[i]), recommendationExecutionPolicyTarget(policies[j])
		if left != right {
			return left < right
		}
		return policies[i].ID < policies[j].ID
	})
}

func normalizeRecommendationExecutionPolicyTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

// This is intentionally a named clone even though the current policy has no
// reference fields. It keeps the persistence boundary copy-safe when policy
// limits later gain slices, maps, or pointer-valued audit metadata.
func cloneRecommendationExecutionPolicy(policy contracts.RecommendationExecutionPolicy) contracts.RecommendationExecutionPolicy {
	return policy
}
