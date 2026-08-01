package store

import (
	"strings"
	"time"

	"e2m.local/contracts"
)

const maxOnboardingErrorCodeLength = 128
const maxOnboardingFingerprintLength = 128

func validOnboardingWorkflow(input contracts.OnboardingWorkflow) bool {
	if input.UserID <= 0 || strings.TrimSpace(input.InstanceID) == "" ||
		strings.TrimSpace(input.PoolID) == "" || !input.Stage.Valid() ||
		!input.Status.Valid() || input.Attempts < 0 ||
		len(input.LastErrorCode) > maxOnboardingErrorCodeLength ||
		len(input.DesiredFingerprint) > maxOnboardingFingerprintLength ||
		input.DesiredGeneration <= 0 || input.LastReadyGeneration < 0 ||
		input.LastReadyGeneration > input.DesiredGeneration {
		return false
	}
	if input.Status == contracts.OnboardingReady && input.Stage != contracts.OnboardingActive {
		return false
	}
	if input.Status == contracts.OnboardingReady && input.NextAttemptAt == nil {
		return false
	}
	if input.Status == contracts.OnboardingRetryable {
		if input.Stage != contracts.OnboardingFailedRetryable || input.NextAttemptAt == nil {
			return false
		}
	}
	if input.Status == contracts.OnboardingDormantStatus {
		// A terminal transition is submitted under the current live lease; the
		// store clears that lease atomically when it persists the dormant row.
		if input.Stage != contracts.OnboardingDormant || input.NextAttemptAt != nil {
			return false
		}
	}
	for channelID, version := range input.KeyVersionSummary {
		if strings.TrimSpace(channelID) == "" || version < 0 {
			return false
		}
	}
	return true
}

func normalizeNewOnboardingWorkflow(input contracts.OnboardingWorkflow) contracts.OnboardingWorkflow {
	if input.Stage == "" {
		input.Stage = contracts.OnboardingWaitingConnector
	}
	if input.Status == "" {
		input.Status = contracts.OnboardingPending
	}
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.PoolID = strings.TrimSpace(input.PoolID)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	input.LastErrorCode = strings.TrimSpace(input.LastErrorCode)
	input.DesiredFingerprint = strings.TrimSpace(input.DesiredFingerprint)
	if input.DesiredGeneration <= 0 {
		input.DesiredGeneration = 1
	}
	if input.LastReadyGeneration < 0 || input.LastReadyGeneration > input.DesiredGeneration {
		input.LastReadyGeneration = 0
		input.LastReadyAt = nil
	}
	return input
}

func copyOnboardingWorkflow(input contracts.OnboardingWorkflow) contracts.OnboardingWorkflow {
	out := input
	if input.NextAttemptAt != nil {
		t := *input.NextAttemptAt
		out.NextAttemptAt = &t
	}
	if input.LeaseUntil != nil {
		t := *input.LeaseUntil
		out.LeaseUntil = &t
	}
	if input.LastReadyAt != nil {
		t := *input.LastReadyAt
		out.LastReadyAt = &t
	}
	if input.KeyVersionSummary != nil {
		out.KeyVersionSummary = make(map[string]int64, len(input.KeyVersionSummary))
		for channelID, version := range input.KeyVersionSummary {
			out.KeyVersionSummary[channelID] = version
		}
	}
	return out
}

func onboardingClaimDue(input contracts.OnboardingWorkflow, now time.Time) bool {
	if strings.TrimSpace(input.ConnectorID) == "" {
		return false
	}
	switch input.Status {
	case contracts.OnboardingPending, contracts.OnboardingRetryable:
		return input.NextAttemptAt == nil || !input.NextAttemptAt.After(now)
	case contracts.OnboardingReady:
		return input.NextAttemptAt != nil && !input.NextAttemptAt.After(now)
	case contracts.OnboardingRunning:
		return input.LeaseUntil == nil || !input.LeaseUntil.After(now)
	default:
		return false
	}
}
