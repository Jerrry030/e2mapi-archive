package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"e2m.local/contracts"
)

const authSystemSettingsKey = "auth.registration"
const paymentConfigKey = "payment.collection"
const commerceSettingsKey = "commerce.runtime"

type authSystemSettingsPayload struct {
	RegistrationEnabled              bool     `json:"registration_enabled"`
	RegistrationEmailSuffixWhitelist []string `json:"registration_email_suffix_whitelist"`
	TurnstileEnabled                 bool     `json:"turnstile_enabled"`
	TurnstileSiteKey                 string   `json:"turnstile_site_key"`
	TurnstileSecretKey               string   `json:"turnstile_secret_key"`
}

func (s *PostgresStore) GetAuthSystemSettings(ctx context.Context) (contracts.AuthSystemSettings, error) {
	row := s.pool.QueryRow(ctx, `SELECT value, updated_at FROM system_settings WHERE key=$1`, authSystemSettingsKey)
	var raw []byte
	var updatedAt time.Time
	if err := row.Scan(&raw, &updatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.AuthSystemSettings{}, ErrNotFound
		}
		return contracts.AuthSystemSettings{}, err
	}
	var payload authSystemSettingsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return contracts.AuthSystemSettings{}, err
	}
	return contracts.AuthSystemSettings{
		RegistrationEnabled:              payload.RegistrationEnabled,
		RegistrationEmailSuffixWhitelist: append([]string(nil), payload.RegistrationEmailSuffixWhitelist...),
		TurnstileEnabled:                 payload.TurnstileEnabled,
		TurnstileSiteKey:                 payload.TurnstileSiteKey,
		TurnstileSecretConfigured:        payload.TurnstileSecretKey != "",
		TurnstileSecretKey:               payload.TurnstileSecretKey,
		UpdatedAt:                        updatedAt,
	}, nil
}

func (s *PostgresStore) UpsertAuthSystemSettings(ctx context.Context, input contracts.AuthSystemSettings) (contracts.AuthSystemSettings, error) {
	payload := authSystemSettingsPayload{
		RegistrationEnabled:              input.RegistrationEnabled,
		RegistrationEmailSuffixWhitelist: append([]string{}, input.RegistrationEmailSuffixWhitelist...),
		TurnstileEnabled:                 input.TurnstileEnabled,
		TurnstileSiteKey:                 input.TurnstileSiteKey,
		TurnstileSecretKey:               input.TurnstileSecretKey,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return contracts.AuthSystemSettings{}, err
	}
	var updatedAt time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO system_settings (key, value, updated_at)
		 VALUES ($1, $2::jsonb, now())
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()
		 RETURNING updated_at`,
		authSystemSettingsKey, string(raw)).Scan(&updatedAt)
	if err != nil {
		return contracts.AuthSystemSettings{}, err
	}
	out := copyAuthSystemSettings(input)
	out.UpdatedAt = updatedAt
	out.TurnstileSecretConfigured = out.TurnstileSecretKey != ""
	return out, nil
}

func (s *PostgresStore) GetCommerceSettings(ctx context.Context) (contracts.CommerceSettings, error) {
	row := s.pool.QueryRow(ctx, `SELECT value, updated_at FROM system_settings WHERE key=$1`, commerceSettingsKey)
	var raw []byte
	var updatedAt time.Time
	if err := row.Scan(&raw, &updatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.CommerceSettings{}, ErrNotFound
		}
		return contracts.CommerceSettings{}, err
	}
	var settings contracts.CommerceSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return contracts.CommerceSettings{}, err
	}
	settings.UpdatedAt = updatedAt
	return settings, nil
}

func (s *PostgresStore) UpsertCommerceSettings(ctx context.Context, input contracts.CommerceSettings) (contracts.CommerceSettings, error) {
	settings := input
	settings.UpdatedAt = time.Time{}
	raw, err := json.Marshal(settings)
	if err != nil {
		return contracts.CommerceSettings{}, err
	}
	var updatedAt time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO system_settings (key, value, updated_at)
		 VALUES ($1, $2::jsonb, now())
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()
		 RETURNING updated_at`,
		commerceSettingsKey, string(raw)).Scan(&updatedAt)
	if err != nil {
		return contracts.CommerceSettings{}, err
	}
	settings.UpdatedAt = updatedAt
	return settings, nil
}

func (s *PostgresStore) GetPaymentConfig(ctx context.Context) (contracts.PaymentConfig, error) {
	row := s.pool.QueryRow(ctx, `SELECT value, updated_at FROM system_settings WHERE key=$1`, paymentConfigKey)
	var raw []byte
	var updatedAt time.Time
	if err := row.Scan(&raw, &updatedAt); err != nil {
		if errors.Is(err, pgxNoRows()) {
			return contracts.PaymentConfig{}, ErrNotFound
		}
		return contracts.PaymentConfig{}, err
	}
	var config contracts.PaymentConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return contracts.PaymentConfig{}, err
	}
	config.UpdatedAt = updatedAt
	config.EnabledPaymentTypes = append([]string{}, config.EnabledPaymentTypes...)
	return config, nil
}

func (s *PostgresStore) UpsertPaymentConfig(ctx context.Context, input contracts.PaymentConfig) (contracts.PaymentConfig, error) {
	config := input
	config.UpdatedAt = time.Time{}
	raw, err := json.Marshal(config)
	if err != nil {
		return contracts.PaymentConfig{}, err
	}
	var updatedAt time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO system_settings (key, value, updated_at)
		 VALUES ($1, $2::jsonb, now())
		 ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()
		 RETURNING updated_at`,
		paymentConfigKey, string(raw)).Scan(&updatedAt)
	if err != nil {
		return contracts.PaymentConfig{}, err
	}
	config.UpdatedAt = updatedAt
	config.EnabledPaymentTypes = append([]string{}, config.EnabledPaymentTypes...)
	return config, nil
}
