package contracts

import "time"

// QualityCircuitState is the persisted scheduling state of one route-plan
// channel binding. The scope is deliberately local to a downstream plan: an
// upstream quality regression must not become a provider-global off switch.
type QualityCircuitState string

const (
	QualityCircuitClosed   QualityCircuitState = "closed"
	QualityCircuitOpen     QualityCircuitState = "open"
	QualityCircuitHalfOpen QualityCircuitState = "half_open"
)

// Valid reports whether the state can be persisted.
func (s QualityCircuitState) Valid() bool {
	switch s {
	case QualityCircuitClosed, QualityCircuitOpen, QualityCircuitHalfOpen:
		return true
	default:
		return false
	}
}

// QualityCircuitReason is the dependency-neutral persisted counterpart of a
// strategy decision reason. Core maps its own reason type to this value rather
// than making the shared contracts package depend on Core.
type QualityCircuitReason struct {
	Code string `json:"code,omitempty"`
	Text string `json:"text,omitempty"`
}

// QualityCircuitRuntime is the durable state machine for one (plan, channel)
// scheduling scope. Open and half-open bindings remain outside normal traffic;
// ProbeAfter only makes an active recovery probe eligible and never restores
// traffic by itself.
//
// Version starts at 1 and is advanced by every successful store transition.
// Callers use it as an optimistic-concurrency token so parallel schedulers
// cannot both advance the same circuit and repeat gateway side effects.
type QualityCircuitRuntime struct {
	PlanID    string              `json:"plan_id"`
	ChannelID string              `json:"channel_id"`
	State     QualityCircuitState `json:"state"`

	OpenedAt         *time.Time `json:"opened_at,omitempty"`
	ProbeAfter       *time.Time `json:"probe_after,omitempty"`
	HalfOpenSince    *time.Time `json:"half_open_since,omitempty"`
	LastProbeAt      *time.Time `json:"last_probe_at,omitempty"`
	LastTransitionAt *time.Time `json:"last_transition_at,omitempty"`

	OpenCount                 int                  `json:"open_count"`
	ConsecutiveProbeSuccesses int                  `json:"consecutive_probe_successes"`
	LastScore                 float64              `json:"last_score"`
	LastReason                QualityCircuitReason `json:"last_reason"`
	// RestorePending is set before enabling a recovered binding and cleared only
	// after the gateway-side enable has succeeded. It closes the crash window
	// between the remote side effect and the final closed transition.
	RestorePending bool `json:"restore_pending,omitempty"`
	// RecoveryReady means the active-probe threshold has been met and this
	// binding is participating in a source-wide guarded traffic rollout. A ready
	// binding may still be half-open and disabled until its stable cohort is
	// admitted. Closed+ready means it is currently carrying canary traffic.
	RecoveryReady bool `json:"recovery_ready,omitempty"`
	// RecoveryStage is the target share of ready downstream bindings admitted
	// for this source incident. Supported stages are 10, 25, 50 and 100.
	RecoveryStage int `json:"recovery_stage,omitempty"`
	// RecoveryStageStartedAt and RecoveryObserveAfter make the rollout durable
	// across scheduler restarts. A stage cannot expand before fresh passive
	// evidence from its admitted cohort has completed the observation window.
	RecoveryStageStartedAt *time.Time `json:"recovery_stage_started_at,omitempty"`
	RecoveryObserveAfter   *time.Time `json:"recovery_observe_after,omitempty"`

	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// QualityCircuitRuntimeFilter narrows runtime scans. ProbeDueBefore keeps only
// rows whose probe_after is set and no later than the supplied time. A recovery
// worker normally combines it with States: [open]. Results are ordered by the
// next probe time, then by stable scope identity.
type QualityCircuitRuntimeFilter struct {
	PlanID         string
	ChannelID      string
	States         []QualityCircuitState
	ProbeDueBefore time.Time
	Limit          int
}

// Matches reports whether a runtime satisfies the filter.
func (f QualityCircuitRuntimeFilter) Matches(rt QualityCircuitRuntime) bool {
	if f.PlanID != "" && rt.PlanID != f.PlanID {
		return false
	}
	if f.ChannelID != "" && rt.ChannelID != f.ChannelID {
		return false
	}
	if len(f.States) > 0 {
		matched := false
		for _, state := range f.States {
			if rt.State == state {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if !f.ProbeDueBefore.IsZero() && (rt.ProbeAfter == nil || rt.ProbeAfter.After(f.ProbeDueBefore)) {
		return false
	}
	return true
}
