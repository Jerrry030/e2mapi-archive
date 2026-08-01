package strategy

import (
	"hash/fnv"
	"math"
	"time"
)

// QualityCircuitState is the scheduling lifecycle of one downstream/source
// binding. It must be stored at that scope, never as one provider-global flag.
type QualityCircuitState string

const (
	CircuitClosed   QualityCircuitState = "closed"
	CircuitOpen     QualityCircuitState = "open"
	CircuitHalfOpen QualityCircuitState = "half_open"
)

// QualityCircuitAction tells the caller which idempotent scheduling side effect
// follows a state transition. Open and half-open both remain out of normal
// traffic; only closed is schedulable.
type QualityCircuitAction string

const (
	CircuitNoop        QualityCircuitAction = "none"
	CircuitEject       QualityCircuitAction = "eject"
	CircuitKeepEjected QualityCircuitAction = "keep_ejected"
	CircuitRestore     QualityCircuitAction = "restore"
)

// QualityCircuitPolicy controls cooldown and guarded recovery. A source never
// returns merely because time elapsed: cooldown only permits active probes, and
// multiple strong probes are required before scheduling is restored.
type QualityCircuitPolicy struct {
	BaseCooldown           time.Duration
	MaxCooldown            time.Duration
	HalfOpenSuccesses      int
	RecoveryScore          float64
	CooldownJitterFraction float64
}

func DefaultQualityCircuitPolicy() QualityCircuitPolicy {
	return QualityCircuitPolicy{
		BaseCooldown:           5 * time.Minute,
		MaxCooldown:            time.Hour,
		HalfOpenSuccesses:      3,
		RecoveryScore:          85,
		CooldownJitterFraction: 0.20,
	}
}

func (p QualityCircuitPolicy) withDefaults() QualityCircuitPolicy {
	d := DefaultQualityCircuitPolicy()
	if p.BaseCooldown <= 0 {
		p.BaseCooldown = d.BaseCooldown
	}
	if p.MaxCooldown <= 0 {
		p.MaxCooldown = d.MaxCooldown
	}
	if p.MaxCooldown < p.BaseCooldown {
		p.MaxCooldown = p.BaseCooldown
	}
	if p.HalfOpenSuccesses <= 0 {
		p.HalfOpenSuccesses = d.HalfOpenSuccesses
	}
	if p.RecoveryScore <= 0 || p.RecoveryScore > 100 {
		p.RecoveryScore = d.RecoveryScore
	}
	if p.CooldownJitterFraction < 0 || p.CooldownJitterFraction > 1 {
		p.CooldownJitterFraction = d.CooldownJitterFraction
	}
	return p
}

// QualityCircuit is persistable pure state. ScopeKey should identify a concrete
// downstream binding (for example instance+source), both to prevent global
// collateral ejection and to deterministically stagger recovery probes.
type QualityCircuit struct {
	ScopeKey string
	State    QualityCircuitState

	OpenedAt       time.Time
	ProbeAfter     time.Time
	HalfOpenSince  time.Time
	LastTransition time.Time

	OpenCount                 int
	ConsecutiveProbeSuccesses int
	LastScore                 float64
	LastReason                Reason
}

// CircuitEventKind separates passive scheduling evidence from a deliberate
// recovery probe. An open circuit ignores passive windows because traffic has
// already been removed; only a due recovery probe can advance it.
type CircuitEventKind string

const (
	CircuitQualityWindow CircuitEventKind = "quality_window"
	CircuitRecoveryProbe CircuitEventKind = "recovery_probe"
)

type QualityCircuitEvent struct {
	Kind       CircuitEventKind
	Now        time.Time
	Evaluation PenaltyEvaluation
}

type QualityCircuitTransition struct {
	Circuit QualityCircuit
	Action  QualityCircuitAction
	Changed bool
	Reason  Reason
}

const (
	CircuitReasonHealthy        = "circuit_healthy"
	CircuitReasonOpened         = "circuit_opened"
	CircuitReasonCooldown       = "circuit_cooldown"
	CircuitReasonProbeRequired  = "circuit_probe_required"
	CircuitReasonHalfOpen       = "circuit_half_open"
	CircuitReasonProbeFailed    = "circuit_probe_failed"
	CircuitReasonProbeSucceeded = "circuit_probe_succeeded"
	CircuitReasonRestored       = "circuit_restored"
)

// AdvanceQualityCircuit deterministically advances a downstream-scoped circuit.
// The returned value is a copy, so callers can persist it transactionally with
// the route command rather than mutating shared provider state.
func AdvanceQualityCircuit(current QualityCircuit, event QualityCircuitEvent, policy QualityCircuitPolicy) QualityCircuitTransition {
	p := policy.withDefaults()
	rt := current
	if rt.State == "" {
		rt.State = CircuitClosed
	}
	now := event.Now
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	}
	rt.LastScore = event.Evaluation.Score

	switch rt.State {
	case CircuitClosed:
		if event.Evaluation.Eject {
			return openCircuit(rt, event.Evaluation.Reason, now, p, CircuitEject)
		}
		return circuitResult(rt, CircuitNoop, false, CircuitReasonHealthy, "质量未达到摘除条件")

	case CircuitOpen:
		if event.Kind != CircuitRecoveryProbe {
			return circuitResult(rt, CircuitKeepEjected, false, CircuitReasonProbeRequired, "等待主动恢复探测")
		}
		if now.Before(rt.ProbeAfter) {
			return circuitResult(rt, CircuitKeepEjected, false, CircuitReasonCooldown, "冷却期未结束，忽略提前探测")
		}
		if !probeHealthy(event.Evaluation, p) {
			return openCircuit(rt, event.Evaluation.Reason, now, p, CircuitKeepEjected)
		}
		rt.State = CircuitHalfOpen
		rt.HalfOpenSince = now
		rt.LastTransition = now
		rt.ConsecutiveProbeSuccesses = 1
		rt.LastReason = Reason{Code: CircuitReasonHalfOpen, Text: "首个恢复探测通过，进入半开观察"}
		if p.HalfOpenSuccesses == 1 {
			return closeCircuit(rt, now)
		}
		return QualityCircuitTransition{Circuit: rt, Action: CircuitKeepEjected, Changed: true, Reason: rt.LastReason}

	case CircuitHalfOpen:
		if event.Kind != CircuitRecoveryProbe {
			return circuitResult(rt, CircuitKeepEjected, false, CircuitReasonProbeRequired, "半开状态仅接受主动探测")
		}
		if !probeHealthy(event.Evaluation, p) {
			return openCircuit(rt, event.Evaluation.Reason, now, p, CircuitKeepEjected)
		}
		rt.ConsecutiveProbeSuccesses++
		if rt.ConsecutiveProbeSuccesses >= p.HalfOpenSuccesses {
			return closeCircuit(rt, now)
		}
		rt.LastReason = Reason{Code: CircuitReasonProbeSucceeded, Text: "恢复探测通过，继续半开观察"}
		return QualityCircuitTransition{Circuit: rt, Action: CircuitKeepEjected, Changed: true, Reason: rt.LastReason}

	default:
		rt.State = CircuitClosed
		rt.LastTransition = now
		return AdvanceQualityCircuit(rt, event, p)
	}
}

// RecoveryProbeDue lets a scheduler cheaply find circuits whose cooldown has
// elapsed. It does not change state and therefore cannot restore traffic by
// itself.
func RecoveryProbeDue(c QualityCircuit, now time.Time) bool {
	return c.State == CircuitOpen && !c.ProbeAfter.IsZero() && !now.Before(c.ProbeAfter)
}

func openCircuit(rt QualityCircuit, reason Reason, now time.Time, p QualityCircuitPolicy, action QualityCircuitAction) QualityCircuitTransition {
	previous := rt.State
	rt.State = CircuitOpen
	rt.OpenedAt = now
	rt.HalfOpenSince = time.Time{}
	rt.LastTransition = now
	rt.ConsecutiveProbeSuccesses = 0
	rt.OpenCount++
	rt.ProbeAfter = now.Add(cooldownFor(rt.ScopeKey, rt.OpenCount, p))
	if reason.Code == "" {
		reason = Reason{Code: CircuitReasonProbeFailed, Text: "质量探测未达到恢复标准"}
	}
	rt.LastReason = reason
	return QualityCircuitTransition{Circuit: rt, Action: action, Changed: previous != CircuitOpen || !rt.ProbeAfter.IsZero(), Reason: Reason{
		Code: CircuitReasonOpened,
		Text: reason.Text,
	}}
}

func closeCircuit(rt QualityCircuit, now time.Time) QualityCircuitTransition {
	rt.State = CircuitClosed
	rt.OpenedAt = time.Time{}
	rt.ProbeAfter = time.Time{}
	rt.HalfOpenSince = time.Time{}
	rt.LastTransition = now
	rt.OpenCount = 0
	rt.ConsecutiveProbeSuccesses = 0
	rt.LastReason = Reason{Code: CircuitReasonRestored, Text: "连续恢复探测通过，恢复参与调度"}
	return QualityCircuitTransition{Circuit: rt, Action: CircuitRestore, Changed: true, Reason: rt.LastReason}
}

func circuitResult(rt QualityCircuit, action QualityCircuitAction, changed bool, code, text string) QualityCircuitTransition {
	return QualityCircuitTransition{
		Circuit: rt,
		Action:  action,
		Changed: changed,
		Reason:  Reason{Code: code, Text: text},
	}
}

func probeHealthy(e PenaltyEvaluation, p QualityCircuitPolicy) bool {
	return e.Evidence > 0 && !e.Eject && !e.HardFailure && e.Score >= p.RecoveryScore
}

func cooldownFor(scopeKey string, openCount int, p QualityCircuitPolicy) time.Duration {
	if openCount < 1 {
		openCount = 1
	}
	multiplier := math.Pow(2, float64(openCount-1))
	d := time.Duration(float64(p.BaseCooldown) * multiplier)
	if d > p.MaxCooldown || d < 0 {
		d = p.MaxCooldown
	}
	if p.CooldownJitterFraction == 0 || scopeKey == "" {
		return d
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(scopeKey))
	unit := float64(h.Sum32()) / float64(math.MaxUint32) // [0,1]
	// Cooldown is a minimum quarantine, not a mean. Only add jitter so a 5m
	// base can never become a 4m probe; stagger up to +20% and keep the absolute
	// backoff cap authoritative.
	factor := 1 + unit*p.CooldownJitterFraction
	jittered := time.Duration(float64(d) * factor)
	if jittered > p.MaxCooldown {
		return p.MaxCooldown
	}
	return jittered
}
