package strategy

import (
	"testing"
	"time"

	"e2m.local/contracts"
)

// --- helpers ---

func chan_(id string, status contracts.UpstreamChannelStatus) contracts.UpstreamChannel {
	return contracts.UpstreamChannel{ID: id, PoolID: "pool-1", Status: status}
}

// snap builds a well-sampled snapshot with explicit sub-scores so tests drive the
// blend directly rather than re-deriving the aggregator's maths.
func snap(sample int, success, ttftP95, durP95 float64, sub subs) contracts.ChannelHealthSnapshot {
	return contracts.ChannelHealthSnapshot{
		Window:             contracts.Window5m,
		SampleCount:        sample,
		SuccessRate:        success,
		QualitySampleCount: sample,
		QualitySuccessRate: success,
		QualityErrorRate:   1 - success,
		TTFTP95:            ttftP95,
		DurationP95:        durP95,
		SuccessScore:       sub.success,
		TTFTScore:          sub.ttft,
		DurationScore:      sub.duration,
		StabilityScore:     sub.stability,
		CostScore:          sub.cost,
	}
}

type subs struct {
	success, ttft, duration, stability, cost float64
}

func mustBest(t *testing.T, r Ranking) ScoredCandidate {
	t.Helper()
	best, ok := r.Best()
	if !ok {
		t.Fatalf("expected an eligible candidate, got none (excluded=%d)", len(r.Excluded))
	}
	return best
}

func excludedCode(r Ranking, channelID string) (string, bool) {
	for _, e := range r.Excluded {
		if e.ChannelID == channelID {
			return e.Reason.Code, true
		}
	}
	return "", false
}

// --- strategy defaults / normalize ---

func TestNormalizeDefaultsToStability(t *testing.T) {
	if got := contracts.RouteStrategyType("bogus").Normalize(); got != contracts.StrategyStabilityFirst {
		t.Fatalf("unknown type should normalize to stability_first, got %q", got)
	}
	if got := contracts.StrategyCostFirst.Normalize(); got != contracts.StrategyCostFirst {
		t.Fatalf("known type must be preserved, got %q", got)
	}
}

func TestDefaultWeightsSumToOne(t *testing.T) {
	for _, typ := range []contracts.RouteStrategyType{
		contracts.StrategyStabilityFirst, contracts.StrategyCostFirst,
		contracts.StrategyLatencyFirst, contracts.StrategyBalanced,
	} {
		w := defaultWeights(typ)
		sum := w.Success + w.TTFT + w.Duration + w.Stability + w.Cost
		if sum < 0.999 || sum > 1.001 {
			t.Fatalf("%s weights must sum to 1.0, got %v", typ, sum)
		}
	}
}

// --- soft scoring / ranking ---

// stability_first must prefer the reliable, low-volatility channel even when a
// rival is faster/cheaper but shakier.
func TestStabilityFirstPrefersReliable(t *testing.T) {
	rock := Candidate{ // high success + stability, mediocre latency/cost
		Channel:  chan_("rock", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.99, 1200, 6000, subs{success: 99, ttft: 60, duration: 60, stability: 95, cost: 30}),
	}
	flashy := Candidate{ // fast + cheap but lower success + jittery
		Channel:  chan_("flashy", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.90, 400, 2000, subs{success: 90, ttft: 100, duration: 100, stability: 40, cost: 100}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{flashy, rock})
	if best := mustBest(t, r); best.ChannelID != "rock" {
		t.Fatalf("stability_first should pick rock, got %q", best.ChannelID)
	}
}

// latency_first flips the same field: the fast channel should now win.
func TestLatencyFirstPrefersFast(t *testing.T) {
	rock := Candidate{
		Channel:  chan_("rock", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.99, 1200, 6000, subs{success: 99, ttft: 60, duration: 60, stability: 95, cost: 30}),
	}
	flashy := Candidate{
		Channel:  chan_("flashy", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.97, 400, 2000, subs{success: 97, ttft: 100, duration: 100, stability: 60, cost: 100}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyLatencyFirst}, []Candidate{rock, flashy})
	if best := mustBest(t, r); best.ChannelID != "flashy" {
		t.Fatalf("latency_first should pick flashy, got %q", best.ChannelID)
	}
}

// cost_first: among channels that clear the quality floor, the cheapest wins.
func TestCostFirstCheapestAboveFloor(t *testing.T) {
	premium := Candidate{ // clears floor, expensive
		Channel:  chan_("premium", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.99, 900, 5000, subs{success: 99, ttft: 90, duration: 90, stability: 90, cost: 20}),
	}
	cheap := Candidate{ // clears floor, cheap
		Channel:  chan_("cheap", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.97, 1500, 8000, subs{success: 97, ttft: 70, duration: 70, stability: 80, cost: 95}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, []Candidate{premium, cheap})
	if best := mustBest(t, r); best.ChannelID != "cheap" {
		t.Fatalf("cost_first should pick the cheap floor-passer, got %q", best.ChannelID)
	}
}

// cost_first quality floor is non-negotiable: a dirt-cheap channel BELOW the
// floor must rank behind a pricier channel that clears it, even if its raw blend
// (dominated by the cost weight) is numerically higher.
func TestCostFirstFloorOverridesCheapButUnreliable(t *testing.T) {
	cheapBad := Candidate{ // cheapest, but success below target floor (0.95)
		Channel:  chan_("cheap-bad", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.93, 1000, 6000, subs{success: 93, ttft: 90, duration: 90, stability: 70, cost: 100}),
	}
	solid := Candidate{ // pricier, clears floor
		Channel:  chan_("solid", contracts.UpstreamChannelActive),
		Snapshot: snap(50, 0.98, 1000, 6000, subs{success: 98, ttft: 90, duration: 90, stability: 90, cost: 40}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, []Candidate{cheapBad, solid})
	best := mustBest(t, r)
	if best.ChannelID != "solid" {
		t.Fatalf("cost_first floor must rank the floor-passer first, got %q", best.ChannelID)
	}
	// cheapBad is still eligible (not hard-gated at 0.93 >= 0.85 hard floor), just
	// ranked below via FloorPassed=false.
	if best.FloorPassed != true {
		t.Fatalf("winner should have passed the floor")
	}
	for _, e := range r.Eligible {
		if e.ChannelID == "cheap-bad" && e.FloorPassed {
			t.Fatalf("cheap-bad is below target success, must not pass the floor")
		}
	}
}

// --- hard gates ---

func TestHardGates(t *testing.T) {
	base := subs{success: 99, ttft: 90, duration: 90, stability: 90, cost: 90}
	good := snap(50, 0.99, 900, 5000, base)

	cases := []struct {
		name     string
		cand     Candidate
		wantCode string
	}{
		{"retired", Candidate{Channel: chan_("c", contracts.UpstreamChannelRetired), Snapshot: good}, GateRetired},
		{"maintenance", Candidate{Channel: chan_("c", contracts.UpstreamChannelMaintenance), Snapshot: good}, GateMaintenance},
		{"quarantined", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: good, State: contracts.HealthQuarantined}, GateQuarantined},
		{"auth", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: good, AuthFailure: true}, GateAuth},
		{"balance", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: good, InsufficientBalance: true}, GateBalance},
		{"success-floor", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: snap(50, 0.50, 900, 5000, base)}, GateSuccessFloor},
		{"ttft-ceiling", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: snap(50, 0.99, 9000, 5000, base)}, GateTTFTP95},
		{"duration-ceiling", Candidate{Channel: chan_("c", contracts.UpstreamChannelActive), Snapshot: snap(50, 0.99, 900, 90000, base)}, GateDurationP95},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{tc.cand})
			if len(r.Eligible) != 0 {
				t.Fatalf("candidate should be gated out, but was eligible")
			}
			code, ok := excludedCode(r, "c")
			if !ok || code != tc.wantCode {
				t.Fatalf("want gate %q, got %q (present=%v)", tc.wantCode, code, ok)
			}
		})
	}
}

func TestCapacitySignalsDoNotHardGateHealthyScore(t *testing.T) {
	good := snap(50, 0.99, 900, 5000, subs{success: 99, ttft: 90, duration: 90, stability: 90, cost: 90})
	for _, candidate := range []Candidate{
		{Channel: chan_("provider", contracts.UpstreamChannelActive), Snapshot: good, ProviderDown: true},
		{Channel: chan_("streak", contracts.UpstreamChannelActive), Snapshot: good, ConsecutiveFailures: 3},
	} {
		ranking := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{candidate})
		if len(ranking.Eligible) != 1 || len(ranking.Excluded) != 0 {
			t.Fatalf("soft capacity signal bypassed numeric quality: %+v", ranking)
		}
	}
}

// A low-sample window must NOT trip the numeric gates (idle != broken): a channel
// with a bad-looking but tiny window stays eligible, just low-confidence.
func TestLowSampleDoesNotHardGateOnNumbers(t *testing.T) {
	cand := Candidate{
		Channel:  chan_("thin", contracts.UpstreamChannelActive),
		Snapshot: snap(2, 0.50, 9000, 90000, subs{success: 50, ttft: 10, duration: 10, stability: 40, cost: 90}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{cand})
	if len(r.Eligible) != 1 {
		t.Fatalf("thin window must stay eligible (not numerically gated), excluded=%v", r.Excluded)
	}
}

// But a severe event signal (auth) gates even a thin window: event trumps sample
// count.
func TestSevereSignalGatesEvenThinWindow(t *testing.T) {
	cand := Candidate{
		Channel:     chan_("thin", contracts.UpstreamChannelActive),
		Snapshot:    snap(1, 1.0, 100, 500, subs{success: 100, ttft: 100, duration: 100, stability: 100, cost: 100}),
		AuthFailure: true,
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{cand})
	if code, ok := excludedCode(r, "thin"); !ok || code != GateAuth {
		t.Fatalf("auth must gate even a thin perfect window, got %q ok=%v", code, ok)
	}
}

// Confidence: a thin "perfect" window must not outrank a well-sampled strong one.
func TestConfidenceDiscountsThinWindow(t *testing.T) {
	thinPerfect := Candidate{
		Channel:  chan_("thin", contracts.UpstreamChannelActive),
		Snapshot: snap(1, 1.0, 100, 500, subs{success: 100, ttft: 100, duration: 100, stability: 100, cost: 100}),
	}
	deepStrong := Candidate{
		Channel:  chan_("deep", contracts.UpstreamChannelActive),
		Snapshot: snap(100, 0.98, 900, 5000, subs{success: 98, ttft: 92, duration: 92, stability: 95, cost: 60}),
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{thinPerfect, deepStrong})
	if best := mustBest(t, r); best.ChannelID != "deep" {
		t.Fatalf("well-sampled channel should outrank a thin perfect one, got %q", best.ChannelID)
	}
}

func TestRankUsesQualityPopulationForConfidenceAndSuccessGate(t *testing.T) {
	cand := Candidate{
		Channel: chan_("quality-clean", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount: 100, SuccessRate: 0.1, ErrorRate: 0.9,
			QualitySampleCount: 10, QualitySuccessRate: 1,
			SuccessScore: 100, TTFTScore: 100, DurationScore: 100, StabilityScore: 100, CostScore: 100,
		},
	}
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{cand})
	best := mustBest(t, r)
	if best.ChannelID != "quality-clean" || best.Confidence != 1 {
		t.Fatalf("factual client failures must not gate or reduce upstream confidence: %+v", r)
	}

	cand.Snapshot.QualitySampleCount = 1
	r = Rank(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, []Candidate{cand})
	best = mustBest(t, r)
	if best.Confidence != 0.2 {
		t.Fatalf("confidence must use quality samples, got %v", best.Confidence)
	}
}

func TestEmptyCandidatesHasNoBest(t *testing.T) {
	r := Rank(contracts.RouteStrategy{Type: contracts.StrategyCostFirst}, nil)
	if _, ok := r.Best(); ok {
		t.Fatalf("no candidates must yield no best")
	}
}

// --- state machine ---

func TestStateMachineHealthyToDegradedToUnhealthy(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	th := DefaultStateThresholds() // DegradedWindowsToUnhealthy = 2
	rt := &ChannelRuntime{ChannelID: "c", State: contracts.HealthHealthy}

	if s := rt.Transition(contracts.HealthDegraded, now, th); s != contracts.HealthDegraded {
		t.Fatalf("first degraded verdict -> degraded, got %q", s)
	}
	if s := rt.Transition(contracts.HealthDegraded, now.Add(time.Minute), th); s != contracts.HealthUnhealthy {
		t.Fatalf("sustained degraded -> unhealthy, got %q", s)
	}
}

// A HealthUnknown verdict must not change lifecycle state.
func TestStateMachineUnknownHoldsState(t *testing.T) {
	now := time.Now().UTC()
	rt := &ChannelRuntime{ChannelID: "c", State: contracts.HealthDegraded}
	if s := rt.Transition(contracts.HealthUnknown, now, DefaultStateThresholds()); s != contracts.HealthDegraded {
		t.Fatalf("unknown verdict must hold state, got %q", s)
	}
}

// Switch back slow: unhealthy -> recovering, and recovering only promotes to
// healthy after BOTH sustained healthy windows AND the observation time.
func TestStateMachineRecoveryIsSlow(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	th := StateThresholds{HealthyWindowsToRecover: 2, RecoveryObservation: 15 * time.Minute}
	rt := &ChannelRuntime{ChannelID: "c", State: contracts.HealthUnhealthy}

	// First healthy verdict moves unhealthy -> recovering (never straight to healthy).
	if s := rt.Transition(contracts.HealthHealthy, now, th); s != contracts.HealthRecovering {
		t.Fatalf("unhealthy+healthy -> recovering, got %q", s)
	}
	// Sustained healthy but observation time not elapsed yet: stay recovering.
	if s := rt.Transition(contracts.HealthHealthy, now.Add(1*time.Minute), th); s != contracts.HealthRecovering {
		t.Fatalf("recovery before observation window must stay recovering, got %q", s)
	}
	// Enough healthy windows AND time elapsed: promote.
	if s := rt.Transition(contracts.HealthHealthy, now.Add(20*time.Minute), th); s != contracts.HealthHealthy {
		t.Fatalf("recovery after window+time -> healthy, got %q", s)
	}
}

// A relapse during recovery drops straight back to unhealthy.
func TestStateMachineRelapseDuringRecovery(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	rt := &ChannelRuntime{ChannelID: "c", State: contracts.HealthRecovering, RecoveringSince: now}
	if s := rt.Transition(contracts.HealthDegraded, now.Add(2*time.Minute), DefaultStateThresholds()); s != contracts.HealthUnhealthy {
		t.Fatalf("relapse in recovery -> unhealthy, got %q", s)
	}
}

// Quarantine/BeginRecovery are the only lifecycle doorways.
func TestStateMachineQuarantineLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	rt := &ChannelRuntime{ChannelID: "c", State: contracts.HealthUnhealthy}
	rt.Quarantine(now)
	if rt.State != contracts.HealthQuarantined {
		t.Fatalf("Quarantine must set quarantined, got %q", rt.State)
	}
	// Data verdicts are ignored while quarantined.
	if s := rt.Transition(contracts.HealthHealthy, now.Add(time.Minute), DefaultStateThresholds()); s != contracts.HealthQuarantined {
		t.Fatalf("quarantined must ignore data verdicts, got %q", s)
	}
	rt.BeginRecovery(now.Add(2 * time.Minute))
	if rt.State != contracts.HealthRecovering {
		t.Fatalf("BeginRecovery must set recovering, got %q", rt.State)
	}
}

// --- dampening ---

func TestDampeningCooldown(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	s := contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst, CooldownSeconds: 600}
	// last switch 5 min ago, cooldown 10 min -> blocked.
	d := AllowSwitch(s, now.Add(-5*time.Minute), nil, now)
	if d.Allowed || d.Reason.Code != DampenCooldown {
		t.Fatalf("within cooldown must block with cooldown reason, got allowed=%v code=%q", d.Allowed, d.Reason.Code)
	}
	// last switch 11 min ago -> allowed.
	d = AllowSwitch(s, now.Add(-11*time.Minute), nil, now)
	if !d.Allowed {
		t.Fatalf("past cooldown must allow, got %q", d.Reason.Code)
	}
}

func TestDampeningMaxPerHour(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	s := contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst, MaxAutoSwitchesPerHour: 2}
	recent := []time.Time{now.Add(-10 * time.Minute), now.Add(-20 * time.Minute)}
	d := AllowSwitch(s, time.Time{}, recent, now)
	if d.Allowed || d.Reason.Code != DampenMaxRate {
		t.Fatalf("at hourly cap must block, got allowed=%v code=%q", d.Allowed, d.Reason.Code)
	}
	// One of them is older than an hour -> only 1 counts -> allowed.
	recent = []time.Time{now.Add(-10 * time.Minute), now.Add(-70 * time.Minute)}
	d = AllowSwitch(s, time.Time{}, recent, now)
	if !d.Allowed {
		t.Fatalf("only one switch within the hour should allow, got %q", d.Reason.Code)
	}
}

func TestDampeningNoLimitsAllows(t *testing.T) {
	now := time.Now().UTC()
	d := AllowSwitch(contracts.RouteStrategy{Type: contracts.StrategyStabilityFirst}, time.Time{}, nil, now)
	if !d.Allowed || d.Reason.Code != DampenOK {
		t.Fatalf("no limits configured must allow, got allowed=%v code=%q", d.Allowed, d.Reason.Code)
	}
}
