package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryUpstreamCostBatchIsAtomicVersionedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	facts := upstreamCostTestBatch(t, 11, "usage-1", time.Date(2026, 7, 25, 2, 0, 0, 123456789, time.UTC))
	created, version, err := st.AppendUpstreamCostFacts(ctx, facts)
	if err != nil || len(created) != 4 || version.FactVersion != 1 {
		t.Fatalf("append: facts=%+v version=%+v err=%v", created, version, err)
	}
	for _, fact := range created {
		if fact.FactVersion != version.FactVersion || fact.ID == "" || fact.CreatedAt.IsZero() {
			t.Fatalf("batch did not share server identity/version: %+v", created)
		}
	}
	replayed, replayVersion, err := st.AppendUpstreamCostFacts(ctx, facts)
	if err != nil || replayVersion.FactVersion != 1 || !reflect.DeepEqual(created, replayed) {
		t.Fatalf("replay: facts=%+v version=%+v err=%v", replayed, replayVersion, err)
	}
	conflict := upstreamCostTestBatch(t, 11, "usage-1", facts[0].OccurredAt)
	amount := contracts.CanonicalDecimal("9")
	conflict[0].Amount = &amount
	if _, _, err := st.AppendUpstreamCostFacts(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v, want ErrConflict", err)
	}
	listed, err := st.ListUpstreamCostFacts(ctx, contracts.UpstreamCostFactFilter{UserID: 11})
	if err != nil || len(listed) != 4 {
		t.Fatalf("list after conflict: facts=%+v err=%v", listed, err)
	}
	second := upstreamCostTestBatch(t, 11, "usage-2", facts[0].OccurredAt.Add(time.Minute))
	_, secondVersion, err := st.AppendUpstreamCostFacts(ctx, second)
	if err != nil || secondVersion.FactVersion != 2 {
		t.Fatalf("second batch version=%+v err=%v", secondVersion, err)
	}
}

func TestMemoryUpstreamCostBatchRejectsPartialAndIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	facts := upstreamCostTestBatch(t, 11, "usage", time.Now().UTC())
	if _, _, err := st.AppendUpstreamCostFacts(ctx, facts[:3]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial batch error=%v, want ErrInvalid", err)
	}
	if listed, err := st.ListUpstreamCostFacts(ctx, contracts.UpstreamCostFactFilter{UserID: 11}); err != nil || len(listed) != 0 {
		t.Fatalf("partial batch persisted: facts=%+v err=%v", listed, err)
	}
	if _, err := st.ListUpstreamCostFacts(ctx, contracts.UpstreamCostFactFilter{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ownerless read error=%v, want ErrInvalid", err)
	}
	if _, _, err := st.AppendUpstreamCostFacts(ctx, facts); err != nil {
		t.Fatal(err)
	}
	foreign, err := st.ListUpstreamCostFacts(ctx, contracts.UpstreamCostFactFilter{UserID: 22})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-owner facts=%+v err=%v", foreign, err)
	}
}

func TestMemoryUpstreamCostReturnsDeepCopies(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore(time.Now())
	facts := upstreamCostTestBatch(t, 11, "usage-copy", time.Now().UTC())
	created, _, err := st.AppendUpstreamCostFacts(ctx, facts)
	if err != nil {
		t.Fatal(err)
	}
	*created[0].Quantity = 99
	*created[0].Amount = "99"
	created[0].MissingFields = append(created[0].MissingFields, "mutated")
	listed, err := st.ListUpstreamCostFacts(ctx, contracts.UpstreamCostFactFilter{UserID: 11})
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range listed {
		if fact.PriceDimension == contracts.UpstreamPriceInput && (*fact.Quantity == 99 || *fact.Amount == "99" || len(fact.MissingFields) != 0) {
			t.Fatalf("caller mutated stored fact: %+v", fact)
		}
	}
}

func upstreamCostTestBatch(t *testing.T, owner int64, usageID string, occurred time.Time) []contracts.UpstreamCostFact {
	t.Helper()
	amount := contracts.CanonicalDecimal("1")
	unit := contracts.CanonicalDecimal("2")
	effective := occurred.Add(-time.Hour)
	quantity := int64(500000)
	dimensions := []contracts.UpstreamPriceDimension{
		contracts.UpstreamPriceInput, contracts.UpstreamPriceOutput, contracts.UpstreamPriceCachedInput, contracts.UpstreamPriceRequest,
	}
	facts := make([]contracts.UpstreamCostFact, 0, len(dimensions))
	for _, dimension := range dimensions {
		key, err := contracts.UpstreamCostIdempotencyKey(owner, usageID, dimension, contracts.UpstreamCostCalculationVersionV1)
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, contracts.UpstreamCostFact{
			IdempotencyKey: key, UserID: owner, UsageObservationID: usageID, ChannelID: "channel-1", InstanceID: "instance-1",
			IntelligenceSourceID: "source-1", ModelKey: "model-1", GroupKey: "group-1", PriceDimension: dimension,
			Quantity: &quantity, PerTokens: 1000000, PriceObservationID: "price-" + string(dimension), PriceEffectiveAt: &effective,
			UnitCost: &unit, Amount: &amount, Currency: "USD", Attribution: contracts.UpstreamCostExact,
			PriceStatus: contracts.UpstreamCostPriceValid, CalculationVersion: contracts.UpstreamCostCalculationVersionV1,
			MissingFields: []string{}, OccurredAt: occurred,
		})
	}
	return facts
}
