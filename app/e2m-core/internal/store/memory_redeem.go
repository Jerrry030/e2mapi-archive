package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

func normalizeRedeemCodeForCreate(input contracts.RedeemCode) (contracts.RedeemCode, error) {
	code := input
	if !code.Type.Valid() || strings.TrimSpace(code.CodeHash) == "" || len(code.CodeHash) > 128 {
		return contracts.RedeemCode{}, ErrInvalid
	}
	code.Currency = strings.ToUpper(strings.TrimSpace(code.Currency))
	if code.Type == contracts.RedeemCodeBalance {
		if code.AmountMicros <= 0 || !validCurrency(code.Currency) {
			return contracts.RedeemCode{}, ErrInvalid
		}
	} else if code.AmountMicros != 0 {
		return contracts.RedeemCode{}, ErrInvalid
	}
	if code.Status == "" {
		code.Status = contracts.RedeemCodeUnused
	}
	if code.Status != contracts.RedeemCodeUnused {
		return contracts.RedeemCode{}, ErrInvalid
	}
	code.BatchID = strings.TrimSpace(code.BatchID)
	code.Notes = strings.TrimSpace(code.Notes)
	if len(code.BatchID) > 128 || len(code.Notes) > 500 || len(code.CodePrefix) > 16 {
		return contracts.RedeemCode{}, ErrInvalid
	}
	return code, nil
}

func (s *MemoryStore) CreateRedeemCodes(ctx context.Context, codes []contracts.RedeemCode) ([]contracts.RedeemCode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	prepared := make([]contracts.RedeemCode, 0, len(codes))
	seen := map[string]bool{}
	for _, input := range codes {
		code, err := normalizeRedeemCodeForCreate(input)
		if err != nil {
			return nil, err
		}
		if seen[code.CodeHash] {
			return nil, ErrDuplicate
		}
		seen[code.CodeHash] = true
		for _, existing := range s.redeemCodes {
			if existing.CodeHash == code.CodeHash {
				return nil, ErrDuplicate
			}
		}
		code.ID = s.nextID("redeem")
		code.CreatedAt, code.UpdatedAt = now, now
		prepared = append(prepared, code)
	}
	s.redeemCodes = append(s.redeemCodes, prepared...)
	out := make([]contracts.RedeemCode, len(prepared))
	copy(out, prepared)
	return out, nil
}

func (s *MemoryStore) GetRedeemCodeByHash(ctx context.Context, codeHash string) (contracts.RedeemCode, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RedeemCode{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, code := range s.redeemCodes {
		if code.CodeHash == codeHash {
			return code, nil
		}
	}
	return contracts.RedeemCode{}, ErrNotFound
}

func (s *MemoryStore) ListRedeemCodes(ctx context.Context, filter contracts.RedeemCodeFilter) (contracts.RedeemCodePage, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RedeemCodePage{}, err
	}
	page, pageSize := normalizePaymentOrderPage(filter.Page, filter.PageSize)
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := []contracts.RedeemCode{}
	for _, code := range s.redeemCodes {
		if filter.Type != "" && code.Type != filter.Type {
			continue
		}
		if filter.Status != "" && code.Status != filter.Status {
			continue
		}
		if filter.BatchID != "" && code.BatchID != filter.BatchID {
			continue
		}
		matched = append(matched, code)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].CreatedAt.After(matched[j].CreatedAt) })
	total := int64(len(matched))
	start := (page - 1) * pageSize
	if start > len(matched) {
		start = len(matched)
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	items := make([]contracts.RedeemCode, end-start)
	copy(items, matched[start:end])
	return contracts.RedeemCodePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *MemoryStore) DisableRedeemCode(ctx context.Context, id string) (contracts.RedeemCode, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RedeemCode{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.redeemCodes {
		code := &s.redeemCodes[i]
		if code.ID != id {
			continue
		}
		if code.Status != contracts.RedeemCodeUnused {
			return contracts.RedeemCode{}, ErrConflict
		}
		code.Status = contracts.RedeemCodeDisabled
		code.UpdatedAt = s.now()
		return *code, nil
	}
	return contracts.RedeemCode{}, ErrNotFound
}

func (s *MemoryStore) DeleteUnusedRedeemCode(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.redeemCodes {
		code := s.redeemCodes[i]
		if code.ID != id {
			continue
		}
		if code.Status == contracts.RedeemCodeUsed {
			return ErrConflict
		}
		s.redeemCodes = append(s.redeemCodes[:i], s.redeemCodes[i+1:]...)
		return nil
	}
	return ErrNotFound
}

// RedeemBalanceCode atomically consumes an unused balance code and credits the
// user's wallet with a balanced redeem journal. An expired code is marked
// expired instead of consumed; every non-unused state returns ErrConflict so a
// concurrent double redeem has exactly one winner.
func (s *MemoryStore) RedeemBalanceCode(ctx context.Context, codeHash string, userID int64, now time.Time) (contracts.RedeemCode, contracts.Wallet, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	if userID <= 0 || strings.TrimSpace(codeHash) == "" {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userEnabled := false
	for _, user := range s.users {
		if user.ID == userID {
			userEnabled = user.Enabled
			break
		}
	}
	if !userEnabled {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrNotFound
	}
	for i := range s.redeemCodes {
		code := &s.redeemCodes[i]
		if code.CodeHash != codeHash {
			continue
		}
		if code.Type != contracts.RedeemCodeBalance {
			return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
		}
		if code.Status == contracts.RedeemCodeUnused && code.ExpiresAt != nil && !code.ExpiresAt.After(now) {
			code.Status = contracts.RedeemCodeExpired
			code.UpdatedAt = s.now()
			return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
		}
		if code.Status != contracts.RedeemCodeUnused {
			return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
		}
		stamp := s.now()
		usedAt := now.UTC()
		code.Status = contracts.RedeemCodeUsed
		code.UsedBy = userID
		code.UsedAt = &usedAt
		code.UpdatedAt = stamp
		walletKey := walletMapKey(userID, code.Currency)
		wallet := s.wallets[walletKey]
		wallet.UserID, wallet.Currency = userID, code.Currency
		wallet.AvailableMicros += code.AmountMicros
		wallet.Version++
		wallet.UpdatedAt = stamp
		s.wallets[walletKey] = wallet
		s.appendWalletJournalLocked(userID, contracts.WalletJournalRedeem, code.Currency, code.AmountMicros,
			"redeem:"+code.ID, "redeem_code", code.ID, contracts.WalletAccountPlatformCash, contracts.WalletAccountUserAvailable, stamp)
		return *code, wallet, nil
	}
	return contracts.RedeemCode{}, contracts.Wallet{}, ErrNotFound
}
