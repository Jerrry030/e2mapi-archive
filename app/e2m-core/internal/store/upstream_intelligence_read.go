package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"e2m.local/contracts"
)

const (
	// These are internal read-snapshot safety rails, not API page sizes. The
	// public API remains capped at contracts.MaxUpstreamIntelligenceListLimit.
	// A snapshot deliberately fails instead of returning a silently truncated
	// view that could produce a false ranking or aggregate.
	maxUpstreamIntelligenceReadSources = 1_000
	maxUpstreamIntelligenceReadOffers  = 50_000
	maxUpstreamIntelligenceReadChanges = 20_000
	maxUpstreamIntelligenceReadLinks   = 20_000
	maxUpstreamIntelligenceReadQuality = 50_000

	upstreamIntelligenceReadChangeHistory = 7 * 24 * time.Hour
)

// UpstreamIntelligenceCurrentSnapshot is the owner-scoped, internally
// consistent input used by UI-07 read models. It contains Core domain facts,
// not a browser response; the HTTP layer is still responsible for projecting
// away Connector-local identities and other implementation metadata.
//
// MemoryStore copies this value while holding one read lock. PostgresStore
// builds it in one read-only repeatable-read transaction. Consequently every
// collection below describes the same database snapshot and FactVersion.
type UpstreamIntelligenceCurrentSnapshot struct {
	UserID      int64
	FactVersion contracts.UpstreamIntelligenceFactVersion
	GeneratedAt time.Time
	Sources     []contracts.UpstreamIntelligenceSource
	LatestRuns  []contracts.UpstreamCollectionRun
	Wallets     []contracts.UpstreamWalletObservation
	Offers      []contracts.UpstreamOfferObservation
	Absences    []UpstreamSnapshotAbsence
	Changes     []contracts.UpstreamChangeEvent
	Links       []contracts.UpstreamIntelligenceLink
	// LinkResolutions are owner-derived safe targets keyed by LinkID. They never
	// expose the opaque source identity and are the only supported way for an
	// HTTP projection to turn a source_identity link into a channel id.
	LinkResolutions []UpstreamIntelligenceLinkResolution
	// QualitySnapshots contains at most one conservative Window5m snapshot per
	// allocated channel/model. Stale, low-sample and unknown evidence is kept so
	// the decision domain can explain why it is blocked; non-finite measurements
	// are the only quality rows discarded as unsafe to project.
	QualitySnapshots []contracts.ChannelHealthSnapshot
}

// UpstreamIntelligenceLinkResolution is internal read-model evidence that one
// explicit link resolves to one and only one channel allocated to the owner.
// TargetVerified means both the link verification and unique owner allocation
// proof succeeded in the same consistent-read boundary.
type UpstreamIntelligenceLinkResolution struct {
	LinkID                 string
	UserID                 int64
	ResolvedChannelID      string
	ResolvedChannelOwnerID int64
	TargetVerified         bool
}

// UpstreamIntelligenceEvidenceSnapshot resolves one immutable evidence id in
// the same consistent-read boundary. Exactly one of Wallet, Offer, or Change
// is non-nil. Source and Run are included for server-side projection; Run is
// nil for a change event that has no unambiguous related observation.
type UpstreamIntelligenceEvidenceSnapshot struct {
	UserID      int64
	FactVersion contracts.UpstreamIntelligenceFactVersion
	GeneratedAt time.Time
	Source      contracts.UpstreamIntelligenceSource
	Run         *contracts.UpstreamCollectionRun
	Wallet      *contracts.UpstreamWalletObservation
	Offer       *contracts.UpstreamOfferObservation
	Change      *contracts.UpstreamChangeEvent
}

// UpstreamIntelligenceReadStore is separate from the ingest interface so
// workers that only retain/write facts do not accidentally inherit dashboard
// concerns. Both production store implementations satisfy it.
type UpstreamIntelligenceReadStore interface {
	ReadUpstreamIntelligenceCurrent(context.Context, int64, *time.Time) (UpstreamIntelligenceCurrentSnapshot, error)
	ReadUpstreamIntelligenceEvidence(context.Context, int64, string) (UpstreamIntelligenceEvidenceSnapshot, error)
}

var (
	_                                       UpstreamIntelligenceReadStore = (*MemoryStore)(nil)
	_                                       UpstreamIntelligenceReadStore = (*PostgresStore)(nil)
	postgresUpstreamReadMigrationAssertions sync.Once
)

type upstreamReadQueryer interface {
	upstreamQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s *MemoryStore) ReadUpstreamIntelligenceCurrent(ctx context.Context, userID int64, referenceTime *time.Time) (UpstreamIntelligenceCurrentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	generatedAt := normalizeUpstreamTime(s.now())
	if referenceTime != nil {
		generatedAt = normalizeUpstreamTime(referenceTime.UTC())
	}
	snapshot := UpstreamIntelligenceCurrentSnapshot{
		UserID: userID, GeneratedAt: generatedAt,
		FactVersion: s.upstreamIntelVersions[userID],
	}
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
	changeCutoff := generatedAt.Add(-upstreamIntelligenceReadChangeHistory)
	for _, change := range s.upstreamIntelChanges {
		if change.UserID == userID && !change.ConfirmedAt.Before(changeCutoff) {
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
			snapshot.LinkResolutions = append(snapshot.LinkResolutions,
				memoryUpstreamLinkResolution(s.channelAllocations, cloned))
		}
	}
	if len(snapshot.Links) > maxUpstreamIntelligenceReadLinks {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	snapshot.QualitySnapshots = memoryUpstreamReadQualitySnapshots(
		s.channelSnapshots, s.channelAllocations, s.instances, userID, generatedAt)
	if len(snapshot.QualitySnapshots) > maxUpstreamIntelligenceReadQuality {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	normalizeUpstreamIntelligenceCurrentOrder(&snapshot)
	return snapshot, nil
}

func (s *MemoryStore) ReadUpstreamIntelligenceEvidence(ctx context.Context, userID int64, evidenceID string) (UpstreamIntelligenceEvidenceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, err
	}
	if err := requireUpstreamOwner(userID); err != nil || evidenceID == "" {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := UpstreamIntelligenceEvidenceSnapshot{
		UserID: userID, FactVersion: s.upstreamIntelVersions[userID], GeneratedAt: normalizeUpstreamTime(s.now()),
	}
	result.FactVersion.UserID = userID
	matchCount := 0
	var runID, sourceID string
	for _, wallet := range s.upstreamIntelWallets {
		if wallet.UserID == userID && wallet.ID == evidenceID && s.memoryUpstreamEvidenceRunFinalizedLocked(userID, wallet.RunID) {
			copyWallet := cloneUpstreamWallet(wallet)
			result.Wallet, runID, sourceID = &copyWallet, wallet.RunID, wallet.SourceID
			matchCount++
		}
	}
	for _, offer := range s.upstreamIntelOffers {
		if offer.UserID == userID && offer.ID == evidenceID && s.memoryUpstreamEvidenceRunFinalizedLocked(userID, offer.RunID) {
			copyOffer := cloneUpstreamOffer(offer)
			result.Offer, runID, sourceID = &copyOffer, offer.RunID, offer.SourceID
			matchCount++
		}
	}
	for _, change := range s.upstreamIntelChanges {
		if change.UserID == userID && change.ID == evidenceID {
			copyChange := cloneUpstreamChange(change)
			result.Change, sourceID = &copyChange, change.SourceID
			matchCount++
		}
	}
	if matchCount == 0 {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrNotFound
	}
	if matchCount != 1 {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrConflict
	}
	for _, source := range s.upstreamIntelSources {
		if source.UserID == userID && source.ID == sourceID {
			result.Source = cloneUpstreamIntelligenceSource(source)
			break
		}
	}
	if result.Source.ID == "" {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrNotFound
	}
	if runID != "" {
		for _, run := range s.upstreamIntelRuns {
			if run.UserID == userID && run.ID == runID {
				copyRun := cloneUpstreamRun(run)
				result.Run = &copyRun
				break
			}
		}
		if result.Run == nil {
			return UpstreamIntelligenceEvidenceSnapshot{}, ErrNotFound
		}
	}
	return result, nil
}

func (s *MemoryStore) memoryUpstreamEvidenceRunFinalizedLocked(userID int64, runID string) bool {
	for _, run := range s.upstreamIntelRuns {
		if run.UserID == userID && run.ID == runID {
			return run.FinalizedFactVersion > 0
		}
	}
	return false
}

func (s *PostgresStore) ReadUpstreamIntelligenceCurrent(ctx context.Context, userID int64, referenceTime *time.Time) (UpstreamIntelligenceCurrentSnapshot, error) {
	if err := requireUpstreamOwner(userID); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	assertPostgresUpstreamReadColumns()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot := UpstreamIntelligenceCurrentSnapshot{UserID: userID}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&snapshot.GeneratedAt); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	snapshot.GeneratedAt = normalizeUpstreamTime(snapshot.GeneratedAt)
	if referenceTime != nil {
		snapshot.GeneratedAt = normalizeUpstreamTime(referenceTime.UTC())
	}
	if err := readUpstreamFactVersionQuery(ctx, tx, userID, &snapshot.FactVersion); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}

	if snapshot.Sources, err = queryUpstreamReadSources(ctx, tx, userID, maxUpstreamIntelligenceReadSources+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.Sources) > maxUpstreamIntelligenceReadSources {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if snapshot.LatestRuns, err = queryUpstreamReadLatestRuns(ctx, tx, userID); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if snapshot.Wallets, err = queryUpstreamReadWallets(ctx, tx, userID); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if snapshot.Offers, err = queryUpstreamReadOffers(ctx, tx, userID, maxUpstreamIntelligenceReadOffers+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.Offers) > maxUpstreamIntelligenceReadOffers {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if snapshot.Absences, err = queryUpstreamReadAbsences(ctx, tx, userID); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if snapshot.Changes, err = queryUpstreamReadChanges(ctx, tx, userID, snapshot.GeneratedAt.Add(-upstreamIntelligenceReadChangeHistory), maxUpstreamIntelligenceReadChanges+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.Changes) > maxUpstreamIntelligenceReadChanges {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if snapshot.Links, err = queryUpstreamReadLinks(ctx, tx, userID, maxUpstreamIntelligenceReadLinks+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.Links) > maxUpstreamIntelligenceReadLinks {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if snapshot.LinkResolutions, err = queryUpstreamReadLinkResolutions(ctx, tx, userID, maxUpstreamIntelligenceReadLinks+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.LinkResolutions) > maxUpstreamIntelligenceReadLinks {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if snapshot.QualitySnapshots, err = queryUpstreamReadQualitySnapshots(ctx, tx, userID, snapshot.GeneratedAt, maxUpstreamIntelligenceReadQuality+1); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	if len(snapshot.QualitySnapshots) > maxUpstreamIntelligenceReadQuality {
		return UpstreamIntelligenceCurrentSnapshot{}, ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamIntelligenceCurrentSnapshot{}, err
	}
	filterConfirmedRemovedUpstreamOffers(&snapshot)
	normalizeUpstreamIntelligenceCurrentOrder(&snapshot)
	return snapshot, nil
}

func (s *PostgresStore) ReadUpstreamIntelligenceEvidence(ctx context.Context, userID int64, evidenceID string) (UpstreamIntelligenceEvidenceSnapshot, error) {
	if err := requireUpstreamOwner(userID); err != nil || evidenceID == "" {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrInvalid
	}
	assertPostgresUpstreamReadColumns()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result := UpstreamIntelligenceEvidenceSnapshot{UserID: userID}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&result.GeneratedAt); err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, err
	}
	result.GeneratedAt = normalizeUpstreamTime(result.GeneratedAt)
	if err := readUpstreamFactVersionQuery(ctx, tx, userID, &result.FactVersion); err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, err
	}

	matchCount := 0
	var runID, sourceID string
	if wallet, found, queryErr := queryUniqueUpstreamWalletEvidence(ctx, tx, userID, evidenceID); queryErr != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, queryErr
	} else if found {
		result.Wallet, runID, sourceID, matchCount = &wallet, wallet.RunID, wallet.SourceID, matchCount+1
	}
	if offer, found, queryErr := queryUniqueUpstreamOfferEvidence(ctx, tx, userID, evidenceID); queryErr != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, queryErr
	} else if found {
		result.Offer, runID, sourceID, matchCount = &offer, offer.RunID, offer.SourceID, matchCount+1
	}
	if change, found, queryErr := queryUniqueUpstreamChangeEvidence(ctx, tx, userID, evidenceID); queryErr != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, queryErr
	} else if found {
		result.Change, sourceID, matchCount = &change, change.SourceID, matchCount+1
	}
	if matchCount == 0 {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrNotFound
	}
	if matchCount != 1 {
		return UpstreamIntelligenceEvidenceSnapshot{}, ErrConflict
	}
	result.Source, err = scanUpstreamSource(tx.QueryRow(ctx, `SELECT `+upstreamSourceCols+` FROM upstream_intelligence_sources WHERE user_id=$1 AND id=$2`, userID, sourceID))
	if err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, mapNotFound(err)
	}
	if runID != "" {
		run, runErr := scanUpstreamRun(tx.QueryRow(ctx, `SELECT `+upstreamRunCols+` FROM upstream_collection_runs WHERE user_id=$1 AND id=$2`, userID, runID))
		if runErr != nil {
			return UpstreamIntelligenceEvidenceSnapshot{}, mapNotFound(runErr)
		}
		result.Run = &run
	}
	if err := tx.Commit(ctx); err != nil {
		return UpstreamIntelligenceEvidenceSnapshot{}, err
	}
	return result, nil
}

func readUpstreamFactVersionQuery(ctx context.Context, queryer upstreamQueryer, userID int64, version *contracts.UpstreamIntelligenceFactVersion) error {
	err := queryer.QueryRow(ctx, `SELECT user_id,fact_version,updated_at FROM upstream_intelligence_fact_versions WHERE user_id=$1`, userID).
		Scan(&version.UserID, &version.FactVersion, &version.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		*version = contracts.UpstreamIntelligenceFactVersion{UserID: userID}
		return nil
	}
	return err
}

func queryUpstreamReadSources(ctx context.Context, queryer upstreamReadQueryer, userID int64, limit int) ([]contracts.UpstreamIntelligenceSource, error) {
	rows, err := queryer.Query(ctx, `SELECT `+upstreamSourceCols+` FROM upstream_intelligence_sources WHERE user_id=$1 ORDER BY id LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamIntelligenceSource, 0)
	for rows.Next() {
		value, scanErr := scanUpstreamSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadLatestRuns(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]contracts.UpstreamCollectionRun, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("ranked", upstreamRunCols)+` FROM (
		SELECT run.*,row_number() OVER (PARTITION BY source_id ORDER BY observed_at DESC,id DESC) AS read_rank
		FROM upstream_collection_runs AS run WHERE user_id=$1 AND finalized_fact_version>0
	) AS ranked WHERE read_rank=1 ORDER BY source_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamCollectionRun, 0)
	for rows.Next() {
		value, scanErr := scanUpstreamRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadWallets(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]contracts.UpstreamWalletObservation, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("ranked", walletCols)+` FROM (
		SELECT wallet.*,row_number() OVER (PARTITION BY wallet.source_id ORDER BY wallet.observed_at DESC,wallet.run_id DESC,wallet.id DESC) AS read_rank
		FROM upstream_wallet_observations AS wallet
		JOIN upstream_collection_runs AS run ON run.user_id=wallet.user_id AND run.id=wallet.run_id
		WHERE wallet.user_id=$1 AND run.finalized_fact_version>0
	) AS ranked WHERE read_rank=1 ORDER BY source_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamWalletObservation, 0)
	for rows.Next() {
		value, scanErr := scanWallet(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadOffers(ctx context.Context, queryer upstreamReadQueryer, userID int64, limit int) ([]contracts.UpstreamOfferObservation, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("ranked", offerCols)+` FROM (
		SELECT offer.*,row_number() OVER (
			PARTITION BY offer.source_id,offer.group_key,offer.model_key,offer.price_dimension
			ORDER BY offer.observed_at DESC,offer.run_id DESC,offer.id DESC
		) AS read_rank
		FROM upstream_offer_observations AS offer
		JOIN upstream_collection_runs AS run ON run.user_id=offer.user_id AND run.id=offer.run_id
		WHERE offer.user_id=$1 AND run.finalized_fact_version>0
	) AS ranked WHERE read_rank=1 ORDER BY source_id,group_key,model_key,price_dimension LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamOfferObservation, 0)
	for rows.Next() {
		value, scanErr := scanOffer(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadAbsences(ctx context.Context, queryer upstreamReadQueryer, userID int64) ([]UpstreamSnapshotAbsence, error) {
	rows, err := queryer.Query(ctx, `SELECT `+absenceCols+` FROM upstream_snapshot_absences WHERE user_id=$1 ORDER BY source_id,comparison_key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UpstreamSnapshotAbsence, 0)
	for rows.Next() {
		value, scanErr := scanAbsence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadChanges(ctx context.Context, queryer upstreamReadQueryer, userID int64, since time.Time, limit int) ([]contracts.UpstreamChangeEvent, error) {
	rows, err := queryer.Query(ctx, `SELECT `+changeCols+` FROM upstream_change_events
		WHERE user_id=$1 AND confirmed_at >= $2 ORDER BY confirmed_at DESC,id DESC LIMIT $3`, userID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamChangeEvent, 0)
	for rows.Next() {
		value, scanErr := scanChange(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadLinks(ctx context.Context, queryer upstreamReadQueryer, userID int64, limit int) ([]contracts.UpstreamIntelligenceLink, error) {
	rows, err := queryer.Query(ctx, `SELECT `+linkCols+` FROM upstream_intelligence_links WHERE user_id=$1 ORDER BY id LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamIntelligenceLink, 0)
	for rows.Next() {
		value, scanErr := scanLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadLinkResolutions(ctx context.Context, queryer upstreamReadQueryer, userID int64, limit int) ([]UpstreamIntelligenceLinkResolution, error) {
	// The lateral subquery deliberately keeps the match count. A
	// source_identity target is verified only when exactly one owner allocation
	// resolves it; no arbitrary MIN/MAX channel may become a trusted join.
	rows, err := queryer.Query(ctx, `SELECT link.id,link.user_id,
		COALESCE(target.channel_id,''),COALESCE(target.user_id,0),
		(link.status='active' AND link.verified_at IS NOT NULL AND link.price_dimension<>'' AND target.match_count=1)
	FROM upstream_intelligence_links AS link
	LEFT JOIN LATERAL (
		SELECT MIN(allocation.channel_id) AS channel_id,
		       MIN(allocation.user_id) AS user_id,
		       COUNT(*) AS match_count
		FROM upstream_channel_allocations AS allocation
		WHERE allocation.user_id=link.user_id AND (
			(link.link_scope='channel' AND allocation.channel_id=link.channel_id) OR
			(link.link_scope='source_identity' AND allocation.source_id=link.upstream_source_identity)
		)
	) AS target ON TRUE
	WHERE link.user_id=$1 ORDER BY link.id LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UpstreamIntelligenceLinkResolution, 0)
	for rows.Next() {
		var value UpstreamIntelligenceLinkResolution
		if err := rows.Scan(&value.LinkID, &value.UserID, &value.ResolvedChannelID,
			&value.ResolvedChannelOwnerID, &value.TargetVerified); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUpstreamReadQualitySnapshots(ctx context.Context, queryer upstreamReadQueryer, userID int64, referenceTime time.Time, limit int) ([]contracts.ChannelHealthSnapshot, error) {
	// One owner-scoped allocation join is mandatory: instances alone are not a
	// durable ownership proof. First choose the current row for every concrete
	// instance scope at the fixed page reference time. Then select the
	// conservative least-favorable scope per channel/model. Window5m is the
	// Frontier contract. Unknown evidence sorts ahead of scored evidence so one
	// unhealthy/unknown instance cannot be hidden by a healthy peer. Freshness,
	// sample sufficiency and score bounds remain domain decisions. PostgreSQL
	// DOUBLE PRECISION admits NaN and infinity, so explicitly reject them before
	// ordering or projecting the row.
	rows, err := queryer.Query(ctx, `WITH current_scopes AS (
		SELECT DISTINCT ON (snapshot.instance_id,snapshot.channel_id,snapshot.model,snapshot.capability,snapshot.endpoint_path,snapshot."window")
		       `+prefixedUpstreamReadColumns("snapshot", channelHealthSnapshotCols)+`
		FROM channel_health_snapshots AS snapshot
		JOIN upstream_channel_allocations AS allocation
		  ON allocation.channel_id=snapshot.channel_id AND allocation.user_id=$1
		JOIN instances AS instance
		  ON instance.id=snapshot.instance_id AND instance.user_id=allocation.user_id
		WHERE snapshot.bucket_start <= $2 AND snapshot.created_at <= $2
		  AND snapshot."window"='5m'
		ORDER BY snapshot.instance_id,snapshot.channel_id,snapshot.model,snapshot.capability,snapshot.endpoint_path,snapshot."window",
		         snapshot.bucket_start DESC,snapshot.created_at DESC,snapshot.id DESC
	), projectable AS (
		SELECT current_scopes.*,
		       row_number() OVER (
		         PARTITION BY channel_id,model
		         ORDER BY (health_state='unknown') DESC,
		                  quality_score ASC,quality_success_rate ASC,ttft_p95 DESC,duration_p95 DESC,
		                  quality_sample_count ASC,id ASC
		       ) AS conservative_rank
		FROM current_scopes
		WHERE quality_score NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)
		  AND quality_success_rate NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)
		  AND ttft_p95 NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)
		  AND duration_p95 NOT IN ('NaN'::double precision,'Infinity'::double precision,'-Infinity'::double precision)
	)
	SELECT `+prefixedUpstreamReadColumns("projectable", channelHealthSnapshotCols)+`
	FROM projectable WHERE conservative_rank=1
	ORDER BY channel_id,model LIMIT $3`, userID, referenceTime, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.ChannelHealthSnapshot, 0)
	for rows.Next() {
		value, scanErr := scanChannelHealthSnapshot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func queryUniqueUpstreamWalletEvidence(ctx context.Context, queryer upstreamReadQueryer, userID int64, id string) (contracts.UpstreamWalletObservation, bool, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("wallet", walletCols)+` FROM upstream_wallet_observations AS wallet
		WHERE wallet.user_id=$1 AND wallet.id=$2 AND EXISTS (
			SELECT 1 FROM upstream_collection_runs AS run
			WHERE run.user_id=wallet.user_id AND run.id=wallet.run_id AND run.finalized_fact_version>0
		) ORDER BY wallet.observed_at DESC LIMIT 2`, userID, id)
	if err != nil {
		return contracts.UpstreamWalletObservation{}, false, err
	}
	defer rows.Close()
	var value contracts.UpstreamWalletObservation
	count := 0
	for rows.Next() {
		if count > 0 {
			return contracts.UpstreamWalletObservation{}, false, ErrConflict
		}
		if value, err = scanWallet(rows); err != nil {
			return contracts.UpstreamWalletObservation{}, false, err
		}
		count++
	}
	return value, count == 1, rows.Err()
}

func queryUniqueUpstreamOfferEvidence(ctx context.Context, queryer upstreamReadQueryer, userID int64, id string) (contracts.UpstreamOfferObservation, bool, error) {
	rows, err := queryer.Query(ctx, `SELECT `+prefixedUpstreamReadColumns("offer", offerCols)+` FROM upstream_offer_observations AS offer
		WHERE offer.user_id=$1 AND offer.id=$2 AND EXISTS (
			SELECT 1 FROM upstream_collection_runs AS run
			WHERE run.user_id=offer.user_id AND run.id=offer.run_id AND run.finalized_fact_version>0
		) ORDER BY offer.observed_at DESC LIMIT 2`, userID, id)
	if err != nil {
		return contracts.UpstreamOfferObservation{}, false, err
	}
	defer rows.Close()
	var value contracts.UpstreamOfferObservation
	count := 0
	for rows.Next() {
		if count > 0 {
			return contracts.UpstreamOfferObservation{}, false, ErrConflict
		}
		if value, err = scanOffer(rows); err != nil {
			return contracts.UpstreamOfferObservation{}, false, err
		}
		count++
	}
	return value, count == 1, rows.Err()
}

func queryUniqueUpstreamChangeEvidence(ctx context.Context, queryer upstreamReadQueryer, userID int64, id string) (contracts.UpstreamChangeEvent, bool, error) {
	rows, err := queryer.Query(ctx, `SELECT `+changeCols+` FROM upstream_change_events WHERE user_id=$1 AND id=$2 LIMIT 2`, userID, id)
	if err != nil {
		return contracts.UpstreamChangeEvent{}, false, err
	}
	defer rows.Close()
	var value contracts.UpstreamChangeEvent
	count := 0
	for rows.Next() {
		if count > 0 {
			return contracts.UpstreamChangeEvent{}, false, ErrConflict
		}
		if value, err = scanChange(rows); err != nil {
			return contracts.UpstreamChangeEvent{}, false, err
		}
		count++
	}
	return value, count == 1, rows.Err()
}

func upstreamReadRunNewer(candidate, current contracts.UpstreamCollectionRun) bool {
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.ObservedAt.Equal(current.ObservedAt) && candidate.ID > current.ID
}

func upstreamReadWalletNewer(candidate, current contracts.UpstreamWalletObservation) bool {
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.ObservedAt.Equal(current.ObservedAt) &&
		(candidate.RunID > current.RunID || candidate.RunID == current.RunID && candidate.ID > current.ID)
}

func upstreamReadOfferNewer(candidate, current contracts.UpstreamOfferObservation) bool {
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.ObservedAt.Equal(current.ObservedAt) &&
		(candidate.RunID > current.RunID || candidate.RunID == current.RunID && candidate.ID > current.ID)
}

func upstreamReadOfferKey(offer contracts.UpstreamOfferObservation) string {
	return offer.SourceID + "\x00" + offer.GroupKey + "\x00" + offer.ModelKey + "\x00" + string(offer.PriceDimension)
}

func memoryUpstreamLinkResolution(allocations map[string]upstreamChannelAllocation, link contracts.UpstreamIntelligenceLink) UpstreamIntelligenceLinkResolution {
	result := UpstreamIntelligenceLinkResolution{LinkID: link.ID, UserID: link.UserID}
	var channels []string
	switch link.Scope {
	case contracts.UpstreamLinkChannel:
		if allocation, ok := allocations[link.ChannelID]; ok && allocation.UserID == link.UserID {
			channels = []string{link.ChannelID}
		}
	case contracts.UpstreamLinkSourceIdentity:
		channels = memoryUpstreamSourceIdentityAllocatedChannels(allocations, link.UserID, link.UpstreamSourceIdentity)
	}
	if len(channels) == 1 {
		result.ResolvedChannelID = channels[0]
		result.ResolvedChannelOwnerID = link.UserID
		result.TargetVerified = link.Status == contracts.UpstreamLinkActive && link.VerifiedAt != nil &&
			!link.VerifiedAt.IsZero() && link.PriceDimension != ""
	}
	return result
}

func memoryUpstreamReadQualitySnapshots(snapshots []contracts.ChannelHealthSnapshot, allocations map[string]upstreamChannelAllocation, instances []contracts.Instance, userID int64, referenceTime time.Time) []contracts.ChannelHealthSnapshot {
	// First collapse history to the current row for each concrete instance scope
	// at the fixed reference time. The second pass conservatively chooses the
	// least favorable projectable instance for each channel/model. Window5m is
	// fixed by the Frontier contract; stale, insufficient and unknown evidence
	// is intentionally retained for the domain to classify.
	ownedInstances := make(map[string]struct{})
	for _, instance := range instances {
		if instance.UserID == userID {
			ownedInstances[instance.ID] = struct{}{}
		}
	}
	currentScopes := make(map[string]contracts.ChannelHealthSnapshot)
	for _, candidate := range snapshots {
		allocation, ok := allocations[candidate.ChannelID]
		_, instanceOwned := ownedInstances[candidate.InstanceID]
		if !ok || allocation.UserID != userID || !instanceOwned || candidate.Window != contracts.Window5m ||
			candidate.BucketStart.After(referenceTime) || candidate.CreatedAt.After(referenceTime) {
			continue
		}
		key := upstreamQualityScopeKey(candidate)
		if current, exists := currentScopes[key]; !exists || upstreamQualitySnapshotNewer(candidate, current) {
			currentScopes[key] = candidate
		}
	}
	selected := make(map[string]contracts.ChannelHealthSnapshot)
	for _, candidate := range currentScopes {
		if !projectableUpstreamQualitySnapshot(candidate) {
			continue
		}
		key := upstreamQualityCohortKey(candidate)
		if current, exists := selected[key]; !exists || worseUpstreamQualitySnapshot(candidate, current) {
			selected[key] = candidate
		}
	}
	out := make([]contracts.ChannelHealthSnapshot, 0, len(selected))
	for _, candidate := range selected {
		out = append(out, candidate)
	}
	return out
}

func upstreamQualityScopeKey(value contracts.ChannelHealthSnapshot) string {
	return value.InstanceID + "\x00" + value.ChannelID + "\x00" + value.Model + "\x00" +
		string(value.Capability) + "\x00" + value.EndpointPath + "\x00" + string(value.Window)
}

func upstreamQualityCohortKey(value contracts.ChannelHealthSnapshot) string {
	return value.ChannelID + "\x00" + value.Model + "\x00" + string(value.Window)
}

func upstreamQualitySnapshotNewer(candidate, current contracts.ChannelHealthSnapshot) bool {
	if !candidate.BucketStart.Equal(current.BucketStart) {
		return candidate.BucketStart.After(current.BucketStart)
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

func projectableUpstreamQualitySnapshot(value contracts.ChannelHealthSnapshot) bool {
	return finiteUpstreamQualityMetric(value.QualityScore) && finiteUpstreamQualityMetric(value.QualitySuccessRate) &&
		finiteUpstreamQualityMetric(value.TTFTP95) && finiteUpstreamQualityMetric(value.DurationP95)
}

func finiteUpstreamQualityMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func worseUpstreamQualitySnapshot(candidate, current contracts.ChannelHealthSnapshot) bool {
	if candidate.HealthState == contracts.HealthUnknown || current.HealthState == contracts.HealthUnknown {
		return candidate.HealthState == contracts.HealthUnknown && current.HealthState != contracts.HealthUnknown
	}
	if candidate.QualityScore != current.QualityScore {
		return candidate.QualityScore < current.QualityScore
	}
	if candidate.QualitySuccessRate != current.QualitySuccessRate {
		return candidate.QualitySuccessRate < current.QualitySuccessRate
	}
	if candidate.TTFTP95 != current.TTFTP95 {
		return candidate.TTFTP95 > current.TTFTP95
	}
	if candidate.DurationP95 != current.DurationP95 {
		return candidate.DurationP95 > current.DurationP95
	}
	if candidate.QualitySampleCount != current.QualitySampleCount {
		return candidate.QualitySampleCount < current.QualitySampleCount
	}
	return candidate.ID < current.ID
}

// The store's scan-column constants intentionally include casts such as
// amount::text. Prefix every underlying identifier while keeping aliases and
// casts intact so derived-table and EXISTS queries cannot become ambiguous.
func prefixedUpstreamReadColumns(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for index, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			parts[index] = alias + "." + trimmed
		}
	}
	return strings.Join(parts, ",")
}

func assertPostgresUpstreamReadColumns() {
	postgresUpstreamReadMigrationAssertions.Do(func() {
		// These constants are hand-maintained scan projections. Exercising all of
		// them here makes accidental duplicate/ambiguous identifiers visible in
		// tests before a production query is reached. The function intentionally
		// has no side effects after its first call.
		for _, columns := range []string{upstreamRunCols, walletCols, offerCols} {
			if prefixedUpstreamReadColumns("read", columns) == "" {
				panic("store: empty upstream intelligence read projection")
			}
		}
	})
}

func normalizeUpstreamIntelligenceCurrentOrder(snapshot *UpstreamIntelligenceCurrentSnapshot) {
	filterConfirmedRemovedUpstreamOffers(snapshot)
	sort.Slice(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].ID < snapshot.Sources[j].ID })
	sort.Slice(snapshot.LatestRuns, func(i, j int) bool { return snapshot.LatestRuns[i].SourceID < snapshot.LatestRuns[j].SourceID })
	sort.Slice(snapshot.Wallets, func(i, j int) bool { return snapshot.Wallets[i].SourceID < snapshot.Wallets[j].SourceID })
	sort.Slice(snapshot.Offers, func(i, j int) bool {
		left, right := upstreamReadOfferKey(snapshot.Offers[i]), upstreamReadOfferKey(snapshot.Offers[j])
		return left < right
	})
	sort.Slice(snapshot.Absences, func(i, j int) bool {
		if snapshot.Absences[i].SourceID != snapshot.Absences[j].SourceID {
			return snapshot.Absences[i].SourceID < snapshot.Absences[j].SourceID
		}
		return snapshot.Absences[i].ComparisonKey < snapshot.Absences[j].ComparisonKey
	})
	sort.Slice(snapshot.Changes, func(i, j int) bool {
		if !snapshot.Changes[i].ConfirmedAt.Equal(snapshot.Changes[j].ConfirmedAt) {
			return snapshot.Changes[i].ConfirmedAt.After(snapshot.Changes[j].ConfirmedAt)
		}
		return snapshot.Changes[i].ID > snapshot.Changes[j].ID
	})
	sort.Slice(snapshot.Links, func(i, j int) bool { return snapshot.Links[i].ID < snapshot.Links[j].ID })
	sort.Slice(snapshot.LinkResolutions, func(i, j int) bool { return snapshot.LinkResolutions[i].LinkID < snapshot.LinkResolutions[j].LinkID })
	sort.Slice(snapshot.QualitySnapshots, func(i, j int) bool {
		left, right := upstreamQualityCohortKey(snapshot.QualitySnapshots[i]), upstreamQualityCohortKey(snapshot.QualitySnapshots[j])
		if left != right {
			return left < right
		}
		return snapshot.QualitySnapshots[i].ID < snapshot.QualitySnapshots[j].ID
	})
}

func cloneUpstreamIntelligenceSource(value contracts.UpstreamIntelligenceSource) contracts.UpstreamIntelligenceSource {
	value.LastRunAt = normalizeUpstreamTimePtr(value.LastRunAt)
	value.LastSuccessAt = normalizeUpstreamTimePtr(value.LastSuccessAt)
	value.NextPollAt = normalizeUpstreamTimePtr(value.NextPollAt)
	return value
}

func cloneUpstreamWallet(value contracts.UpstreamWalletObservation) contracts.UpstreamWalletObservation {
	value.BalanceAmount = cloneUpstreamDecimal(value.BalanceAmount)
	value.Confidence = cloneUpstreamDecimal(value.Confidence)
	value.MissingFields = append([]string(nil), value.MissingFields...)
	return value
}

func cloneUpstreamOffer(value contracts.UpstreamOfferObservation) contracts.UpstreamOfferObservation {
	value.GroupMultiplier = cloneUpstreamDecimal(value.GroupMultiplier)
	value.RechargeYield = cloneUpstreamDecimal(value.RechargeYield)
	value.PublishedUnitPrice = cloneUpstreamDecimal(value.PublishedUnitPrice)
	value.EffectiveMultiplier = cloneUpstreamDecimal(value.EffectiveMultiplier)
	value.EffectiveUnitCost = cloneUpstreamDecimal(value.EffectiveUnitCost)
	value.Confidence = cloneUpstreamDecimal(value.Confidence)
	value.ValidUntil = normalizeUpstreamTimePtr(value.ValidUntil)
	value.MissingFields = append([]string(nil), value.MissingFields...)
	return value
}

func cloneUpstreamAbsence(value UpstreamSnapshotAbsence) UpstreamSnapshotAbsence {
	value.FirstAbsentAt = normalizeUpstreamTimePtr(value.FirstAbsentAt)
	return value
}

func cloneUpstreamChange(value contracts.UpstreamChangeEvent) contracts.UpstreamChangeEvent {
	value.AbsoluteChange = cloneUpstreamDecimal(value.AbsoluteChange)
	value.PercentageChange = cloneUpstreamDecimal(value.PercentageChange)
	if value.ImpactScope != nil {
		original := value.ImpactScope
		value.ImpactScope = make(map[string]string, len(original))
		for key, item := range original {
			value.ImpactScope[key] = item
		}
	}
	return value
}

func cloneUpstreamRun(value contracts.UpstreamCollectionRun) contracts.UpstreamCollectionRun {
	value.CompletedAt = normalizeUpstreamTimePtr(value.CompletedAt)
	return value
}

func cloneUpstreamLink(value contracts.UpstreamIntelligenceLink) contracts.UpstreamIntelligenceLink {
	value.VerifiedAt = normalizeUpstreamTimePtr(value.VerifiedAt)
	return value
}

func cloneUpstreamDecimal(value *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func filterConfirmedRemovedUpstreamOffers(snapshot *UpstreamIntelligenceCurrentSnapshot) {
	if snapshot == nil || len(snapshot.Offers) == 0 || len(snapshot.Absences) == 0 {
		return
	}
	removed := make(map[string]struct{})
	for _, absence := range snapshot.Absences {
		if absence.ConsecutiveCompleteRuns < 2 {
			continue
		}
		presence, err := parseUpstreamAbsenceComparisonKey(absence.ComparisonKey)
		if err != nil {
			continue
		}
		if presence.EventType == contracts.UpstreamChangeGroupRemoved {
			removed[absence.SourceID+"\x00group\x00"+presence.GroupKey] = struct{}{}
		} else {
			removed[absence.SourceID+"\x00model\x00"+presence.GroupKey+"\x00"+presence.ModelKey] = struct{}{}
		}
	}
	filtered := snapshot.Offers[:0]
	for _, offer := range snapshot.Offers {
		if _, absent := removed[offer.SourceID+"\x00group\x00"+offer.GroupKey]; absent {
			continue
		}
		if _, absent := removed[offer.SourceID+"\x00model\x00"+offer.GroupKey+"\x00"+offer.ModelKey]; absent {
			continue
		}
		filtered = append(filtered, offer)
	}
	snapshot.Offers = filtered
}
