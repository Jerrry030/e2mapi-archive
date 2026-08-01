package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestCreateInstanceDefaultsStatus(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))

	instance, err := s.CreateInstance(ctx, contracts.Instance{
		UserID: 101,
		Name:   "Test sub2api",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	if instance.ID == "" {
		t.Fatal("expected instance id")
	}
	if instance.Status != contracts.InstanceStatusUnknown {
		t.Fatalf("expected unknown status, got %s", instance.Status)
	}
}

func TestListInstancesScopedByUser(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))

	if _, err := s.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "A", Kind: contracts.InstanceKindSub2API}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.CreateInstance(ctx, contracts.Instance{UserID: 102, Name: "B", Kind: contracts.InstanceKindNewAPI}); err != nil {
		t.Fatalf("create: %v", err)
	}

	a, err := s.ListInstances(ctx, 101)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, in := range a {
		if in.UserID != 101 {
			t.Fatalf("user scope leaked: got instance of %d", in.UserID)
		}
	}
	if len(a) != 1 {
		t.Fatalf("expected 1 instance for user-a, got %d", len(a))
	}
}

func TestAppendAuditIsExplicit(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))

	// Creating an instance must NOT write an audit as a side effect.
	before, err := s.ListAudits(ctx, 0)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if _, err := s.CreateInstance(ctx, contracts.Instance{UserID: 101, Name: "X", Kind: contracts.InstanceKindSub2API}); err != nil {
		t.Fatalf("create: %v", err)
	}
	after, err := s.ListAudits(ctx, 0)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("create should not write an audit; before=%d after=%d", len(before), len(after))
	}

	// Audits appear only when explicitly appended.
	if _, err := s.AppendAudit(ctx, contracts.OperationAudit{UserID: 101, Action: "instance.create", RiskLevel: contracts.RiskLevelL0, Result: "accepted"}); err != nil {
		t.Fatalf("append audit: %v", err)
	}
	got, err := s.ListAudits(ctx, 101)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 user-test audit, got %d", len(got))
	}
}

func TestUserAndSupplyOffer(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))

	sup, err := s.CreateUser(ctx, contracts.User{
		Email:       "upstream-li@example.com",
		DisplayName: "Upstream Li",
		Roles:       []contracts.UserRole{contracts.UserRoleSupplier},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	offer, err := s.CreateSupplyOffer(ctx, contracts.SupplyOffer{
		SupplierUserID: sup.ID,
		Kind:           contracts.SupplyOfferOAuthSubscription,
		Provider:       "anthropic",
		CredentialRef:  "credential_ref:offer-1",
		ProxyRef:       "proxy_ref:res-1",
	})
	if err != nil {
		t.Fatalf("create supply offer: %v", err)
	}
	if offer.Status != contracts.SupplyOfferStatusPending {
		t.Fatalf("expected pending default status, got %s", offer.Status)
	}

	offers, err := s.ListSupplyOffers(ctx, sup.ID)
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(offers))
	}

	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) < 1 {
		t.Fatalf("expected created user, got %d", len(users))
	}
}

func TestAllocateSupplyOfferIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	offer, err := s.CreateSupplyOffer(ctx, contracts.SupplyOffer{
		SupplierUserID: 77,
		Kind:           contracts.SupplyOfferAPIKey,
		CredentialRef:  "credential_ref:user/77/upstream/test",
	})
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	input := contracts.SupplyLedgerEntry{
		OfferID:        offer.ID,
		SupplierUserID: offer.SupplierUserID,
		UserID:         88,
		InstanceID:     "inst-concurrent",
	}

	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.AllocateSupplyOffer(ctx, input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes, duplicates := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDuplicate):
			duplicates++
		default:
			t.Fatalf("unexpected allocate error: %v", err)
		}
	}
	if successes != 1 || duplicates != attempts-1 {
		t.Fatalf("unexpected results: successes=%d duplicates=%d", successes, duplicates)
	}
	entries, err := s.ListSupplyLedger(ctx, offer.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one ledger entry: entries=%+v err=%v", entries, err)
	}
	updated, err := s.GetSupplyOffer(ctx, offer.ID)
	if err != nil || updated.Status != contracts.SupplyOfferStatusActive {
		t.Fatalf("offer must be active in same operation: offer=%+v err=%v", updated, err)
	}
}
