package strategy

import (
	"fmt"
	"sort"

	"e2m.local/contracts"
)

// Rank evaluates every candidate against the strategy: it hard-gates the
// ineligible ones (with a reason) and scores the survivors, returning them
// sorted best-first. It is pure: no I/O, no gateway calls, deterministic for a
// given input (ties break by channel ID).
//
// Scoring uses the snapshot's pre-computed 0..100 sub-scores blended by the
// strategy weights, then discounts by a sample-count confidence so a thin,
// deceptively-perfect window never outranks a well-sampled healthy channel. For
// cost_first the quality floor is a first-class sort key: channels that clear the
// floor always rank above those that do not, and only then does cheaper win.
func Rank(s contracts.RouteStrategy, candidates []Candidate) Ranking {
	rs := resolve(s)
	out := Ranking{Strategy: rs.typ}
	for i := range candidates {
		scored, excluded := evaluate(rs, candidates[i])
		if excluded != nil {
			out.Excluded = append(out.Excluded, *excluded)
			continue
		}
		out.Eligible = append(out.Eligible, scored)
	}
	sortEligible(rs.typ, out.Eligible)
	return out
}

// evaluate applies the hard gates then, if the candidate survives, scores it.
// It returns exactly one of (scored, nil) or (zero, excluded).
func evaluate(rs resolvedStrategy, c Candidate) (ScoredCandidate, *ExcludedCandidate) {
	if ex := hardGate(rs, c); ex != nil {
		return ScoredCandidate{}, ex
	}
	return score(rs, c), nil
}

// hardGate returns the first disqualifying reason, or nil if the candidate is
// eligible. Order is by explainability: lifecycle and severe-signal gates (which
// hold regardless of sample count) come before numeric gates (which only apply
// once the window has enough samples to trust).
func hardGate(rs resolvedStrategy, c Candidate) *ExcludedCandidate {
	id := c.Channel.ID
	th := rs.thresholds

	// --- lifecycle gates: independent of measured data ---
	switch c.Channel.Status {
	case contracts.UpstreamChannelRetired:
		return excl(id, GateRetired, "渠道已下线(retired)，不参与选路")
	case contracts.UpstreamChannelMaintenance:
		return excl(id, GateMaintenance, "渠道处于维护态，手动维护优先于自动策略")
	}
	if c.State == contracts.HealthQuarantined {
		return excl(id, GateQuarantined, "渠道已被隔离(quarantined)，等待恢复观察期")
	}

	// --- severe-signal gates: event-based, trusted regardless of sample count ---
	if c.AuthFailure {
		return excl(id, GateAuth, "认证失败，直接踢出候选")
	}
	if c.InsufficientBalance {
		return excl(id, GateBalance, "余额不足，直接踢出候选")
	}

	// --- numeric gates: only meaningful once the window is well-sampled. A
	// low-sample window is handled as low-confidence in scoring, never as a
	// confirmed failure, so an idle channel is not mistaken for a broken one. ---
	snap := c.Snapshot
	if snap.QualitySampleCount >= th.MinSamples {
		if snap.QualitySuccessRate < th.FloorSuccessRate {
			return excl(id, GateSuccessFloor,
				fmt.Sprintf("近窗质量成功率 %.1f%% 低于硬底线 %.1f%%", snap.QualitySuccessRate*100, th.FloorSuccessRate*100))
		}
		if snap.TTFTP95 > 0 && snap.TTFTP95 > th.MaxTTFTP95MS {
			return excl(id, GateTTFTP95,
				fmt.Sprintf("首字延迟 p95 %.0fms 超过上限 %.0fms", snap.TTFTP95, th.MaxTTFTP95MS))
		}
		if snap.DurationP95 > 0 && snap.DurationP95 > th.MaxDurationP95MS {
			return excl(id, GateDurationP95,
				fmt.Sprintf("总耗时 p95 %.0fms 超过上限 %.0fms", snap.DurationP95, th.MaxDurationP95MS))
		}
	}
	return nil
}

// score blends the snapshot's sub-scores with the strategy weights and applies a
// sample-count confidence discount. It records an explainable reason set.
func score(rs resolvedStrategy, c Candidate) ScoredCandidate {
	snap := c.Snapshot
	w := rs.weights
	raw := clamp(
		snap.SuccessScore*w.Success+
			snap.TTFTScore*w.TTFT+
			snap.DurationScore*w.Duration+
			snap.StabilityScore*w.Stability+
			snap.CostScore*w.Cost,
		0, 100)

	conf := confidence(snap.QualitySampleCount, rs.thresholds.MinSamples)
	floorOK := passesQualityFloor(rs.thresholds, snap)

	sc := ScoredCandidate{
		ChannelID:   c.Channel.ID,
		RawScore:    raw,
		Score:       raw * conf,
		Confidence:  conf,
		FloorPassed: floorOK,
		Snapshot:    snap,
	}
	sc.Reasons = append(sc.Reasons, Reason{
		Code: ReasonScore,
		Text: fmt.Sprintf("综合评分 %.1f（原始 %.1f × 置信度 %.0f%%，策略 %s）", sc.Score, raw, conf*100, rs.typ),
	})
	sc.Reasons = append(sc.Reasons, Reason{
		Code: ReasonSuccess,
		Text: fmt.Sprintf("成功率 %.1f%% / p95首字 %.0fms / p95总耗时 %.0fms / 成本分 %.0f",
			snap.QualitySuccessRate*100, snap.TTFTP95, snap.DurationP95, snap.CostScore),
	})
	if conf < 1 {
		sc.Reasons = append(sc.Reasons, Reason{
			Code: ReasonLowConfidence,
			Text: fmt.Sprintf("样本不足（%d/%d），已下调评分置信度，避免过度信任薄弱数据",
				snap.QualitySampleCount, rs.thresholds.MinSamples),
		})
	}
	if rs.typ == contracts.StrategyCostFirst {
		if floorOK {
			sc.Reasons = append(sc.Reasons, Reason{Code: ReasonFloorPassed, Text: "已满足成本优先的质量底线，进入按成本排序"})
		} else {
			sc.Reasons = append(sc.Reasons, Reason{Code: ReasonFloorFailed, Text: "未满足质量底线，成本优先下被降级排序"})
		}
	}
	return sc
}

// confidence maps a window's sample count onto 0..1: a window at or above
// MinSamples is fully trusted; below it, confidence scales down linearly so a
// two-sample "perfect" channel cannot outrank a well-sampled healthy one.
func confidence(sampleCount, minSamples int) float64 {
	if minSamples <= 0 {
		return 1
	}
	if sampleCount >= minSamples {
		return 1
	}
	if sampleCount <= 0 {
		return 0
	}
	return float64(sampleCount) / float64(minSamples)
}

// passesQualityFloor reports whether a snapshot clears the strategy's quality
// floor: enough samples to trust, success at/above target, and p95 latencies
// within ceiling. It is the gate cost_first ranks around ("cheapest that clears
// the floor"), and a general signal other strategies can surface.
func passesQualityFloor(th contracts.StrategyThresholds, snap contracts.ChannelHealthSnapshot) bool {
	if snap.QualitySampleCount < th.MinSamples {
		return false
	}
	if snap.QualitySuccessRate < th.TargetSuccessRate {
		return false
	}
	if snap.TTFTP95 > 0 && snap.TTFTP95 > th.MaxTTFTP95MS {
		return false
	}
	if snap.DurationP95 > 0 && snap.DurationP95 > th.MaxDurationP95MS {
		return false
	}
	return true
}

// sortEligible orders survivors best-first. cost_first ranks floor-passers above
// non-passers first (quality floor is non-negotiable), then by score; every
// other strategy ranks purely by score. Ties break by channel ID for
// determinism.
func sortEligible(typ contracts.RouteStrategyType, list []ScoredCandidate) {
	sort.SliceStable(list, func(a, b int) bool {
		x, y := list[a], list[b]
		if typ == contracts.StrategyCostFirst && x.FloorPassed != y.FloorPassed {
			return x.FloorPassed // a floor-passer sorts before a non-passer
		}
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		return x.ChannelID < y.ChannelID
	})
}

func excl(channelID, code, text string) *ExcludedCandidate {
	return &ExcludedCandidate{ChannelID: channelID, Reason: Reason{Code: code, Text: text}}
}
