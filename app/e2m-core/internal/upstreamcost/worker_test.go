package upstreamcost

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type workerStoreStub struct {
	job           store.UpstreamCostAttributionJob
	claimed       bool
	claimErr      error
	evidenceErr   error
	completeErr   error
	completed     [][]contracts.UpstreamCostFact
	retried       []workerRetry
	loadCallCount int
}

type workerRetry struct {
	job       store.UpstreamCostAttributionJob
	errorCode string
	delay     time.Duration
}

func (stub *workerStoreStub) ClaimUpstreamCostAttributionJob(context.Context, string, time.Duration) (store.UpstreamCostAttributionJob, bool, error) {
	if stub.claimErr != nil || !stub.claimed {
		return store.UpstreamCostAttributionJob{}, false, stub.claimErr
	}
	stub.claimed = false
	return stub.job, true, nil
}

func (stub *workerStoreStub) LoadUpstreamCostAttributionEvidence(context.Context, store.UpstreamCostAttributionJob) ([]contracts.UpstreamIntelligenceLink, []contracts.UpstreamOfferObservation, error) {
	stub.loadCallCount++
	return nil, nil, stub.evidenceErr
}

func (stub *workerStoreStub) CompleteUpstreamCostAttributionJob(_ context.Context, _ store.UpstreamCostAttributionJob, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	stub.completed = append(stub.completed, append([]contracts.UpstreamCostFact(nil), facts...))
	if stub.completeErr != nil {
		return nil, contracts.UpstreamCostFactVersion{}, stub.completeErr
	}
	return facts, contracts.UpstreamCostFactVersion{UserID: stub.job.UserID, FactVersion: 1}, nil
}

func (stub *workerStoreStub) RetryUpstreamCostAttributionJob(_ context.Context, job store.UpstreamCostAttributionJob, errorCode string, delay time.Duration) (store.UpstreamCostAttributionJob, error) {
	stub.retried = append(stub.retried, workerRetry{job: job, errorCode: errorCode, delay: delay})
	return job, nil
}

func TestWorkerProcessOneCompletesFourFactsAndDoesNotReplay(t *testing.T) {
	stub := &workerStoreStub{job: workerTestJob(), claimed: true}
	worker := NewWorker(stub, time.Second)
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed || stub.loadCallCount != 1 || len(stub.completed) != 1 || len(stub.completed[0]) != 4 || len(stub.retried) != 0 {
		t.Fatalf("processed=%v err=%v load=%d completed=%d retried=%d", processed, err, stub.loadCallCount, len(stub.completed), len(stub.retried))
	}
	processed, err = worker.ProcessOne(context.Background())
	if err != nil || processed || len(stub.completed) != 1 {
		t.Fatalf("replay processed=%v err=%v completions=%d", processed, err, len(stub.completed))
	}
}

func TestWorkerProcessOneRetriesEvidenceAndLedgerFailures(t *testing.T) {
	tests := []struct {
		name                     string
		evidenceErr, completeErr error
		wantCode                 string
		wantComplete             int
	}{
		{name: "evidence", evidenceErr: errors.New("read failed"), wantCode: "evidence_read_failed"},
		{name: "ledger", completeErr: errors.New("write failed"), wantCode: "ledger_write_failed", wantComplete: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := workerTestJob()
			job.Attempts = 2
			stub := &workerStoreStub{job: job, claimed: true, evidenceErr: tt.evidenceErr, completeErr: tt.completeErr}
			processed, err := NewWorker(stub, time.Second).ProcessOne(context.Background())
			if err != nil || !processed || len(stub.completed) != tt.wantComplete || len(stub.retried) != 1 {
				t.Fatalf("processed=%v err=%v completed=%d retried=%d", processed, err, len(stub.completed), len(stub.retried))
			}
			retry := stub.retried[0]
			if retry.errorCode != tt.wantCode || retry.delay != 5*time.Second || retry.job.LeaseVersion != job.LeaseVersion {
				t.Fatalf("retry=%+v", retry)
			}
		})
	}
}

func TestWorkerProcessOneSurfacesClaimFailure(t *testing.T) {
	want := errors.New("claim failed")
	stub := &workerStoreStub{claimErr: want}
	processed, err := NewWorker(stub, time.Second).ProcessOne(context.Background())
	if processed || !errors.Is(err, want) || stub.loadCallCount != 0 || len(stub.completed) != 0 || len(stub.retried) != 0 {
		t.Fatalf("processed=%v err=%v load=%d completed=%d retried=%d", processed, err, stub.loadCallCount, len(stub.completed), len(stub.retried))
	}
}

func workerTestJob() store.UpstreamCostAttributionJob {
	input, requests := int64(100), int64(1)
	leaseUntil := time.Now().UTC().Add(time.Minute)
	return store.UpstreamCostAttributionJob{
		UsageObservationID: "core-usage-1", UserID: 42, ChannelID: "channel-1", InstanceID: "instance-1",
		ModelKey: "gpt-test", GroupKey: "paid", InputTokens: &input, RequestCount: &requests,
		OccurredAt: time.Date(2026, 7, 26, 2, 3, 4, 0, time.UTC), CalculationVersion: contracts.UpstreamCostCalculationVersionV1,
		Status: store.UpstreamCostJobProcessing, Attempts: 1, LeaseOwner: "worker-test", LeaseUntil: &leaseUntil, LeaseVersion: 1,
	}
}
