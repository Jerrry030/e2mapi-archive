package store

import (
	"context"
	"sort"
	"time"

	"e2m.local/contracts"
)

// UpstreamMarginReadStore is intentionally separate from the public list API:
// a margin coverage result must never be computed from a silently truncated
// page. The hard bound fails closed instead of returning partial accounting.
type UpstreamMarginReadStore interface {
	ReadUpstreamMarginCostFacts(context.Context, int64, time.Time, time.Time) ([]contracts.UpstreamCostFact, error)
}

const maxUpstreamMarginCostFacts = 50_000

var (
	_ UpstreamMarginReadStore = (*MemoryStore)(nil)
	_ UpstreamMarginReadStore = (*PostgresStore)(nil)
)

func validUpstreamMarginWindow(userID int64, since, until time.Time) bool {
	return userID > 0 && !since.IsZero() && !until.IsZero() && since.Before(until)
}

func (s *MemoryStore) ReadUpstreamMarginCostFacts(ctx context.Context, userID int64, since, until time.Time) ([]contracts.UpstreamCostFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validUpstreamMarginWindow(userID, since, until) {
		return nil, ErrInvalid
	}
	since, until = normalizeUpstreamTime(since), normalizeUpstreamTime(until)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamCostFact, 0)
	for _, fact := range s.upstreamCostFacts {
		if fact.UserID != userID || fact.OccurredAt.Before(since) || !fact.OccurredAt.Before(until) {
			continue
		}
		if len(out) == maxUpstreamMarginCostFacts {
			return nil, ErrConflict
		}
		out = append(out, cloneUpstreamCostFact(fact))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	return out, nil
}

func (s *PostgresStore) ReadUpstreamMarginCostFacts(ctx context.Context, userID int64, since, until time.Time) ([]contracts.UpstreamCostFact, error) {
	if !validUpstreamMarginWindow(userID, since, until) {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+upstreamCostFactCols+` FROM upstream_cost_facts
		WHERE user_id=$1 AND occurred_at >= $2 AND occurred_at < $3
		ORDER BY occurred_at DESC,id DESC LIMIT $4`, userID, since, until, maxUpstreamMarginCostFacts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.UpstreamCostFact, 0)
	for rows.Next() {
		fact, scanErr := scanUpstreamCostFact(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if len(out) == maxUpstreamMarginCostFacts {
			return nil, ErrConflict
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
