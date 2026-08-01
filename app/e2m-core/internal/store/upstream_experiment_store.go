package store

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
	"e2m.local/core/internal/upstreamexperiment"
	"e2m.local/core/internal/upstreamrecommendation"
)

// UpstreamExperimentStore persists immutable shadow and dry-run evidence.
// Replays are idempotent; reusing an identity with different evidence fails.
type UpstreamExperimentStore interface {
	AppendUpstreamShadowResult(context.Context, contracts.UpstreamShadowResult) (contracts.UpstreamShadowResult, error)
	GetUpstreamShadowResult(context.Context, int64, string) (contracts.UpstreamShadowResult, error)
	ListUpstreamShadowResults(context.Context, int64, string, int) ([]contracts.UpstreamShadowResult, error)
	AppendUpstreamDryRunResult(context.Context, contracts.UpstreamDryRunResult) (contracts.UpstreamDryRunResult, error)
	GetUpstreamDryRunResult(context.Context, int64, string) (contracts.UpstreamDryRunResult, error)
	ListUpstreamDryRunResults(context.Context, int64, string, int) ([]contracts.UpstreamDryRunResult, error)
	CompleteUpstreamShadow(context.Context, contracts.UpstreamRecommendation, contracts.UpstreamShadowResult) (contracts.UpstreamRecommendation, contracts.UpstreamShadowResult, error)
	CompleteUpstreamDryRun(context.Context, contracts.UpstreamRecommendation, contracts.UpstreamDryRunResult) (contracts.UpstreamRecommendation, contracts.UpstreamDryRunResult, error)
}

// CompleteUpstreamShadow persists immutable experiment evidence and advances
// open -> shadowing -> ready_for_dry_run while holding one MemoryStore lock.
// The transient state is deliberately never externally visible: a process
// failure before this call leaves the recommendation open, while a successful
// call commits both facts together.
func (s *MemoryStore) CompleteUpstreamShadow(ctx context.Context, expected contracts.UpstreamRecommendation, input contracts.UpstreamShadowResult) (contracts.UpstreamRecommendation, contracts.UpstreamShadowResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, err
	}
	expected = cloneUpstreamRecommendation(expected)
	input = cloneShadowResult(input)
	input.EvaluatedAt = normalizeUpstreamExperimentTime(input.EvaluatedAt)
	if expected.Status != contracts.UpstreamRecommendationOpen || upstreamrecommendation.Validate(expected) != nil || !validShadowResult(input) || !shadowResultMatchesRecommendation(input, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrInvalid
	}
	next, err := completedShadowRecommendation(expected, input.EvaluatedAt)
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeUpstreamShadowLocked(expected, next, input)
}

func (s *MemoryStore) completeUpstreamShadowLocked(expected, next contracts.UpstreamRecommendation, input contracts.UpstreamShadowResult) (contracts.UpstreamRecommendation, contracts.UpstreamShadowResult, error) {
	recommendationIndex := -1
	for index, current := range s.upstreamRecommendations {
		if current.UserID == expected.UserID && current.ID == expected.ID {
			recommendationIndex = index
			break
		}
	}
	if recommendationIndex < 0 {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrNotFound
	}
	current := s.upstreamRecommendations[recommendationIndex]
	for _, existing := range s.upstreamShadowResults {
		if existing.UserID != input.UserID || existing.ID != input.ID {
			continue
		}
		if reflect.DeepEqual(existing, input) && current.Status == next.Status && current.DryRunID == next.DryRunID && recommendationImmutableEqual(current, expected) {
			return cloneUpstreamRecommendation(current), cloneShadowResult(existing), nil
		}
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrConflict
	}
	if current.Status != expected.Status || !recommendationImmutableEqual(current, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamShadowResult{}, ErrConflict
	}
	s.upstreamShadowResults = append(s.upstreamShadowResults, input)
	s.recordOperationalMetricLocked("experiments", "shadow", 1)
	s.upstreamRecommendations[recommendationIndex].Status = next.Status
	return cloneUpstreamRecommendation(s.upstreamRecommendations[recommendationIndex]), cloneShadowResult(input), nil
}

// CompleteUpstreamDryRun is the dry-run equivalent of CompleteUpstreamShadow.
// It atomically records the exact PlanScheduling preview and advances
// ready_for_dry_run -> dry_running -> dry_run_passed.
func (s *MemoryStore) CompleteUpstreamDryRun(ctx context.Context, expected contracts.UpstreamRecommendation, input contracts.UpstreamDryRunResult) (contracts.UpstreamRecommendation, contracts.UpstreamDryRunResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, err
	}
	expected = cloneUpstreamRecommendation(expected)
	input = cloneDryRunResult(input)
	input = normalizeDryRunResultTimes(input)
	if expected.Status != contracts.UpstreamRecommendationReadyForDryRun || upstreamrecommendation.Validate(expected) != nil || !validDryRunResult(input) || !dryRunResultMatchesRecommendation(input, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	next, err := completedDryRunRecommendation(expected, input.ID, input.CreatedAt)
	if err != nil {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recommendationIndex := -1
	for index, current := range s.upstreamRecommendations {
		if current.UserID == expected.UserID && current.ID == expected.ID {
			recommendationIndex = index
			break
		}
	}
	if recommendationIndex < 0 {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrNotFound
	}
	current := s.upstreamRecommendations[recommendationIndex]
	for _, existing := range s.upstreamDryRunResults {
		if existing.UserID != input.UserID || existing.ID != input.ID {
			continue
		}
		if reflect.DeepEqual(existing, input) && current.Status == next.Status && current.DryRunID == next.DryRunID && recommendationImmutableEqual(current, expected) {
			return cloneUpstreamRecommendation(current), cloneDryRunResult(existing), nil
		}
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrConflict
	}
	if current.Status != expected.Status || !recommendationImmutableEqual(current, expected) {
		return contracts.UpstreamRecommendation{}, contracts.UpstreamDryRunResult{}, ErrConflict
	}
	s.upstreamDryRunResults = append(s.upstreamDryRunResults, input)
	s.recordOperationalMetricLocked("experiments", "dry_run", 1)
	s.upstreamRecommendations[recommendationIndex].Status = next.Status
	s.upstreamRecommendations[recommendationIndex].DryRunID = next.DryRunID
	return cloneUpstreamRecommendation(s.upstreamRecommendations[recommendationIndex]), cloneDryRunResult(input), nil
}

func completedShadowRecommendation(current contracts.UpstreamRecommendation, now time.Time) (contracts.UpstreamRecommendation, error) {
	shadowing, err := upstreamrecommendation.Transition(current, contracts.UpstreamRecommendationEvent{
		Type: contracts.UpstreamRecommendationEventStartShadow, UserID: current.UserID, Now: now,
	})
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	return upstreamrecommendation.Transition(shadowing, contracts.UpstreamRecommendationEvent{
		Type: contracts.UpstreamRecommendationEventShadowPassed, UserID: current.UserID, Now: now,
	})
}

func completedDryRunRecommendation(current contracts.UpstreamRecommendation, dryRunID string, now time.Time) (contracts.UpstreamRecommendation, error) {
	running, err := upstreamrecommendation.Transition(current, contracts.UpstreamRecommendationEvent{
		Type: contracts.UpstreamRecommendationEventStartDryRun, UserID: current.UserID, Now: now, DryRunID: dryRunID,
	})
	if err != nil {
		return contracts.UpstreamRecommendation{}, err
	}
	return upstreamrecommendation.Transition(running, contracts.UpstreamRecommendationEvent{
		Type: contracts.UpstreamRecommendationEventDryRunPassed, UserID: current.UserID, Now: now, DryRunID: dryRunID,
	})
}

func shadowResultMatchesRecommendation(result contracts.UpstreamShadowResult, recommendation contracts.UpstreamRecommendation) bool {
	if result.UserID != recommendation.UserID || result.RecommendationID != recommendation.ID || result.RecommendationFingerprint != recommendation.Fingerprint ||
		result.EvaluatedAt.Before(recommendation.CreatedAt) || !result.EvaluatedAt.Before(recommendation.ExpiresAt) ||
		!sameExperimentEvidence(result.EvidenceIDs, recommendation.EvidenceIDs) || len(result.Ranking) != 2 || !reflect.DeepEqual(result.Winner, result.Ranking[0]) {
		return false
	}
	allowed := map[string]struct {
		source   string
		group    string
		wantCost contracts.CanonicalDecimal
	}{
		recommendation.FromChannelID: {source: recommendation.FromSourceID, group: recommendation.FromGroupKey, wantCost: recommendation.FromCost.Expected},
		recommendation.ToChannelID:   {source: recommendation.ToSourceID, group: recommendation.ToGroupKey, wantCost: recommendation.ToCost.Expected},
	}
	seen := make(map[string]bool, len(result.Ranking))
	for _, candidate := range result.Ranking {
		lane, ok := allowed[candidate.ChannelID]
		if !ok || candidate.UserID != recommendation.UserID || lane.source != candidate.SourceID || lane.group != candidate.GroupKey || seen[candidate.ChannelID] ||
			candidate.ModelKey != recommendation.ModelKey || candidate.PriceDimension != recommendation.PriceDimension ||
			candidate.SettlementCurrency != recommendation.SettlementCurrency || candidate.PerTokens != recommendation.PerTokens ||
			candidate.Cost != lane.wantCost || !reflect.DeepEqual(candidate.Constraints, recommendation.Constraints) ||
			!experimentEvidenceSubset(candidate.EvidenceIDs, recommendation.EvidenceIDs) {
			return false
		}
		seen[candidate.ChannelID] = true
	}
	if len(seen) != 2 {
		return false
	}
	rebuilt, err := upstreamexperiment.ShadowRank(recommendation, result.Ranking, result.EvaluatedAt)
	return err == nil && reflect.DeepEqual(rebuilt.Winner, result.Winner) && reflect.DeepEqual(rebuilt.Ranking, result.Ranking) &&
		sameExperimentEvidence(rebuilt.EvidenceIDs, result.EvidenceIDs)
}

func dryRunResultMatchesRecommendation(result contracts.UpstreamDryRunResult, recommendation contracts.UpstreamRecommendation) bool {
	if result.UserID != recommendation.UserID || result.RecommendationID != recommendation.ID || result.RecommendationFingerprint != recommendation.Fingerprint ||
		result.IntelligenceFactVersion != recommendation.IntelligenceFactVersion || result.CostLedgerFactVersion != recommendation.CostLedgerFactVersion ||
		result.LinkFactVersion != recommendation.LinkFactVersion || result.PlanGeneration != recommendation.PlanGeneration || len(recommendation.AffectedPlanIDs) != 1 ||
		result.PlanID != recommendation.AffectedPlanIDs[0] || result.FromChannelID != recommendation.FromChannelID || result.ToChannelID != recommendation.ToChannelID ||
		result.DesiredScheduling[recommendation.FromChannelID] || !result.DesiredScheduling[recommendation.ToChannelID] || len(result.DesiredScheduling) != 2 ||
		result.CreatedAt.Before(recommendation.CreatedAt) || !result.CreatedAt.Before(recommendation.ExpiresAt) ||
		result.Plan.CreatedAt.IsZero() || result.Plan.CreatedAt.After(result.CreatedAt) {
		return false
	}
	if len(recommendation.AffectedDownstreams) != 1 || result.Plan.InstanceID != recommendation.AffectedDownstreams[0] || len(result.Plan.Actions) == 0 {
		return false
	}
	allowed := map[string]contracts.ReconcileActionType{
		recommendation.FromChannelID: contracts.ReconcileDisable,
		recommendation.ToChannelID:   contracts.ReconcileEnable,
	}
	seen := make(map[string]bool, 2)
	realActions := 0
	for _, action := range result.Plan.Actions {
		if action.Type == contracts.ReconcileNoop {
			if _, ok := allowed[action.ChannelID]; !ok || seen[action.ChannelID] {
				return false
			}
			seen[action.ChannelID] = true
			continue
		}
		wanted, ok := allowed[action.ChannelID]
		if !ok || wanted != action.Type || seen[action.ChannelID] {
			return false
		}
		seen[action.ChannelID] = true
		realActions++
	}
	if realActions == 0 {
		return false
	}
	hash, err := upstreamexperiment.ActionSetHash(result.Plan)
	return err == nil && hash == result.ActionSetHash
}

func sameExperimentEvidence(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}

func experimentEvidenceSubset(subset, full []string) bool {
	if len(subset) == 0 {
		return false
	}
	allowed := make(map[string]bool, len(full))
	for _, id := range full {
		allowed[id] = true
	}
	seen := make(map[string]bool, len(subset))
	for _, id := range subset {
		if id == "" || !allowed[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

var (
	_ UpstreamExperimentStore = (*MemoryStore)(nil)
	_ UpstreamExperimentStore = (*PostgresStore)(nil)
)

func validShadowResult(value contracts.UpstreamShadowResult) bool {
	return validUpstreamExperimentID(value.ID) && value.UserID > 0 && validUpstreamExperimentReference(value.RecommendationID, 256) &&
		validUpstreamExperimentHash(value.RecommendationFingerprint) && value.Winner.UserID == value.UserID && value.Winner.ChannelID != "" &&
		len(value.Ranking) > 0 && len(value.EvidenceIDs) > 0 && !value.EvaluatedAt.IsZero()
}

func validDryRunResult(value contracts.UpstreamDryRunResult) bool {
	return validUpstreamExperimentID(value.ID) && value.UserID > 0 && validUpstreamExperimentReference(value.RecommendationID, 256) &&
		validUpstreamExperimentHash(value.RecommendationFingerprint) && value.IntelligenceFactVersion > 0 && value.CostLedgerFactVersion > 0 &&
		value.LinkFactVersion > 0 && value.PlanGeneration > 0 && validUpstreamExperimentReference(value.PlanID, 256) && validUpstreamExperimentReference(value.FromChannelID, 256) &&
		validUpstreamExperimentReference(value.ToChannelID, 256) && value.FromChannelID != value.ToChannelID && value.ReconcileKind == contracts.ReconcileRunDryRun &&
		value.Plan.DryRun && value.Plan.PlanID == value.PlanID && value.ActionHashVersion == contracts.UpstreamExperimentActionHashVersionV1 &&
		validUpstreamExperimentHash(value.ActionSetHash) && !value.CreatedAt.IsZero()
}

func validUpstreamExperimentID(value string) bool {
	return validUpstreamExperimentReference(value, 128)
}

func validUpstreamExperimentReference(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximum && utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && !contracts.LooksLikeConnectorSensitiveValue(value)
}

func validUpstreamExperimentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func cloneShadowResult(value contracts.UpstreamShadowResult) contracts.UpstreamShadowResult {
	value.Ranking = append([]contracts.UpstreamShadowCandidate(nil), value.Ranking...)
	for index := range value.Ranking {
		value.Ranking[index] = cloneShadowCandidate(value.Ranking[index])
	}
	value.Winner = cloneShadowCandidate(value.Winner)
	value.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	return value
}

func cloneShadowCandidate(value contracts.UpstreamShadowCandidate) contracts.UpstreamShadowCandidate {
	value.EvidenceIDs = append([]string(nil), value.EvidenceIDs...)
	value.Constraints = append([]contracts.UpstreamRecommendationConstraint(nil), value.Constraints...)
	for index := range value.Constraints {
		value.Constraints[index].EvidenceIDs = append([]string(nil), value.Constraints[index].EvidenceIDs...)
	}
	return value
}

func cloneDryRunResult(value contracts.UpstreamDryRunResult) contracts.UpstreamDryRunResult {
	value.DesiredScheduling = cloneSchedulingIntent(value.DesiredScheduling)
	value.Plan.Actions = append([]contracts.ReconcileAction(nil), value.Plan.Actions...)
	return value
}

func cloneSchedulingIntent(value map[string]bool) map[string]bool {
	result := make(map[string]bool, len(value))
	for key, enabled := range value {
		result[key] = enabled
	}
	return result
}

func (s *MemoryStore) AppendUpstreamShadowResult(ctx context.Context, input contracts.UpstreamShadowResult) (contracts.UpstreamShadowResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	input = cloneShadowResult(input)
	input.EvaluatedAt = normalizeUpstreamExperimentTime(input.EvaluatedAt)
	if !validShadowResult(input) {
		return contracts.UpstreamShadowResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.upstreamShadowResults {
		if existing.UserID == input.UserID && existing.ID == input.ID {
			if !reflect.DeepEqual(existing, input) {
				return contracts.UpstreamShadowResult{}, ErrConflict
			}
			return cloneShadowResult(existing), nil
		}
	}
	s.upstreamShadowResults = append(s.upstreamShadowResults, input)
	s.recordOperationalMetricLocked("experiments", "shadow", 1)
	return cloneShadowResult(input), nil
}

func (s *MemoryStore) GetUpstreamShadowResult(ctx context.Context, userID int64, id string) (contracts.UpstreamShadowResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamShadowResult{}, err
	}
	if userID <= 0 || strings.TrimSpace(id) == "" {
		return contracts.UpstreamShadowResult{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.upstreamShadowResults {
		if value.UserID == userID && value.ID == id {
			return cloneShadowResult(value), nil
		}
	}
	return contracts.UpstreamShadowResult{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamShadowResults(ctx context.Context, userID int64, recommendationID string, limit int) ([]contracts.UpstreamShadowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]contracts.UpstreamShadowResult, 0)
	for _, value := range s.upstreamShadowResults {
		if value.UserID == userID && (recommendationID == "" || value.RecommendationID == recommendationID) {
			result = append(result, cloneShadowResult(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EvaluatedAt.After(result[j].EvaluatedAt) })
	limit = contracts.NormalizeUpstreamIntelligenceListLimit(limit)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryStore) AppendUpstreamDryRunResult(ctx context.Context, input contracts.UpstreamDryRunResult) (contracts.UpstreamDryRunResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	input = cloneDryRunResult(input)
	input = normalizeDryRunResultTimes(input)
	if !validDryRunResult(input) {
		return contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.upstreamDryRunResults {
		if existing.UserID == input.UserID && existing.ID == input.ID {
			if !reflect.DeepEqual(existing, input) {
				return contracts.UpstreamDryRunResult{}, ErrConflict
			}
			return cloneDryRunResult(existing), nil
		}
	}
	s.upstreamDryRunResults = append(s.upstreamDryRunResults, input)
	s.recordOperationalMetricLocked("experiments", "dry_run", 1)
	return cloneDryRunResult(input), nil
}

// PostgreSQL timestamptz has microsecond precision. Normalize at both store
// boundaries so MemoryStore and PostgreSQL preserve the same immutable event
// identity and idempotent replay comparison.
func normalizeUpstreamExperimentTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func normalizeDryRunResultTimes(value contracts.UpstreamDryRunResult) contracts.UpstreamDryRunResult {
	value.CreatedAt = normalizeUpstreamExperimentTime(value.CreatedAt)
	value.Plan.CreatedAt = normalizeUpstreamExperimentTime(value.Plan.CreatedAt)
	return value
}

func (s *MemoryStore) GetUpstreamDryRunResult(ctx context.Context, userID int64, id string) (contracts.UpstreamDryRunResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.UpstreamDryRunResult{}, err
	}
	if userID <= 0 || strings.TrimSpace(id) == "" {
		return contracts.UpstreamDryRunResult{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.upstreamDryRunResults {
		if value.UserID == userID && value.ID == id {
			return cloneDryRunResult(value), nil
		}
	}
	return contracts.UpstreamDryRunResult{}, ErrNotFound
}

func (s *MemoryStore) ListUpstreamDryRunResults(ctx context.Context, userID int64, recommendationID string, limit int) ([]contracts.UpstreamDryRunResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]contracts.UpstreamDryRunResult, 0)
	for _, value := range s.upstreamDryRunResults {
		if value.UserID == userID && (recommendationID == "" || value.RecommendationID == recommendationID) {
			result = append(result, cloneDryRunResult(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	limit = contracts.NormalizeUpstreamIntelligenceListLimit(limit)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
