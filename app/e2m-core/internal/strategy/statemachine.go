package strategy

import (
	"time"

	"e2m.local/contracts"
)

// This file holds the per-channel runtime state machine and the instance-level
// anti-flapping primitives. The metrics aggregator only ever emits the pure data
// verdicts (unknown/healthy/degraded/unhealthy); the lifecycle states
// (quarantined/recovering) and every transition rule live here, owned by the
// switch layer. The doc's governing principle: switch out fast, switch back slow.

// StateThresholds tunes cross-window escalation and recovery. Durations are real
// wall-clock time so the same rules work under any snapshot cadence.
type StateThresholds struct {
	// DegradedWindowsToUnhealthy is how many consecutive non-healthy data verdicts
	// escalate a degraded channel to unhealthy (sustained, not a single blip).
	DegradedWindowsToUnhealthy int
	// HealthyWindowsToRecover is how many consecutive healthy verdicts a recovering
	// channel needs before it returns to healthy.
	HealthyWindowsToRecover int
	// RecoveryObservation is the minimum time a channel must sit in recovering
	// before it can be promoted to healthy (the switch-back-slow guard).
	RecoveryObservation time.Duration
}

// DefaultStateThresholds returns the first-version tuning.
func DefaultStateThresholds() StateThresholds {
	return StateThresholds{
		DegradedWindowsToUnhealthy: 2,
		HealthyWindowsToRecover:    2,
		RecoveryObservation:        15 * time.Minute,
	}
}

func (t StateThresholds) withDefaults() StateThresholds {
	d := DefaultStateThresholds()
	if t.DegradedWindowsToUnhealthy <= 0 {
		t.DegradedWindowsToUnhealthy = d.DegradedWindowsToUnhealthy
	}
	if t.HealthyWindowsToRecover <= 0 {
		t.HealthyWindowsToRecover = d.HealthyWindowsToRecover
	}
	if t.RecoveryObservation <= 0 {
		t.RecoveryObservation = d.RecoveryObservation
	}
	return t
}

// ChannelRuntime is the persisted-ish runtime state of one channel. The caller
// owns storage; this package only advances it deterministically. Counters track
// consecutive runs of a verdict so a single noisy window neither escalates nor
// recovers a channel on its own.
type ChannelRuntime struct {
	ChannelID string
	State     contracts.HealthState

	// Counters of consecutive verdicts, reset when the run breaks.
	consecutiveUnhealthy int
	consecutiveDegraded  int
	consecutiveHealthy   int

	// QuarantinedAt/RecoveringSince timestamp the lifecycle transitions so the
	// recovery-observation window can be enforced.
	QuarantinedAt   time.Time
	RecoveringSince time.Time
	LastTransition  time.Time
}

// Transition advances a channel's runtime state from a fresh data verdict
// (snapshot.HealthState) at time now. It encodes the doc's state machine:
//
//	healthy   -> degraded    on a degraded/unhealthy verdict
//	degraded  -> unhealthy   after DegradedWindowsToUnhealthy sustained bad windows,
//	                         or immediately on an unhealthy verdict
//	*         -> quarantined only via Quarantine (an orchestrator drain action)
//	quarantined -> recovering only via BeginRecovery (a probe/observation restart)
//	recovering -> healthy    after HealthyWindowsToRecover healthy windows AND the
//	                         recovery-observation time has elapsed
//	recovering -> unhealthy  if it fails again during observation (back to square one)
//
// A HealthUnknown verdict (too few samples) never changes lifecycle state: an
// idle channel holds its state rather than being punished or healed by silence.
func (r *ChannelRuntime) Transition(verdict contracts.HealthState, now time.Time, th StateThresholds) contracts.HealthState {
	th = th.withDefaults()
	if r.State == "" {
		r.State = contracts.HealthUnknown
	}

	// Quarantine is a hard, externally-set state; only BeginRecovery leaves it.
	// Ignore data verdicts while quarantined so a drained channel is not silently
	// re-promoted by passive traffic before it has been observed.
	if r.State == contracts.HealthQuarantined {
		return r.State
	}

	if verdict == contracts.HealthUnknown {
		return r.State // hold state on insufficient data
	}

	r.updateCounters(verdict)

	switch r.State {
	case contracts.HealthRecovering:
		// Switch back slow: require sustained healthy verdicts AND elapsed
		// observation time; any non-healthy verdict drops it straight back.
		switch verdict {
		case contracts.HealthHealthy:
			if r.consecutiveHealthy >= th.HealthyWindowsToRecover &&
				(r.RecoveringSince.IsZero() || now.Sub(r.RecoveringSince) >= th.RecoveryObservation) {
				r.set(contracts.HealthHealthy, now)
			}
		default:
			r.set(contracts.HealthUnhealthy, now)
		}
	case contracts.HealthUnhealthy:
		if verdict == contracts.HealthHealthy {
			// Do not jump unhealthy->healthy directly; go through recovering so the
			// observation window applies.
			r.set(contracts.HealthRecovering, now)
			r.RecoveringSince = now
		}
	default: // unknown / healthy / degraded
		switch verdict {
		case contracts.HealthHealthy:
			r.set(contracts.HealthHealthy, now)
		case contracts.HealthDegraded:
			if r.State == contracts.HealthDegraded && r.consecutiveDegraded >= th.DegradedWindowsToUnhealthy {
				r.set(contracts.HealthUnhealthy, now)
			} else {
				r.set(contracts.HealthDegraded, now)
			}
		case contracts.HealthUnhealthy:
			r.set(contracts.HealthUnhealthy, now)
		}
	}
	return r.State
}

// Quarantine forces a channel out of scheduling (orchestrator drain). This is the
// only path into quarantined; it is invoked when auto-switch removes a channel.
func (r *ChannelRuntime) Quarantine(now time.Time) {
	r.set(contracts.HealthQuarantined, now)
	r.QuarantinedAt = now
	r.RecoveringSince = time.Time{}
	r.resetCounters()
}

// BeginRecovery moves a quarantined channel into recovering and starts its
// observation clock. This is the only path out of quarantined.
func (r *ChannelRuntime) BeginRecovery(now time.Time) {
	if r.State != contracts.HealthQuarantined {
		return
	}
	r.set(contracts.HealthRecovering, now)
	r.RecoveringSince = now
	r.resetCounters()
}

func (r *ChannelRuntime) updateCounters(verdict contracts.HealthState) {
	switch verdict {
	case contracts.HealthHealthy:
		r.consecutiveHealthy++
		r.consecutiveDegraded = 0
		r.consecutiveUnhealthy = 0
	case contracts.HealthDegraded:
		r.consecutiveDegraded++
		r.consecutiveHealthy = 0
		r.consecutiveUnhealthy = 0
	case contracts.HealthUnhealthy:
		r.consecutiveUnhealthy++
		r.consecutiveDegraded++ // unhealthy also counts as a sustained bad window
		r.consecutiveHealthy = 0
	}
}

func (r *ChannelRuntime) resetCounters() {
	r.consecutiveHealthy = 0
	r.consecutiveDegraded = 0
	r.consecutiveUnhealthy = 0
}

func (r *ChannelRuntime) set(state contracts.HealthState, now time.Time) {
	if r.State != state {
		r.LastTransition = now
	}
	r.State = state
}

// --- instance-level dampening (anti-flapping) ---

// DampeningDecision reports whether an automatic switch is allowed right now, and
// if not, an explainable reason. It is a pure guard the orchestrator consults
// before generating a decision; it never mutates history itself.
type DampeningDecision struct {
	Allowed bool
	Reason  Reason
}

// Dampening reason codes.
const (
	DampenCooldown = "dampen_cooldown"
	DampenMaxRate  = "dampen_max_per_hour"
	DampenOK       = "dampen_ok"
)

// AllowSwitch enforces the two instance-level guards from the doc: a minimum
// interval between switches (cooldown) and a per-hour ceiling. lastSwitch is the
// most recent switch time on this instance (zero if none); recentSwitches are the
// switch timestamps within the trailing hour. now is the decision time.
func AllowSwitch(s contracts.RouteStrategy, lastSwitch time.Time, recentSwitches []time.Time, now time.Time) DampeningDecision {
	if s.CooldownSeconds > 0 && !lastSwitch.IsZero() {
		cd := time.Duration(s.CooldownSeconds) * time.Second
		if elapsed := now.Sub(lastSwitch); elapsed < cd {
			return DampeningDecision{Reason: Reason{
				Code: DampenCooldown,
				Text: "距上次切换未满最小间隔，处于冷却期，暂不自动切换",
			}}
		}
	}
	if s.MaxAutoSwitchesPerHour > 0 {
		n := 0
		hourAgo := now.Add(-time.Hour)
		for _, t := range recentSwitches {
			if t.After(hourAgo) {
				n++
			}
		}
		if n >= s.MaxAutoSwitchesPerHour {
			return DampeningDecision{Reason: Reason{
				Code: DampenMaxRate,
				Text: "本小时自动切换次数已达上限，暂停自动切换以避免抖动",
			}}
		}
	}
	return DampeningDecision{Allowed: true, Reason: Reason{Code: DampenOK, Text: "未触发防抖限制，允许自动切换"}}
}
