package health

import (
	"fmt"
	"sort"
	"strings"

	"e2m.local/contracts"
)

// SwitchStrategy tunes how a backup is scored. The platform runs one strategy
// per deployment (config-driven); per-user overrides can layer on later.
type SwitchStrategy string

const (
	// StrategyStability prefers the most reliable backup: healthy, high
	// historical success, comfortable balance. The default for managed hosting.
	StrategyStability SwitchStrategy = "stability"
	// StrategyCost prefers cheaper backups once they clear a stability floor.
	StrategyCost SwitchStrategy = "cost"
	// StrategyPerformance prefers higher-priority / higher-capacity backups.
	StrategyPerformance SwitchStrategy = "performance"
)

// normalizeStrategy maps free-form config to a known strategy (default stability).
func normalizeStrategy(s string) SwitchStrategy {
	switch SwitchStrategy(strings.ToLower(strings.TrimSpace(s))) {
	case StrategyCost:
		return StrategyCost
	case StrategyPerformance:
		return StrategyPerformance
	default:
		return StrategyStability
	}
}

// backupSignals carries the runtime facts the scorer needs beyond the raw
// GatewayAccount: how the problem account looked (for affinity matching) and
// how the candidate has behaved historically (fail streak).
type backupSignals struct {
	// failStreak is the candidate's current consecutive-failure count. A
	// candidate with a live fail streak is a poor backup even if its status
	// currently reads healthy (it may be flapping).
	failStreak int
}

// candidateScore is one scored backup with a human-readable rationale so every
// automatic switch can explain *why* this account was chosen.
type candidateScore struct {
	account   contracts.GatewayAccount
	score     float64
	rationale []string
}

// selectBackup picks the best backup for a failing account under the given
// strategy, or nil when no healthy spare exists. It returns the winning score
// (with rationale) so the caller can audit and notify the reasoning.
//
// Selection rules shared by all strategies:
//   - never pick the problem account itself (excludeID),
//   - never pick an unhealthy candidate,
//   - prefer a currently non-scheduled spare (activate a spare rather than
//     double-count a live account) — a scheduled healthy account is still
//     eligible, just penalised, so a switch is never blocked for lack of a
//     dedicated spare.
//
// The concrete weighting differs per strategy; the rationale explains it.
func selectBackup(
	accounts []contracts.GatewayAccount,
	excludeID string,
	problem *contracts.GatewayAccount,
	strategy SwitchStrategy,
	signals map[string]backupSignals,
) *candidateScore {
	var scored []candidateScore
	for i := range accounts {
		ac := accounts[i]
		if ac.ID == excludeID {
			continue
		}
		if !accountHealthy(ac) {
			continue
		}
		cs := scoreCandidate(ac, problem, strategy, signals[ac.ID])
		scored = append(scored, cs)
	}
	if len(scored) == 0 {
		return nil
	}
	// Deterministic order: score desc, then account ID asc for stable tie-break.
	sort.SliceStable(scored, func(a, b int) bool {
		if scored[a].score != scored[b].score {
			return scored[a].score > scored[b].score
		}
		return scored[a].account.ID < scored[b].account.ID
	})
	best := scored[0]
	return &best
}

// scoreCandidate computes a backup's fitness plus the reasons behind it.
func scoreCandidate(
	ac contracts.GatewayAccount,
	problem *contracts.GatewayAccount,
	strategy SwitchStrategy,
	sig backupSignals,
) candidateScore {
	score := 0.0
	var why []string

	// 1. Spare preference: a non-scheduled healthy account is the ideal backup.
	if !ac.Schedulable {
		score += 40
		why = append(why, "空闲备号")
	} else {
		score += 10
		why = append(why, "已在调度(次选)")
	}

	// 2. Model / group affinity: a backup that serves the same groups as the
	// failing account keeps downstream models working. This is the single most
	// important correctness factor — a cheap, healthy backup that can't serve
	// the required model is useless.
	if problem != nil {
		if overlap := groupOverlap(problem.GroupIDs, ac.GroupIDs); overlap > 0 {
			bonus := 30.0 + 10.0*float64(overlap-1)
			if bonus > 60 {
				bonus = 60
			}
			score += bonus
			why = append(why, fmt.Sprintf("分组匹配×%d", overlap))
		} else if len(problem.GroupIDs) > 0 && len(ac.GroupIDs) > 0 {
			score -= 25
			why = append(why, "分组不匹配(降权)")
		}
		if problem.Platform != "" && ac.Platform == problem.Platform {
			score += 8
			why = append(why, "平台一致")
		}
	}

	// 3. Flapping guard: a candidate with a live fail streak is risky.
	if sig.failStreak > 0 {
		penalty := float64(sig.failStreak) * 12
		score -= penalty
		why = append(why, fmt.Sprintf("近期波动-%d", sig.failStreak))
	}

	// 4. Balance headroom: reward accounts that report spare balance so we don't
	// switch into an account that is about to run dry.
	if ac.Balance != nil {
		switch {
		case *ac.Balance <= 0:
			score -= 40
			why = append(why, "余额耗尽(降权)")
		case *ac.Balance < 5:
			score -= 10
			why = append(why, "余额偏低")
		default:
			score += 6
			why = append(why, "余额充足")
		}
	}

	// 5. Strategy-specific weighting.
	switch strategy {
	case StrategyCost:
		// Lower priority number == higher precedence in most gateways; for cost
		// we instead reward *lower* balance burn readiness only lightly and lean
		// on operator-provided cost labels when present.
		if c, ok := costLabel(ac); ok {
			// Cheaper (smaller cost) scores higher: invert within a 0..20 band.
			score += clamp(20-c, 0, 20)
			why = append(why, "成本优先")
		}
	case StrategyPerformance:
		// Higher priority (smaller Priority value) and more balance headroom win.
		if ac.Priority > 0 {
			score += clamp(float64(30-ac.Priority), 0, 30)
			why = append(why, fmt.Sprintf("优先级%d", ac.Priority))
		}
	default: // stability
		// Stability leans on the spare + affinity + flapping factors already
		// scored; give a small reliability nudge to unused, in-group spares.
		if !ac.Schedulable {
			score += 6
		}
	}

	return candidateScore{account: ac, score: score, rationale: why}
}

// reason renders the winning rationale as a compact human string for audits.
func (cs *candidateScore) reason() string {
	if cs == nil || len(cs.rationale) == 0 {
		return ""
	}
	return strings.Join(cs.rationale, "、")
}

// groupOverlap counts how many group IDs two accounts share.
func groupOverlap(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(a))
	for _, g := range a {
		set[g] = struct{}{}
	}
	n := 0
	for _, g := range b {
		if _, ok := set[g]; ok {
			n++
		}
	}
	return n
}

// costLabel reads an optional per-account cost hint from labels
// ("cost" as a small float, e.g. relative price). Absent -> not applicable.
func costLabel(ac contracts.GatewayAccount) (float64, bool) {
	// GatewayAccount has no labels today; cost hints ride on the Type field
	// convention "cost=<n>" when the platform sets it. Kept forward-compatible
	// and side-effect free: unknown formats simply return not-applicable.
	const prefix = "cost="
	if strings.HasPrefix(ac.Type, prefix) {
		var v float64
		if _, err := fmt.Sscanf(ac.Type[len(prefix):], "%f", &v); err == nil {
			return v, true
		}
	}
	return 0, false
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
