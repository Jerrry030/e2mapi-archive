package store

import (
	"context"
	"strings"
	"time"

	"e2m.local/core/internal/operationalmetrics"
	"github.com/jackc/pgx/v5"
)

// operationalEventKind is deliberately closed and label-safe. These counters
// are global durable facts: no owner, source, request, URL, or rejected value
// is persisted with an event.
type operationalEventKind string

type operationalMetricKey struct {
	Metric string
	Label  string
}

const (
	operationalEventCredentialLeakDetected operationalEventKind = "credential_leak_detected"
	operationalEventCrossOwnerRejected     operationalEventKind = "cross_owner_rejected"
	operationalEventFalseRemovalInvariant  operationalEventKind = "false_removal_invariant"
)

type OperationalEventRecorder interface {
	RecordCredentialLeakDetected(context.Context) error
	RecordCrossOwnerRejected(context.Context) error
	RecordFalseRemovalInvariant(context.Context) error
}

func AsOperationalEventRecorder(st any) (OperationalEventRecorder, bool) {
	recorder, ok := st.(OperationalEventRecorder)
	return recorder, ok
}

func (s *MemoryStore) RecordCredentialLeakDetected(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventCredentialLeakDetected)
}

func (s *MemoryStore) RecordCrossOwnerRejected(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventCrossOwnerRejected)
}

func (s *MemoryStore) RecordFalseRemovalInvariant(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventFalseRemovalInvariant)
}

func (s *MemoryStore) recordOperationalEvent(ctx context.Context, kind operationalEventKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validOperationalEventKind(kind) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operationalEventCounters == nil {
		s.operationalEventCounters = make(map[operationalEventKind]int64)
	}
	s.operationalEventCounters[kind]++
	return nil
}

func (s *PostgresStore) RecordCredentialLeakDetected(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventCredentialLeakDetected)
}

func (s *PostgresStore) RecordCrossOwnerRejected(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventCrossOwnerRejected)
}

func (s *PostgresStore) RecordFalseRemovalInvariant(ctx context.Context) error {
	return s.recordOperationalEvent(ctx, operationalEventFalseRemovalInvariant)
}

func (s *PostgresStore) recordOperationalEvent(ctx context.Context, kind operationalEventKind) error {
	if !validOperationalEventKind(kind) {
		return ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO operational_event_counters (kind,total,updated_at)
		VALUES ($1,1,statement_timestamp())
		ON CONFLICT (kind) DO UPDATE SET total=operational_event_counters.total+1,updated_at=statement_timestamp()`, string(kind))
	return err
}

func recordOperationalEventTx(ctx context.Context, tx pgx.Tx, kind operationalEventKind) error {
	if !validOperationalEventKind(kind) {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, `INSERT INTO operational_event_counters (kind,total,updated_at)
		VALUES ($1,1,statement_timestamp())
		ON CONFLICT (kind) DO UPDATE SET total=operational_event_counters.total+1,updated_at=statement_timestamp()`, string(kind))
	return err
}

func recordOperationalMetricTx(ctx context.Context, tx pgx.Tx, metric, label string, delta int64) error {
	if delta < 0 || !validOperationalMetric(metric, label) {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, `INSERT INTO operational_metric_counters (metric,label,total,updated_at)
		VALUES ($1,$2,$3,statement_timestamp())
		ON CONFLICT (metric,label) DO UPDATE SET
			total=operational_metric_counters.total+EXCLUDED.total,updated_at=statement_timestamp()`, metric, label, delta)
	return err
}

func recordCollectionDurationTx(ctx context.Context, tx pgx.Tx, result string, startedAt time.Time, completedAt *time.Time) error {
	if completedAt == nil || completedAt.Before(startedAt) || !validOperationalMetric("collection_runs", result) {
		return nil
	}
	duration := completedAt.Sub(startedAt).Seconds()
	_, err := tx.Exec(ctx, `INSERT INTO operational_collection_duration_counters
		(result,count,sum_seconds,le_0_1,le_0_5,le_1,le_2,le_5,le_10,le_30,le_60,le_300,updated_at)
		VALUES ($1,1,$2::double precision,($2::double precision<=0.1::double precision)::int,
		        ($2::double precision<=0.5::double precision)::int,($2::double precision<=1::double precision)::int,
		        ($2::double precision<=2::double precision)::int,($2::double precision<=5::double precision)::int,
		        ($2::double precision<=10::double precision)::int,($2::double precision<=30::double precision)::int,
		        ($2::double precision<=60::double precision)::int,($2::double precision<=300::double precision)::int,
		        statement_timestamp())
		ON CONFLICT (result) DO UPDATE SET
			count=operational_collection_duration_counters.count+1,
			sum_seconds=operational_collection_duration_counters.sum_seconds+EXCLUDED.sum_seconds,
			le_0_1=operational_collection_duration_counters.le_0_1+EXCLUDED.le_0_1,
			le_0_5=operational_collection_duration_counters.le_0_5+EXCLUDED.le_0_5,
			le_1=operational_collection_duration_counters.le_1+EXCLUDED.le_1,
			le_2=operational_collection_duration_counters.le_2+EXCLUDED.le_2,
			le_5=operational_collection_duration_counters.le_5+EXCLUDED.le_5,
			le_10=operational_collection_duration_counters.le_10+EXCLUDED.le_10,
			le_30=operational_collection_duration_counters.le_30+EXCLUDED.le_30,
			le_60=operational_collection_duration_counters.le_60+EXCLUDED.le_60,
			le_300=operational_collection_duration_counters.le_300+EXCLUDED.le_300,
			updated_at=statement_timestamp()`, result, duration)
	return err
}

func validOperationalEventKind(kind operationalEventKind) bool {
	if strings.TrimSpace(string(kind)) != string(kind) {
		return false
	}
	switch kind {
	case operationalEventCredentialLeakDetected, operationalEventCrossOwnerRejected, operationalEventFalseRemovalInvariant:
		return true
	default:
		return false
	}
}

func (s *MemoryStore) recordOperationalMetricLocked(metric, label string, delta int64) {
	if delta < 0 || !validOperationalMetric(metric, label) {
		return
	}
	if s.operationalMetricCounters == nil {
		s.operationalMetricCounters = make(map[operationalMetricKey]int64)
	}
	s.operationalMetricCounters[operationalMetricKey{Metric: metric, Label: label}] += delta
}

func (s *MemoryStore) recordCollectionDurationLocked(result string, startedAt time.Time, completedAt *time.Time) {
	if completedAt == nil || completedAt.Before(startedAt) || !validOperationalMetric("collection_runs", result) {
		return
	}
	if s.operationalCollectionDurations == nil {
		s.operationalCollectionDurations = make(map[string]operationalmetrics.DurationSummary)
	}
	values := s.operationalCollectionDurations
	if _, ok := values[result]; !ok {
		values[result] = newOperationalDurationSummary()
	}
	addOperationalDuration(values, result, completedAt.Sub(startedAt).Seconds())
}

func validOperationalMetric(metric, label string) bool {
	switch metric {
	case "collection_runs", "collection_facts":
		return label == "succeeded" || label == "partial" || label == "failed"
	case "collection_coverage":
		return label == "complete" || label == "partial" || label == "unavailable"
	case "ingest_facts":
		return label == "accepted" || label == "duplicate"
	case "change_events":
		switch label {
		case "balance_low", "balance_recovered", "group_added", "group_changed", "group_removed",
			"model_added", "price_increased", "price_decreased", "model_removed", "source_stale", "source_recovered":
			return true
		}
	case "reconcile_runs":
		return label == "dry_run" || label == "apply" || label == "rollback"
	case "experiments":
		return label == "shadow" || label == "dry_run"
	}
	return false
}

var (
	_ OperationalEventRecorder = (*MemoryStore)(nil)
	_ OperationalEventRecorder = (*PostgresStore)(nil)
)
