// Package strategy is the health-driven route-selection engine (Phase 3). It
// turns per-channel ChannelHealthSnapshots plus a RouteStrategy policy into an
// explainable ranking of which channel should carry traffic. It is deliberately
// pure and stateless: it reads snapshots and runtime signals, and returns a
// ranking with reasons. It never touches gateways. The automatic-switch
// orchestrator (Phase 4) turns a ranking into a RoutePlan change and runs it
// through reconcile dry-run/apply/rollback, so every real action stays audited
// and rollbackable.
//
// The design follows the doc's two-layer model:
//
//	hard gate  - disqualify a candidate outright (auth/balance failure, maintenance,
//	             quarantine, provider outage, success below the hard floor, p95
//	             latency over ceiling, a live failure streak). A gated candidate is
//	             never selected regardless of its score.
//	soft score - among survivors, blend the snapshot's 0..100 sub-scores with the
//	             strategy's weights and rank. Every score carries a confidence from
//	             its sample count so a thin, deceptively-perfect window is never
//	             trusted over a well-sampled healthy one.
//
// Every eligible and excluded candidate carries a Reason (stable ASCII code plus
// human Chinese text) so a decision and its notification can point at exactly why
// a channel won or was gated out.
package strategy

import "e2m.local/contracts"

// Reason is one explainable factor behind a ranking outcome. Code is a stable
// machine token (safe for tests and programmatic branching); Text is the human
// Chinese string used in notifications and the console.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Gate reason codes (hard-gate exclusions).
const (
	GateRetired             = "gate_retired"
	GateMaintenance         = "gate_maintenance"
	GateQuarantined         = "gate_quarantined"
	GateProviderDown        = "gate_provider_down"
	GateAuth                = "gate_auth"
	GateBalance             = "gate_balance"
	GateConsecutiveFailures = "gate_consecutive_failures"
	GateRecovering          = "gate_recovering"
	GatePenaltyThreshold    = "gate_penalty_threshold"
	GateSuccessFloor        = "gate_success_floor"
	GateTTFTP95             = "gate_ttft_p95"
	GateDurationP95         = "gate_duration_p95"
)

// Soft reason codes (attached to eligible, scored candidates).
const (
	ReasonScore         = "score"
	ReasonSuccess       = "success"
	ReasonStability     = "stability"
	ReasonLatency       = "latency"
	ReasonCost          = "cost"
	ReasonFloorPassed   = "floor_passed"
	ReasonFloorFailed   = "floor_failed"
	ReasonLowConfidence = "low_confidence"
	ReasonPenaltyScore  = "penalty_score"
	ReasonPenaltyError  = "penalty_error"
	ReasonPenaltyTTFT   = "penalty_ttft"
	ReasonPenaltyTotal  = "penalty_duration"
)

// PenaltyBreakdown records how a candidate lost points from the 100-point
// quality baseline. The three dimensions deliberately sum to at most 100.
// Missing observations never create a penalty.
type PenaltyBreakdown struct {
	ErrorRate float64 `json:"error_rate"`

	ErrorPenalty    float64 `json:"error_penalty"`
	TTFTPenalty     float64 `json:"ttft_penalty"`
	DurationPenalty float64 `json:"duration_penalty"`
	TotalPenalty    float64 `json:"total_penalty"`
}

// Candidate is one channel offered to the engine: its platform lifecycle record,
// the primary decision snapshot (the 5m window in the first version), the runtime
// health-state owned by the orchestrator (quarantined/recovering aware), and the
// few severe-failure signals that a numeric snapshot cannot express on its own.
type Candidate struct {
	Channel  contracts.UpstreamChannel
	Snapshot contracts.ChannelHealthSnapshot

	// State is the channel's runtime lifecycle state (see the state machine). A
	// quarantined channel is hard-gated out of selection until it recovers.
	State contracts.HealthState

	// Severe-failure signals the caller derives from recent observations (the
	// aggregator collapses these into HealthState/RiskScore, so the engine takes
	// them explicitly to gate precisely).
	AuthFailure         bool
	InsufficientBalance bool
	ConsecutiveFailures int
	// ProviderDown marks a provider-wide outage: every channel from that provider
	// is gated so the engine does not switch within a failing provider.
	ProviderDown bool
}

// ScoredCandidate is one eligible channel with its blended score and rationale.
// RawScore is the pure weighted blend; Score is RawScore adjusted by Confidence
// (the value ranking uses); FloorPassed reports whether the candidate cleared the
// quality floor (the gate cost_first ranks around).
type ScoredCandidate struct {
	ChannelID   string
	RawScore    float64
	Score       float64
	Confidence  float64
	FloorPassed bool
	Reasons     []Reason
	Snapshot    contracts.ChannelHealthSnapshot
	Penalties   *PenaltyBreakdown `json:"penalties,omitempty"`
}

// ExcludedCandidate is one channel removed by a hard gate, with the gate reason.
type ExcludedCandidate struct {
	ChannelID   string
	Reason      Reason
	Score       float64                         `json:"score,omitempty"`
	HardFailure bool                            `json:"hard_failure,omitempty"`
	Penalties   *PenaltyBreakdown               `json:"penalties,omitempty"`
	Snapshot    contracts.ChannelHealthSnapshot `json:"snapshot,omitempty"`
}

// Ranking is the engine output: eligible candidates sorted best-first and the
// excluded ones with their gate reasons.
type Ranking struct {
	Strategy contracts.RouteStrategyType
	Eligible []ScoredCandidate
	Excluded []ExcludedCandidate
}

// Best returns the top eligible candidate, or false when everything was gated.
func (r Ranking) Best() (ScoredCandidate, bool) {
	if len(r.Eligible) == 0 {
		return ScoredCandidate{}, false
	}
	return r.Eligible[0], true
}
