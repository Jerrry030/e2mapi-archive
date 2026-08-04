// Package settings is the single access path for operator-editable runtime
// configuration. Sections live in the unified system_settings store; the
// database value is authoritative and hot-applies without a restart, while
// environment variables only seed the very first boot of a section. Runtime
// consumers (pricing, alert workers) read through this service instead of
// freezing values at startup.
package settings

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

// Store is the narrow persistence surface the service needs.
type Store interface {
	GetCommerceSettings(ctx context.Context) (contracts.CommerceSettings, error)
	UpsertCommerceSettings(ctx context.Context, input contracts.CommerceSettings) (contracts.CommerceSettings, error)
}

type Service struct {
	store Store

	mu       sync.RWMutex
	commerce contracts.CommerceSettings
}

func New(st Store) *Service {
	return &Service{store: st}
}

// LoadOrSeed reads the commerce section. When the section has never been
// written, the environment-derived seed is validated, persisted, and becomes
// the initial database value — after that the environment is ignored.
func (s *Service) LoadOrSeed(ctx context.Context, seed contracts.CommerceSettings) error {
	current, err := s.store.GetCommerceSettings(ctx)
	if err == nil {
		s.mu.Lock()
		s.commerce = current
		s.mu.Unlock()
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	normalized, validationErr := normalizeCommerce(contracts.UpdateCommerceSettingsRequest{
		USDToCNYRate:          seed.USDToCNYRate,
		BalanceAlertThreshold: seed.BalanceAlertThreshold,
	})
	if validationErr != nil {
		return validationErr
	}
	saved, err := s.store.UpsertCommerceSettings(ctx, normalized)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.commerce = saved
	s.mu.Unlock()
	return nil
}

func (s *Service) Commerce() contracts.CommerceSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commerce
}

// SetCommerce validates, persists, and hot-applies the commerce section.
func (s *Service) SetCommerce(ctx context.Context, input contracts.UpdateCommerceSettingsRequest) (contracts.CommerceSettings, error) {
	normalized, err := normalizeCommerce(input)
	if err != nil {
		return contracts.CommerceSettings{}, err
	}
	saved, err := s.store.UpsertCommerceSettings(ctx, normalized)
	if err != nil {
		return contracts.CommerceSettings{}, err
	}
	s.mu.Lock()
	s.commerce = saved
	s.mu.Unlock()
	return saved, nil
}

// USDToCNYRate returns the live conversion rate; zero means base-table
// pricing is disabled.
func (s *Service) USDToCNYRate() float64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	raw := s.commerce.USDToCNYRate
	s.mu.RUnlock()
	if raw == "" {
		return 0
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || rate <= 0 {
		return 0
	}
	return rate
}

// BalanceAlertThresholdMicros returns the live low-balance alert line; zero
// means the alert sweep is disabled.
func (s *Service) BalanceAlertThresholdMicros() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	raw := s.commerce.BalanceAlertThreshold
	s.mu.RUnlock()
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(value * 1_000_000)
}

// ValidationError distinguishes operator input mistakes from store failures.
type ValidationError struct{ Message string }

func (e ValidationError) Error() string { return e.Message }

func normalizeCommerce(input contracts.UpdateCommerceSettingsRequest) (contracts.CommerceSettings, error) {
	out := contracts.CommerceSettings{
		USDToCNYRate:          strings.TrimSpace(input.USDToCNYRate),
		BalanceAlertThreshold: strings.TrimSpace(input.BalanceAlertThreshold),
	}
	if out.USDToCNYRate != "" {
		rate, err := strconv.ParseFloat(out.USDToCNYRate, 64)
		if err != nil || rate <= 0 || rate >= 1000 || len(out.USDToCNYRate) > 12 {
			return contracts.CommerceSettings{}, ValidationError{"usd_to_cny_rate must be a positive decimal below 1000, or empty to disable base-table pricing"}
		}
	}
	if out.BalanceAlertThreshold != "" {
		threshold, err := strconv.ParseFloat(out.BalanceAlertThreshold, 64)
		if err != nil || threshold <= 0 || threshold >= 1_000_000_000 || len(out.BalanceAlertThreshold) > 15 {
			return contracts.CommerceSettings{}, ValidationError{"balance_alert_threshold must be a positive yuan amount, or empty to disable alerts"}
		}
	}
	return out, nil
}
