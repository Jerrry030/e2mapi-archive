package strategy

import (
	"testing"

	"e2m.local/contracts"
)

func TestPenaltyStartsAtFullScoreWithoutSamples(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
	})
	if eval.Score != 100 || eval.Evidence != 0 || eval.Eject {
		t.Fatalf("empty source should start at 100 without ejection, got score=%v evidence=%v eject=%v", eval.Score, eval.Evidence, eval.Eject)
	}
	if eval.Penalties.TotalPenalty != 0 {
		t.Fatalf("missing data must not incur a penalty, got %+v", eval.Penalties)
	}
}

func TestPenaltyUsesOnlyDeductions(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount:        50,
			SuccessRate:        0.90,
			ErrorRate:          0.10,
			QualitySampleCount: 50,
			UpstreamErrorRate:  0.10,
			TTFTP95:            3360,
			DurationP95:        4000,
		},
	})
	// Error: 55*(.10/.15)=36.67, TTFT: 25*((3360-800)/(4000-800))=20.
	if !closeTo(eval.Penalties.ErrorPenalty, 36.6667, .001) {
		t.Fatalf("unexpected error penalty: %+v", eval.Penalties)
	}
	if !closeTo(eval.Penalties.TTFTPenalty, 20, .001) {
		t.Fatalf("unexpected ttft penalty: %+v", eval.Penalties)
	}
	if eval.Penalties.DurationPenalty != 0 {
		t.Fatalf("duration at the good reference must not lose points: %+v", eval.Penalties)
	}
	if !closeTo(eval.Score, 43.3333, .001) || !eval.Eject {
		t.Fatalf("combined regression must cross the default 60-point eject line, got score=%v eject=%v", eval.Score, eval.Eject)
	}
	if eval.Reason.Code != GatePenaltyThreshold || eval.HardFailure {
		t.Fatalf("soft quality ejection must be threshold-based, got reason=%q hard=%v", eval.Reason.Code, eval.HardFailure)
	}
}

func TestPenaltyThinWindowScalesDeductionsInsteadOfBaseline(t *testing.T) {
	strat := contracts.RouteStrategy{Thresholds: contracts.StrategyThresholds{MinSamples: 10}}
	thin := Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount:        1,
			SuccessRate:        0,
			ErrorRate:          1,
			QualitySampleCount: 1,
			UpstreamErrorRate:  1,
			TTFTP95:            4000,
			DurationP95:        20000,
		},
	}
	eval := EvaluatePenalty(strat, thin)
	if !closeTo(eval.Evidence, .1, .001) {
		t.Fatalf("one of ten required samples should have 10%% evidence, got %v", eval.Evidence)
	}
	if !closeTo(eval.Score, 90, .001) || eval.Eject {
		t.Fatalf("thin bad window should deduct 10%% of its full penalty, got score=%v eject=%v", eval.Score, eval.Eject)
	}
}

func TestPenaltyDoesNotChargeDownstreamErrorsOrCancellation(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount:       50,
			SuccessRate:       0,
			ErrorRate:         1,
			UpstreamErrorRate: 0,
		},
	})
	if eval.Penalties.ErrorPenalty != 0 || eval.Score != 100 || eval.Eject {
		t.Fatalf("legacy/downstream error rate must not punish the upstream: %+v", eval)
	}
}

func TestPenaltyHardFailuresEjectImmediately(t *testing.T) {
	cases := []struct {
		name string
		cand Candidate
		code string
	}{
		{name: "auth", cand: Candidate{AuthFailure: true}, code: GateAuth},
		{name: "balance", cand: Candidate{InsufficientBalance: true}, code: GateBalance},
		{name: "snapshot-auth", cand: Candidate{Snapshot: contracts.ChannelHealthSnapshot{InstanceID: "instance-1", AuthFailureCount: 1}}, code: GateAuth},
		{name: "snapshot-balance", cand: Candidate{Snapshot: contracts.ChannelHealthSnapshot{InstanceID: "instance-1", InsufficientBalanceCount: 1}}, code: GateBalance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cand.Channel = chan_("a", contracts.UpstreamChannelActive)
			eval := EvaluatePenalty(contracts.RouteStrategy{}, tc.cand)
			if !eval.Eject || !eval.HardFailure || eval.Reason.Code != tc.code {
				t.Fatalf("hard signal must eject immediately: %+v", eval)
			}
			if eval.Score != 100 {
				t.Fatalf("hard action is independent of the numeric score, got %v", eval.Score)
			}
		})
	}
}

func TestPenaltyCapacitySignalsRequireNumericScoreToCrossEjectLine(t *testing.T) {
	cases := []struct {
		name string
		cand Candidate
	}{
		{name: "provider", cand: Candidate{ProviderDown: true}},
		{name: "streak", cand: Candidate{ConsecutiveFailures: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cand.Channel = chan_("a", contracts.UpstreamChannelActive)
			eval := EvaluatePenalty(contracts.RouteStrategy{}, tc.cand)
			if eval.Score != 100 || eval.Eject || eval.HardFailure {
				t.Fatalf("capacity-like signal cannot bypass the numeric eject line: %+v", eval)
			}
		})
	}
}

func TestPenaltyCapacitySignalStillEjectsAfterDeductionsCrossLine(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel:      chan_("a", contracts.UpstreamChannelActive),
		ProviderDown: true,
		Snapshot: contracts.ChannelHealthSnapshot{
			QualitySampleCount: 50,
			UpstreamErrorRate:  0.20,
			TTFTP95:            4000,
			DurationP95:        20000,
		},
	})
	if !eval.Eject || eval.HardFailure || eval.Score > 60 || eval.Reason.Code != GatePenaltyThreshold {
		t.Fatalf("soft signal may eject only after deductions cross the line: %+v", eval)
	}
}

func TestPenaltyDoesNotSpreadCredentialFailureFromGlobalSnapshot(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount:        50,
			QualitySampleCount: 50,
			AuthFailureCount:   1,
		},
	})
	if eval.Eject || eval.HardFailure {
		t.Fatalf("unscoped credential failure must not eject every downstream: %+v", eval)
	}
}

func TestPenaltyRecoveringIsProbeOnly(t *testing.T) {
	eval := EvaluatePenalty(contracts.RouteStrategy{}, Candidate{
		Channel: chan_("a", contracts.UpstreamChannelActive),
		State:   contracts.HealthRecovering,
	})
	if !eval.Eject || eval.HardFailure || eval.Reason.Code != GateRecovering {
		t.Fatalf("recovering source must stay out of normal traffic: %+v", eval)
	}
}

func TestRankByPenaltySortsAndExplainsEjections(t *testing.T) {
	good := Candidate{
		Channel: chan_("good", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount: 50, SuccessRate: .99, ErrorRate: .01, QualitySampleCount: 50, QualitySuccessRate: .99, QualityErrorRate: .01, UpstreamErrorRate: .01, TTFTP95: 800, DurationP95: 4000,
		},
	}
	bad := Candidate{
		Channel: chan_("bad", contracts.UpstreamChannelActive),
		Snapshot: contracts.ChannelHealthSnapshot{
			SampleCount: 50, SuccessRate: .80, ErrorRate: .20, QualitySampleCount: 50, QualitySuccessRate: .80, QualityErrorRate: .20, UpstreamErrorRate: .20, TTFTP95: 4000, DurationP95: 20000,
		},
	}
	fresh := Candidate{Channel: chan_("fresh", contracts.UpstreamChannelActive)}
	ranking := RankByPenalty(contracts.RouteStrategy{}, []Candidate{bad, good, fresh})
	if len(ranking.Eligible) != 2 || ranking.Eligible[0].ChannelID != "fresh" || ranking.Eligible[0].Score != 100 {
		t.Fatalf("fresh source should retain its full initial score: %+v", ranking.Eligible)
	}
	if len(ranking.Excluded) != 1 || ranking.Excluded[0].ChannelID != "bad" || ranking.Excluded[0].Penalties == nil {
		t.Fatalf("bad source should be ejected with its penalty breakdown: %+v", ranking.Excluded)
	}
	if ranking.Eligible[1].Penalties == nil || len(ranking.Eligible[1].Reasons) < 2 {
		t.Fatalf("ranked source must expose deductions: %+v", ranking.Eligible[1])
	}
}

func closeTo(got, want, tolerance float64) bool {
	if got < want {
		return want-got <= tolerance
	}
	return got-want <= tolerance
}
