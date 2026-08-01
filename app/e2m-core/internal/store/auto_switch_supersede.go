package store

import (
	"fmt"
	"time"

	"e2m.local/contracts"
)

func autoSwitchSupersededReason(generation int64) string {
	return fmt.Sprintf("superseded by route-plan scheduling generation %d", generation)
}

func supersedeAutoSwitchDecision(decision *contracts.AutoSwitchDecision, generation int64, now time.Time) {
	reason := autoSwitchSupersededReason(generation)
	decision.Status = contracts.AutoSwitchFailed
	decision.Error = reason
	decision.ObservationNote = reason
	decision.LeaseUntil = nil
	decision.ResolvedAt = copyTimePointer(&now)
	decision.UpdatedAt = now
}
