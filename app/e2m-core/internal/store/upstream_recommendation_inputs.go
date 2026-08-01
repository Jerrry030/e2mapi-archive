package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

const maxUpstreamRecommendationCostFacts = 50_000

// UpstreamRecommendationInputs is one owner-scoped consistency boundary. The
// generator must not combine separately-read dashboard, ledger and route data.
type UpstreamRecommendationInputs struct {
	UserID                int64
	GeneratedAt           time.Time
	Intelligence          UpstreamIntelligenceCurrentSnapshot
	CostLedgerFactVersion int64
	CostFacts             []contracts.UpstreamCostFact
	RoutePlans            []contracts.RoutePlan
	Channels              []contracts.UpstreamChannel
	Bindings              []contracts.PublishedBinding
}

// RecommendationRolloutDecisionInputs binds a durable recommendation, the
// current owner decision facts and the exact quality rows that originally
// justified that recommendation to one consistency boundary. Exact quality
// rows are historical evidence; they are never substituted into the ordinary
// current projection outside rollout regression revalidation.
type RecommendationRolloutDecisionInputs struct {
	Recommendation       contracts.UpstreamRecommendation
	Current              UpstreamRecommendationInputs
	ExactQualityEvidence []contracts.ChannelHealthSnapshot
	// QualityOnlyFactAdvance is an owner-scoped lineage proof for every
	// intelligence version strictly after the recommendation's immutable
	// baseline through Current. Controller validates the proof independently;
	// Complete is an assertion from Store, never sufficient on its own.
	QualityOnlyFactAdvance QualityOnlyFactAdvanceProof
}

// UpstreamIntelligenceFactMutationKind classifies the write which allocated
// one owner-scoped intelligence fact version. Unknown is deliberately not a
// wildcard: safety boundaries must reject it.
type UpstreamIntelligenceFactMutationKind string

const (
	UpstreamIntelligenceFactMutationQuality    UpstreamIntelligenceFactMutationKind = "quality"
	UpstreamIntelligenceFactMutationCollection UpstreamIntelligenceFactMutationKind = "collection"
	UpstreamIntelligenceFactMutationLink       UpstreamIntelligenceFactMutationKind = "link"
	UpstreamIntelligenceFactMutationSource     UpstreamIntelligenceFactMutationKind = "source"
	UpstreamIntelligenceFactMutationRetention  UpstreamIntelligenceFactMutationKind = "retention"
	UpstreamIntelligenceFactMutationUnknown    UpstreamIntelligenceFactMutationKind = "unknown"
)

// UpstreamIntelligenceFactMutation is one immutable owner/version lineage
// entry. EvidenceID is an opaque safe fact identity, never a credential or an
// upstream response body.
type UpstreamIntelligenceFactMutation struct {
	UserID      int64
	FactVersion int64
	Kind        UpstreamIntelligenceFactMutationKind
	EvidenceID  string
	CreatedAt   time.Time
}

// QualityOnlyFactAdvanceProof covers the half-open version interval
// (BaselineFactVersion, CurrentFactVersion]. LineageWatermark is the earliest
// baseline for which Store can prove a gap-free interval; an older baseline
// must fail closed rather than infer provenance from missing history.
type QualityOnlyFactAdvanceProof struct {
	UserID              int64
	BaselineFactVersion int64
	CurrentFactVersion  int64
	LineageWatermark    int64
	Mutations           []UpstreamIntelligenceFactMutation
	Complete            bool
}

// ValidQualityOnlyFactAdvanceProof independently verifies Store's lineage
// assertion against Controller's expected owner and exact interval. It accepts
// no gaps, duplicates, reordering, non-quality writes, missing identities or
// time reversal. In particular, Complete alone is never trusted.
func ValidQualityOnlyFactAdvanceProof(value QualityOnlyFactAdvanceProof, userID, baselineFactVersion, currentFactVersion int64) bool {
	if !value.Complete || userID <= 0 || value.UserID != userID ||
		baselineFactVersion <= 0 || currentFactVersion <= baselineFactVersion ||
		value.BaselineFactVersion != baselineFactVersion || value.CurrentFactVersion != currentFactVersion ||
		value.LineageWatermark < 0 || baselineFactVersion < value.LineageWatermark ||
		int64(len(value.Mutations)) != currentFactVersion-baselineFactVersion {
		return false
	}
	expectedVersion := baselineFactVersion
	var previousCreatedAt time.Time
	for _, mutation := range value.Mutations {
		expectedVersion++
		if mutation.UserID != userID || mutation.FactVersion != expectedVersion ||
			mutation.Kind != UpstreamIntelligenceFactMutationQuality ||
			strings.TrimSpace(mutation.EvidenceID) == "" || mutation.CreatedAt.IsZero() ||
			!previousCreatedAt.IsZero() && mutation.CreatedAt.Before(previousCreatedAt) {
			return false
		}
		previousCreatedAt = mutation.CreatedAt
	}
	return expectedVersion == currentFactVersion
}

type UpstreamRecommendationInputStore interface {
	ReadUpstreamRecommendationInputs(context.Context, int64) (UpstreamRecommendationInputs, error)
}

// RecommendationRolloutDecisionInputStore is rollout-only so recommendation
// generation, shadow and dry-run readers do not inherit historical evidence
// access or need to implement a capability they never call.
type RecommendationRolloutDecisionInputStore interface {
	ReadRecommendationRolloutDecisionInputs(context.Context, int64, string) (RecommendationRolloutDecisionInputs, error)
}

var (
	_ UpstreamRecommendationInputStore        = (*MemoryStore)(nil)
	_ UpstreamRecommendationInputStore        = (*PostgresStore)(nil)
	_ RecommendationRolloutDecisionInputStore = (*MemoryStore)(nil)
	_ RecommendationRolloutDecisionInputStore = (*PostgresStore)(nil)
)

func (s *MemoryStore) ReadUpstreamRecommendationInputs(ctx context.Context, userID int64) (UpstreamRecommendationInputs, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readUpstreamRecommendationInputsLocked(userID)
}

func (s *MemoryStore) readUpstreamRecommendationInputsLocked(userID int64) (UpstreamRecommendationInputs, error) {
	generatedAt := normalizeUpstreamTime(s.now())
	intelligence, err := s.readUpstreamIntelligenceCurrentLocked(userID, generatedAt)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	result := UpstreamRecommendationInputs{UserID: userID, GeneratedAt: generatedAt, Intelligence: intelligence}
	for _, fact := range s.upstreamCostFacts {
		if fact.UserID != userID {
			continue
		}
		result.CostFacts = append(result.CostFacts, cloneUpstreamCostFact(fact))
		if fact.FactVersion > result.CostLedgerFactVersion {
			result.CostLedgerFactVersion = fact.FactVersion
		}
	}
	if len(result.CostFacts) > maxUpstreamRecommendationCostFacts {
		return UpstreamRecommendationInputs{}, ErrConflict
	}
	planIDs := make(map[string]bool)
	for _, plan := range s.routePlans {
		if plan.UserID == userID {
			result.RoutePlans = append(result.RoutePlans, cloneRecommendationRoutePlan(plan))
			planIDs[plan.ID] = true
		}
	}
	for _, channel := range s.upstreamChannels {
		if allocation, ok := s.channelAllocations[channel.ID]; ok && allocation.UserID == userID {
			result.Channels = append(result.Channels, cloneRecommendationChannel(channel))
		}
	}
	for _, binding := range s.publishedBindings {
		if planIDs[binding.PlanID] {
			result.Bindings = append(result.Bindings, binding)
		}
	}
	normalizeUpstreamRecommendationInputs(&result)
	return result, nil
}

func (s *MemoryStore) ReadRecommendationRolloutDecisionInputs(ctx context.Context, userID int64, recommendationID string) (RecommendationRolloutDecisionInputs, error) {
	if err := ctx.Err(); err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil || strings.TrimSpace(recommendationID) == "" {
		return RecommendationRolloutDecisionInputs{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	recommendation, err := s.getUpstreamRecommendationLocked(userID, recommendationID)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	qualityIDs, ok := exactRecommendationQualityEvidenceIDs(recommendation)
	if !ok {
		return RecommendationRolloutDecisionInputs{}, ErrConflict
	}
	current, err := s.readUpstreamRecommendationInputsLocked(userID)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	exact, err := memoryExactRecommendationQualityEvidence(s.channelSnapshots, s.channelAllocations, s.instances, userID, qualityIDs)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	return RecommendationRolloutDecisionInputs{
		Recommendation: recommendation, Current: current, ExactQualityEvidence: exact,
		QualityOnlyFactAdvance: s.memoryQualityOnlyFactAdvanceProof(userID, recommendation.IntelligenceFactVersion, current.Intelligence.FactVersion.FactVersion),
	}, nil
}

// readUpstreamIntelligenceCurrentLocked mirrors ReadUpstreamIntelligenceCurrent
// while the caller already holds the single MemoryStore read lock.
func (s *MemoryStore) readUpstreamIntelligenceCurrentLocked(userID int64, generatedAt time.Time) (UpstreamIntelligenceCurrentSnapshot, error) {
	snapshot := UpstreamIntelligenceCurrentSnapshot{UserID: userID, GeneratedAt: generatedAt, FactVersion: s.upstreamIntelVersions[userID]}
	snapshot.FactVersion.UserID = userID
	for _, source := range s.upstreamIntelSources {
		if source.UserID == userID {
			snapshot.Sources = append(snapshot.Sources, cloneUpstreamIntelligenceSource(source))
		}
	}
	if len(snapshot.Sources) > maxUpstreamIntelligenceReadSources {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	finalized := make(map[string]contracts.UpstreamCollectionRun)
	latestRuns := make(map[string]contracts.UpstreamCollectionRun)
	for _, run := range s.upstreamIntelRuns {
		if run.UserID != userID || run.FinalizedFactVersion <= 0 {
			continue
		}
		finalized[memoryUpstreamFinalizationKey(userID, run.ID)] = run
		if current, exists := latestRuns[run.SourceID]; !exists || upstreamReadRunNewer(run, current) {
			latestRuns[run.SourceID] = run
		}
	}
	for _, run := range latestRuns {
		snapshot.LatestRuns = append(snapshot.LatestRuns, cloneUpstreamRun(run))
	}
	wallets := make(map[string]contracts.UpstreamWalletObservation)
	for _, wallet := range s.upstreamIntelWallets {
		if wallet.UserID != userID {
			continue
		}
		if _, ok := finalized[memoryUpstreamFinalizationKey(userID, wallet.RunID)]; !ok {
			continue
		}
		if current, exists := wallets[wallet.SourceID]; !exists || upstreamReadWalletNewer(wallet, current) {
			wallets[wallet.SourceID] = wallet
		}
	}
	for _, wallet := range wallets {
		snapshot.Wallets = append(snapshot.Wallets, cloneUpstreamWallet(wallet))
	}
	offers := make(map[string]contracts.UpstreamOfferObservation)
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID != userID {
			continue
		}
		if _, ok := finalized[memoryUpstreamFinalizationKey(userID, offer.RunID)]; !ok {
			continue
		}
		key := upstreamReadOfferKey(offer)
		if current, exists := offers[key]; !exists || upstreamReadOfferNewer(offer, current) {
			offers[key] = offer
		}
	}
	if len(offers) > maxUpstreamIntelligenceReadOffers {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	for _, offer := range offers {
		snapshot.Offers = append(snapshot.Offers, cloneUpstreamOffer(offer))
	}
	for _, absence := range s.upstreamIntelAbsences {
		if absence.UserID == userID {
			snapshot.Absences = append(snapshot.Absences, cloneUpstreamAbsence(absence))
		}
	}
	cutoff := generatedAt.Add(-upstreamIntelligenceReadChangeHistory)
	for _, change := range s.upstreamIntelChanges {
		if change.UserID == userID && !change.ConfirmedAt.Before(cutoff) {
			snapshot.Changes = append(snapshot.Changes, cloneUpstreamChange(change))
		}
	}
	if len(snapshot.Changes) > maxUpstreamIntelligenceReadChanges {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	for _, link := range s.upstreamIntelLinks {
		if link.UserID == userID {
			cloned := cloneUpstreamLink(link)
			snapshot.Links = append(snapshot.Links, cloned)
			snapshot.LinkResolutions = append(snapshot.LinkResolutions, memoryUpstreamLinkResolution(s.channelAllocations, cloned))
		}
	}
	if len(snapshot.Links) > maxUpstreamIntelligenceReadLinks {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	snapshot.QualitySnapshots = memoryUpstreamReadQualitySnapshots(s.channelSnapshots, s.channelAllocations, s.instances, userID, generatedAt)
	if len(snapshot.QualitySnapshots) > maxUpstreamIntelligenceReadQuality {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	filterConfirmedRemovedUpstreamOffers(&snapshot)
	normalizeUpstreamIntelligenceCurrentOrder(&snapshot)
	return snapshot, nil
}

func (s *PostgresStore) ReadUpstreamRecommendationInputs(ctx context.Context, userID int64) (UpstreamRecommendationInputs, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	assertPostgresUpstreamReadColumns()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := readPostgresRecommendationInputs(ctx, tx, userID)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	normalizeUpstreamRecommendationInputs(&result)
	return result, nil
}

func (s *PostgresStore) ReadRecommendationRolloutDecisionInputs(ctx context.Context, userID int64, recommendationID string) (RecommendationRolloutDecisionInputs, error) {
	if err := requireUpstreamOwner(userID); err != nil || strings.TrimSpace(recommendationID) == "" {
		return RecommendationRolloutDecisionInputs{}, ErrInvalid
	}
	assertPostgresUpstreamReadColumns()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	recommendation, err := queryUpstreamRecommendation(ctx, tx, userID, recommendationID)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	qualityIDs, ok := exactRecommendationQualityEvidenceIDs(recommendation)
	if !ok {
		return RecommendationRolloutDecisionInputs{}, ErrConflict
	}
	current, err := readPostgresRecommendationInputs(ctx, tx, userID)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	exact, err := queryExactRecommendationQualityEvidence(ctx, tx, userID, qualityIDs)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	qualityAdvance, err := queryQualityOnlyFactAdvanceProof(ctx, tx, userID,
		recommendation.IntelligenceFactVersion, current.Intelligence.FactVersion.FactVersion)
	if err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RecommendationRolloutDecisionInputs{}, err
	}
	normalizeUpstreamRecommendationInputs(&current)
	return RecommendationRolloutDecisionInputs{
		Recommendation: recommendation, Current: current, ExactQualityEvidence: exact,
		QualityOnlyFactAdvance: qualityAdvance,
	}, nil
}

func readPostgresRecommendationInputs(ctx context.Context, queryer upstreamReadQueryer, userID int64) (UpstreamRecommendationInputs, error) {
	result := UpstreamRecommendationInputs{UserID: userID}
	if err := queryer.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&result.GeneratedAt); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	result.GeneratedAt = normalizeUpstreamTime(result.GeneratedAt)
	var err error
	result.Intelligence, err = readPostgresRecommendationIntelligence(ctx, queryer, userID, result.GeneratedAt)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	if err := queryer.QueryRow(ctx, `SELECT COALESCE((SELECT fact_version FROM upstream_cost_fact_versions WHERE user_id=$1),0)`, userID).Scan(&result.CostLedgerFactVersion); err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	rows, err := queryer.Query(ctx, `SELECT `+upstreamCostFactCols+` FROM upstream_cost_facts WHERE user_id=$1 ORDER BY occurred_at DESC,id DESC LIMIT $2`, userID, maxUpstreamRecommendationCostFacts+1)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	for rows.Next() {
		fact, scanErr := scanUpstreamCostFact(rows)
		if scanErr != nil {
			rows.Close()
			return UpstreamRecommendationInputs{}, scanErr
		}
		result.CostFacts = append(result.CostFacts, fact)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return UpstreamRecommendationInputs{}, err
	}
	rows.Close()
	if len(result.CostFacts) > maxUpstreamRecommendationCostFacts {
		return UpstreamRecommendationInputs{}, ErrConflict
	}
	result.RoutePlans, err = queryRecommendationPlans(ctx, queryer, userID)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	result.Channels, err = queryRecommendationChannels(ctx, queryer, userID)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	result.Bindings, err = queryRecommendationBindings(ctx, queryer, userID)
	if err != nil {
		return UpstreamRecommendationInputs{}, err
	}
	return result, nil
}

func readPostgresRecommendationIntelligence(ctx context.Context, queryer upstreamReadQueryer, userID int64, generatedAt time.Time) (UpstreamIntelligenceCurrentSnapshot, error) {
	result := UpstreamIntelligenceCurrentSnapshot{UserID: userID, GeneratedAt: generatedAt}
	var err error
	if err = readUpstreamFactVersionQuery(ctx, queryer, userID, &result.FactVersion); err != nil {
		return result, err
	}
	if result.Sources, err = queryUpstreamReadSources(ctx, queryer, userID, maxUpstreamIntelligenceReadSources+1); err != nil || len(result.Sources) > maxUpstreamIntelligenceReadSources {
		return result, recommendationSnapshotErr(err)
	}
	if result.LatestRuns, err = queryUpstreamReadLatestRuns(ctx, queryer, userID); err != nil {
		return result, err
	}
	if result.Wallets, err = queryUpstreamReadWallets(ctx, queryer, userID); err != nil {
		return result, err
	}
	if result.Offers, err = queryUpstreamReadOffers(ctx, queryer, userID, maxUpstreamIntelligenceReadOffers+1); err != nil || len(result.Offers) > maxUpstreamIntelligenceReadOffers {
		return result, recommendationSnapshotErr(err)
	}
	if result.Absences, err = queryUpstreamReadAbsences(ctx, queryer, userID); err != nil {
		return result, err
	}
	if result.Changes, err = queryUpstreamReadChanges(ctx, queryer, userID, generatedAt.Add(-upstreamIntelligenceReadChangeHistory), maxUpstreamIntelligenceReadChanges+1); err != nil || len(result.Changes) > maxUpstreamIntelligenceReadChanges {
		return result, recommendationSnapshotErr(err)
	}
	if result.Links, err = queryUpstreamReadLinks(ctx, queryer, userID, maxUpstreamIntelligenceReadLinks+1); err != nil || len(result.Links) > maxUpstreamIntelligenceReadLinks {
		return result, recommendationSnapshotErr(err)
	}
	if result.LinkResolutions, err = queryUpstreamReadLinkResolutions(ctx, queryer, userID, maxUpstreamIntelligenceReadLinks+1); err != nil || len(result.LinkResolutions) > maxUpstreamIntelligenceReadLinks {
		return result, recommendationSnapshotErr(err)
	}
	if result.QualitySnapshots, err = queryUpstreamReadQualitySnapshots(ctx, queryer, userID, generatedAt, maxUpstreamIntelligenceReadQuality+1); err != nil || len(result.QualitySnapshots) > maxUpstreamIntelligenceReadQuality {
		return result, recommendationSnapshotErr(err)
	}
	filterConfirmedRemovedUpstreamOffers(&result)
	normalizeUpstreamIntelligenceCurrentOrder(&result)
	return result, nil
}

func recommendationSnapshotErr(err error) error {
	if err != nil {
		return err
	}
	return ErrConflict
}

func exactRecommendationQualityEvidenceIDs(value contracts.UpstreamRecommendation) ([]string, bool) {
	var ids []string
	qualityConstraints := 0
	for _, constraint := range value.Constraints {
		if constraint.Kind != contracts.UpstreamRecommendationConstraintQuality {
			continue
		}
		qualityConstraints++
		ids = append([]string(nil), constraint.EvidenceIDs...)
	}
	if qualityConstraints != 1 || len(ids) != 2 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(ids))
	for index := range ids {
		ids[index] = strings.TrimSpace(ids[index])
		if ids[index] == "" {
			return nil, false
		}
		if _, duplicate := seen[ids[index]]; duplicate {
			return nil, false
		}
		seen[ids[index]] = struct{}{}
	}
	return ids, true
}

func memoryExactRecommendationQualityEvidence(
	snapshots []contracts.ChannelHealthSnapshot,
	allocations map[string]upstreamChannelAllocation,
	instances []contracts.Instance,
	userID int64,
	ids []string,
) ([]contracts.ChannelHealthSnapshot, error) {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	ownedInstances := make(map[string]struct{})
	for _, instance := range instances {
		if instance.UserID == userID {
			ownedInstances[instance.ID] = struct{}{}
		}
	}
	result := make([]contracts.ChannelHealthSnapshot, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, snapshot := range snapshots {
		if _, ok := wanted[snapshot.ID]; !ok {
			continue
		}
		allocation, allocated := allocations[snapshot.ChannelID]
		_, instanceOwned := ownedInstances[snapshot.InstanceID]
		if !allocated || allocation.UserID != userID || !instanceOwned {
			continue
		}
		if _, duplicate := seen[snapshot.ID]; duplicate {
			return nil, ErrConflict
		}
		seen[snapshot.ID] = struct{}{}
		result = append(result, snapshot)
	}
	if len(result) != len(ids) {
		return nil, ErrNotFound
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func queryExactRecommendationQualityEvidence(ctx context.Context, queryer upstreamReadQueryer, userID int64, ids []string) ([]contracts.ChannelHealthSnapshot, error) {
	if len(ids) != 2 || ids[0] == ids[1] || strings.TrimSpace(ids[0]) == "" || strings.TrimSpace(ids[1]) == "" {
		return nil, ErrInvalid
	}
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("snapshot", channelHealthSnapshotCols)+`
		FROM channel_health_snapshots AS snapshot
		JOIN upstream_channel_allocations AS allocation
		  ON allocation.channel_id=snapshot.channel_id AND allocation.user_id=$1
		JOIN instances AS instance
		  ON instance.id=snapshot.instance_id AND instance.user_id=allocation.user_id
		WHERE snapshot.id=ANY($2::text[])
		ORDER BY snapshot.id`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.ChannelHealthSnapshot, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for rows.Next() {
		value, scanErr := scanChannelHealthSnapshot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return nil, ErrConflict
		}
		seen[value.ID] = struct{}{}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, ErrNotFound
	}
	return result, nil
}

// queryQualityOnlyFactAdvanceProof reads lineage inside the caller's owner
// snapshot transaction. Missing schema/watermark rows and query failures are
// hard errors. Retention gaps or a baseline predating the cutover watermark
// remain readable as Complete=false so Controller can fail closed as a generic
// gate without misclassifying the cause as a quality regression.
func queryQualityOnlyFactAdvanceProof(ctx context.Context, queryer upstreamReadQueryer, userID, baseline, current int64) (QualityOnlyFactAdvanceProof, error) {
	proof := QualityOnlyFactAdvanceProof{
		UserID: userID, BaselineFactVersion: baseline, CurrentFactVersion: current,
	}
	if userID <= 0 || baseline < 0 || current < 0 {
		return QualityOnlyFactAdvanceProof{}, ErrInvalid
	}
	if err := queryer.QueryRow(ctx, `SELECT fact_version
		FROM upstream_intelligence_fact_lineage_watermarks WHERE user_id=$1`, userID).Scan(&proof.LineageWatermark); err != nil {
		return QualityOnlyFactAdvanceProof{}, err
	}
	if baseline < proof.LineageWatermark || current <= baseline {
		return proof, nil
	}

	rows, err := queryer.Query(ctx, `SELECT user_id,fact_version,mutation_kind,evidence_id,created_at
		FROM upstream_intelligence_fact_mutations
		WHERE user_id=$1 AND fact_version>$2 AND fact_version<=$3
		ORDER BY fact_version`, userID, baseline, current)
	if err != nil {
		return QualityOnlyFactAdvanceProof{}, err
	}
	defer rows.Close()
	proof.Mutations = make([]UpstreamIntelligenceFactMutation, 0, current-baseline)
	complete, expected := true, baseline+1
	for rows.Next() {
		var mutation UpstreamIntelligenceFactMutation
		var kind string
		var evidenceID *string
		if err := rows.Scan(&mutation.UserID, &mutation.FactVersion, &kind, &evidenceID, &mutation.CreatedAt); err != nil {
			return QualityOnlyFactAdvanceProof{}, err
		}
		mutation.Kind = UpstreamIntelligenceFactMutationKind(kind)
		if evidenceID != nil {
			mutation.EvidenceID = *evidenceID
		}
		if mutation.UserID != userID || mutation.FactVersion != expected {
			complete = false
		}
		expected++
		proof.Mutations = append(proof.Mutations, mutation)
	}
	if err := rows.Err(); err != nil {
		return QualityOnlyFactAdvanceProof{}, err
	}
	proof.Complete = complete && expected == current+1 && int64(len(proof.Mutations)) == current-baseline
	return proof, nil
}

func queryRecommendationPlans(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]contracts.RoutePlan, error) {
	rows, err := queryer.Query(ctx, `SELECT `+planCols+` FROM route_plans WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.RoutePlan, 0)
	for rows.Next() {
		value, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func queryRecommendationChannels(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]contracts.UpstreamChannel, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("channel", channelCols)+` FROM upstream_channels AS channel
		JOIN upstream_channel_allocations AS allocation ON allocation.channel_id=channel.id AND allocation.user_id=$1 ORDER BY channel.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.UpstreamChannel, 0)
	for rows.Next() {
		value, scanErr := scanChannel(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func queryRecommendationBindings(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]contracts.PublishedBinding, error) {
	rows, err := queryer.Query(ctx, `SELECT binding.id,binding.plan_id,binding.instance_id,binding.channel_id,binding.remote_id,
		binding.account_ownership,binding.state,binding.last_error,binding.scheduling_generation,binding.verification_status,
		binding.verification_source,binding.verified_at,binding.verification_error_code,binding.created_at,binding.updated_at
		FROM published_bindings AS binding JOIN route_plans AS plan ON plan.id=binding.plan_id
		WHERE plan.user_id=$1 ORDER BY binding.plan_id,binding.channel_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contracts.PublishedBinding, 0)
	for rows.Next() {
		var value contracts.PublishedBinding
		var ownership, state, status, source string
		if err := rows.Scan(&value.ID, &value.PlanID, &value.InstanceID, &value.ChannelID, &value.RemoteID,
			&ownership, &state, &value.LastError, &value.SchedulingGeneration, &status, &source,
			&value.VerifiedAt, &value.VerificationErrorCode, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
		value.State = contracts.PublishedBindingState(state)
		value.VerificationStatus = contracts.PublishedBindingVerificationStatus(status)
		value.VerificationSource = contracts.PublishedBindingVerificationSource(source)
		result = append(result, value)
	}
	return result, rows.Err()
}

func normalizeUpstreamRecommendationInputs(value *UpstreamRecommendationInputs) {
	sort.Slice(value.CostFacts, func(i, j int) bool {
		if value.CostFacts[i].OccurredAt.Equal(value.CostFacts[j].OccurredAt) {
			return value.CostFacts[i].ID > value.CostFacts[j].ID
		}
		return value.CostFacts[i].OccurredAt.After(value.CostFacts[j].OccurredAt)
	})
	sort.Slice(value.RoutePlans, func(i, j int) bool { return value.RoutePlans[i].ID < value.RoutePlans[j].ID })
	sort.Slice(value.Channels, func(i, j int) bool { return value.Channels[i].ID < value.Channels[j].ID })
	sort.Slice(value.Bindings, func(i, j int) bool {
		if value.Bindings[i].PlanID == value.Bindings[j].PlanID {
			return value.Bindings[i].ChannelID < value.Bindings[j].ChannelID
		}
		return value.Bindings[i].PlanID < value.Bindings[j].PlanID
	})
}

func cloneRecommendationRoutePlan(value contracts.RoutePlan) contracts.RoutePlan {
	value.Labels = cloneRecommendationLabels(value.Labels)
	return value
}

func cloneRecommendationChannel(value contracts.UpstreamChannel) contracts.UpstreamChannel {
	value.Models = append([]string(nil), value.Models...)
	value.Groups = append([]string(nil), value.Groups...)
	value.Labels = cloneRecommendationLabels(value.Labels)
	return value
}

func cloneRecommendationLabels(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
