package strategy

import (
	"fmt"
	"math"
	"sort"

	"e2m.local/contracts"
)

const (
	maxErrorPenalty    = 55.0
	maxTTFTPenalty     = 25.0
	maxDurationPenalty = 20.0
)

// PenaltyEvaluation is the explainable 100-point result for one downstream
// quality scope. Eject is a scheduling decision only; it does not imply that a
// credential should be deleted or that every downstream using the source must
// be drained.
type PenaltyEvaluation struct {
	ChannelID   string
	Score       float64
	Evidence    float64
	Eject       bool
	HardFailure bool
	Reason      Reason
	Reasons     []Reason
	Penalties   PenaltyBreakdown
	Snapshot    contracts.ChannelHealthSnapshot
}

// EvaluatePenalty starts every source at 100 and only subtracts observed
// quality regressions. Error rate, first-token latency and total duration are
// the only soft deductions in v1. Hard lifecycle/credential failures eject
// immediately regardless of samples or score.
//
// A thin window scales deductions by its evidence ratio. In particular, an
// empty window remains at 100 instead of treating an unused source as broken.
func EvaluatePenalty(s contracts.RouteStrategy, c Candidate) PenaltyEvaluation {
	rs := resolve(s)
	snap := c.Snapshot
	evidence := confidence(snap.QualitySampleCount, rs.thresholds.MinSamples)
	breakdown := calculatePenalties(rs.thresholds, snap, evidence)

	out := PenaltyEvaluation{
		ChannelID: c.Channel.ID,
		Score:     clamp(100-breakdown.TotalPenalty, 0, 100),
		Evidence:  evidence,
		Penalties: breakdown,
		Snapshot:  snap,
	}
	out.Reasons = penaltyReasons(out)

	if reason, hard := penaltyLifecycleGate(rs, c); reason != nil {
		out.Eject = true
		out.HardFailure = hard
		out.Reason = *reason
		return out
	}
	if out.Score <= rs.thresholds.EjectScore {
		out.Eject = true
		out.Reason = Reason{
			Code: GatePenaltyThreshold,
			Text: fmt.Sprintf("质量分 %.1f 已达到摘除线 %.1f", out.Score, rs.thresholds.EjectScore),
		}
	}
	return out
}

// RankByPenalty ranks candidates by remaining points and excludes candidates
// at/below the eject threshold. It is the quality-deduction replacement for
// Rank; Rank remains available while callers migrate.
func RankByPenalty(s contracts.RouteStrategy, candidates []Candidate) Ranking {
	rs := resolve(s)
	out := Ranking{Strategy: rs.typ}
	for i := range candidates {
		eval := EvaluatePenalty(s, candidates[i])
		penalties := eval.Penalties
		if eval.Eject {
			out.Excluded = append(out.Excluded, ExcludedCandidate{
				ChannelID:   eval.ChannelID,
				Reason:      eval.Reason,
				Score:       eval.Score,
				HardFailure: eval.HardFailure,
				Penalties:   &penalties,
				Snapshot:    eval.Snapshot,
			})
			continue
		}
		out.Eligible = append(out.Eligible, ScoredCandidate{
			ChannelID:   eval.ChannelID,
			RawScore:    eval.Score,
			Score:       eval.Score,
			Confidence:  eval.Evidence,
			FloorPassed: eval.Score > rs.thresholds.EjectScore,
			Reasons:     eval.Reasons,
			Snapshot:    eval.Snapshot,
			Penalties:   &penalties,
		})
	}
	sort.SliceStable(out.Eligible, func(i, j int) bool {
		if out.Eligible[i].Score != out.Eligible[j].Score {
			return out.Eligible[i].Score > out.Eligible[j].Score
		}
		return out.Eligible[i].ChannelID < out.Eligible[j].ChannelID
	})
	return out
}

func calculatePenalties(th contracts.StrategyThresholds, snap contracts.ChannelHealthSnapshot, evidence float64) PenaltyBreakdown {
	// Never fall back to ErrorRate/SuccessRate here: those legacy aggregates also
	// include user parameter errors and cancellations. Only the explicitly
	// classified upstream-responsibility rate may affect scheduling quality.
	errorRate := finiteUnit(snap.UpstreamErrorRate)

	// The full error budget is consumed at the configured hard success floor.
	// Latency budgets are consumed linearly from 20% of the ceiling to the
	// ceiling. Missing/non-finite latency does not lose points.
	errorBad := 1 - th.FloorSuccessRate
	if errorBad <= 0 {
		errorBad = 0.15
	}
	ttftGood := th.MaxTTFTP95MS * 0.20
	durationGood := th.MaxDurationP95MS * 0.20

	p := PenaltyBreakdown{
		ErrorRate:       errorRate,
		ErrorPenalty:    maxErrorPenalty * ratio(errorRate, 0, errorBad) * evidence,
		TTFTPenalty:     maxTTFTPenalty * ratio(finiteNonNegative(snap.TTFTP95), ttftGood, th.MaxTTFTP95MS) * evidence,
		DurationPenalty: maxDurationPenalty * ratio(finiteNonNegative(snap.DurationP95), durationGood, th.MaxDurationP95MS) * evidence,
	}
	p.TotalPenalty = clamp(p.ErrorPenalty+p.TTFTPenalty+p.DurationPenalty, 0, 100)
	return p
}

// penaltyLifecycleGate contains only definitive gates. Numeric quality does not
// appear here: it must accumulate as deductions and cross EjectScore. A
// recovering source is probe-only and cannot receive normal traffic yet.
func penaltyLifecycleGate(rs resolvedStrategy, c Candidate) (*Reason, bool) {
	switch c.Channel.Status {
	case contracts.UpstreamChannelRetired:
		return &Reason{Code: GateRetired, Text: "渠道已下线，不能参与调度"}, false
	case contracts.UpstreamChannelMaintenance:
		return &Reason{Code: GateMaintenance, Text: "渠道处于维护状态，不能参与调度"}, false
	}
	switch c.State {
	case contracts.HealthQuarantined:
		return &Reason{Code: GateQuarantined, Text: "渠道处于冷却隔离期"}, false
	case contracts.HealthRecovering:
		return &Reason{Code: GateRecovering, Text: "渠道处于半开恢复期，仅允许主动探测"}, false
	}
	// Credential failures belong to one downstream binding. Counts from a
	// provider/global snapshot must not eject every downstream using that source;
	// the caller can still pass the explicit flag when it has binding-level proof.
	scopedSnapshot := c.Snapshot.InstanceID != ""
	if c.AuthFailure || (scopedSnapshot && c.Snapshot.AuthFailureCount > 0) {
		return &Reason{Code: GateAuth, Text: "认证失败，立即摘除当前用户的凭证绑定"}, true
	}
	if c.InsufficientBalance || (scopedSnapshot && c.Snapshot.InsufficientBalanceCount > 0) {
		return &Reason{Code: GateBalance, Text: "余额不足，立即摘除当前用户的凭证绑定"}, true
	}
	// Provider-wide pressure and ordinary failure streaks remain soft evidence.
	// Only deductions crossing EjectScore may remove them; auth and balance are
	// the only event signals allowed to bypass the numeric score.
	return nil, false
}

func penaltyReasons(e PenaltyEvaluation) []Reason {
	reasons := []Reason{{
		Code: ReasonPenaltyScore,
		Text: fmt.Sprintf("质量分 %.1f = 100 - %.1f", e.Score, e.Penalties.TotalPenalty),
	}}
	if e.Penalties.ErrorPenalty > 0 {
		reasons = append(reasons, Reason{
			Code: ReasonPenaltyError,
			Text: fmt.Sprintf("上游责任错误率 %.1f%%，扣 %.1f 分", e.Penalties.ErrorRate*100, e.Penalties.ErrorPenalty),
		})
	}
	if e.Penalties.TTFTPenalty > 0 {
		reasons = append(reasons, Reason{
			Code: ReasonPenaltyTTFT,
			Text: fmt.Sprintf("p95 首字耗时 %.0fms，扣 %.1f 分", e.Snapshot.TTFTP95, e.Penalties.TTFTPenalty),
		})
	}
	if e.Penalties.DurationPenalty > 0 {
		reasons = append(reasons, Reason{
			Code: ReasonPenaltyTotal,
			Text: fmt.Sprintf("p95 总耗时 %.0fms，扣 %.1f 分", e.Snapshot.DurationP95, e.Penalties.DurationPenalty),
		})
	}
	if e.Evidence < 1 {
		reasons = append(reasons, Reason{
			Code: ReasonLowConfidence,
			Text: fmt.Sprintf("当前样本证据 %.0f%%，仅按该比例计入质量扣分", e.Evidence*100),
		})
	}
	return reasons
}

func ratio(v, good, bad float64) float64 {
	if v <= good || bad <= good {
		return 0
	}
	return clamp((v-good)/(bad-good), 0, 1)
}

func finiteUnit(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return clamp(v, 0, 1)
}

func finiteNonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
