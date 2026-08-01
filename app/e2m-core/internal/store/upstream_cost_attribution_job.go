package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"e2m.local/contracts"
)

type UpstreamCostAttributionJobStatus string

const (
	UpstreamCostJobPending    UpstreamCostAttributionJobStatus = "pending"
	UpstreamCostJobProcessing UpstreamCostAttributionJobStatus = "processing"
	UpstreamCostJobRetrying   UpstreamCostAttributionJobStatus = "retrying"
	UpstreamCostJobSucceeded  UpstreamCostAttributionJobStatus = "succeeded"
)

// UpstreamCostAttributionJob is a sanitized, durable copy of presence-aware
// usage. It contains no Connector location, credential or raw response data.
type UpstreamCostAttributionJob struct {
	UsageObservationID string
	UserID             int64
	ChannelID          string
	InstanceID         string
	ModelKey           string
	GroupKey           string
	InputTokens        *int64
	OutputTokens       *int64
	CachedInputTokens  *int64
	RequestCount       *int64
	OccurredAt         time.Time
	CalculationVersion string
	Status             UpstreamCostAttributionJobStatus
	Attempts           int64
	NextAttemptAt      time.Time
	LastErrorCode      string
	LeaseOwner         string
	LeaseUntil         *time.Time
	LeaseVersion       int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type UpstreamCostObservationStore interface {
	AppendChannelObservationWithCostJob(context.Context, contracts.ChannelObservation, *UpstreamCostAttributionJob) (contracts.ChannelObservation, error)
}

type UpstreamCostAttributionJobStore interface {
	ClaimUpstreamCostAttributionJob(context.Context, string, time.Duration) (UpstreamCostAttributionJob, bool, error)
	LoadUpstreamCostAttributionEvidence(context.Context, UpstreamCostAttributionJob) ([]contracts.UpstreamIntelligenceLink, []contracts.UpstreamOfferObservation, error)
	CompleteUpstreamCostAttributionJob(context.Context, UpstreamCostAttributionJob, []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error)
	RetryUpstreamCostAttributionJob(context.Context, UpstreamCostAttributionJob, string, time.Duration) (UpstreamCostAttributionJob, error)
}

func AsUpstreamCostObservationStore(value any) (UpstreamCostObservationStore, bool) {
	st, ok := value.(UpstreamCostObservationStore)
	return st, ok
}

func AsUpstreamCostAttributionJobStore(value any) (UpstreamCostAttributionJobStore, bool) {
	st, ok := value.(UpstreamCostAttributionJobStore)
	return st, ok
}

func normalizeUpstreamCostJob(input UpstreamCostAttributionJob) (UpstreamCostAttributionJob, error) {
	job := input
	job.UsageObservationID = strings.TrimSpace(job.UsageObservationID)
	job.ChannelID = strings.TrimSpace(job.ChannelID)
	job.InstanceID = strings.TrimSpace(job.InstanceID)
	job.ModelKey = strings.TrimSpace(job.ModelKey)
	job.GroupKey = strings.TrimSpace(job.GroupKey)
	job.CalculationVersion = strings.TrimSpace(job.CalculationVersion)
	job.OccurredAt = normalizeUpstreamTime(job.OccurredAt)
	job.InputTokens = cloneInt64(job.InputTokens)
	job.OutputTokens = cloneInt64(job.OutputTokens)
	job.CachedInputTokens = cloneInt64(job.CachedInputTokens)
	job.RequestCount = cloneInt64(job.RequestCount)
	if job.UserID <= 0 || job.UsageObservationID == "" || job.ChannelID == "" ||
		job.InstanceID == "" || job.ModelKey == "" || job.OccurredAt.IsZero() ||
		job.CalculationVersion == "" || len(job.GroupKey) > 128 ||
		job.InputTokens != nil && *job.InputTokens < 0 ||
		job.OutputTokens != nil && *job.OutputTokens < 0 ||
		job.CachedInputTokens != nil && *job.CachedInputTokens < 0 ||
		job.RequestCount != nil && *job.RequestCount < 0 {
		return UpstreamCostAttributionJob{}, ErrInvalid
	}
	if job.Status == "" {
		job.Status = UpstreamCostJobPending
	}
	return job, nil
}

func cloneUpstreamCostJob(job UpstreamCostAttributionJob) UpstreamCostAttributionJob {
	job.InputTokens = cloneInt64(job.InputTokens)
	job.OutputTokens = cloneInt64(job.OutputTokens)
	job.CachedInputTokens = cloneInt64(job.CachedInputTokens)
	job.RequestCount = cloneInt64(job.RequestCount)
	job.LeaseUntil = normalizeUpstreamTimePtr(job.LeaseUntil)
	job.CompletedAt = normalizeUpstreamTimePtr(job.CompletedAt)
	return job
}

func sameUpstreamCostJobPayload(left, right UpstreamCostAttributionJob) bool {
	left.Status, right.Status = "", ""
	left.Attempts, right.Attempts = 0, 0
	left.NextAttemptAt, right.NextAttemptAt = time.Time{}, time.Time{}
	left.LastErrorCode, right.LastErrorCode = "", ""
	left.LeaseOwner, right.LeaseOwner = "", ""
	left.LeaseUntil, right.LeaseUntil = nil, nil
	left.LeaseVersion, right.LeaseVersion = 0, 0
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	left.CompletedAt, right.CompletedAt = nil, nil
	return reflect.DeepEqual(left, right)
}

func retryableUpstreamCostJobErrorCode(code string) bool {
	switch code {
	case "evidence_read_failed", "ledger_write_failed":
		return true
	default:
		return false
	}
}

func upstreamCostFactsMatchJob(facts []contracts.UpstreamCostFact, job UpstreamCostAttributionJob) bool {
	for _, fact := range facts {
		if fact.UserID != job.UserID || fact.UsageObservationID != job.UsageObservationID ||
			fact.ChannelID != job.ChannelID || fact.InstanceID != job.InstanceID ||
			fact.ModelKey != job.ModelKey || fact.GroupKey != job.GroupKey ||
			fact.CalculationVersion != job.CalculationVersion ||
			!fact.OccurredAt.Equal(job.OccurredAt) {
			return false
		}
	}
	return len(facts) == upstreamCostDimensionsPerUsage
}

func validateClaimedUpstreamCostJob(job UpstreamCostAttributionJob, workerID string, now time.Time) error {
	if job.Status != UpstreamCostJobProcessing || job.LeaseOwner != workerID ||
		job.LeaseVersion <= 0 || job.LeaseUntil == nil || !job.LeaseUntil.After(now) {
		return ErrConflict
	}
	return nil
}

func mapCostObservationAppendError(err error) error {
	if errors.Is(err, ErrConflict) {
		return ErrConflict
	}
	return err
}
