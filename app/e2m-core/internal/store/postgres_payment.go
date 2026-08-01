package store

import (
	"context"
	"encoding/json"

	"e2m.local/contracts"
)

const paymentProviderColumns = `id, provider_key, name, config, secret_refs,
	supported_types, enabled, payment_mode, sort_order, limits,
	refund_enabled, allow_user_refund, created_at, updated_at`

func scanPaymentProvider(row rowScanner) (contracts.PaymentProvider, error) {
	var provider contracts.PaymentProvider
	var providerKey string
	var rawConfig, rawSecretRefs, rawTypes, rawLimits []byte
	if err := row.Scan(
		&provider.ID, &providerKey, &provider.Name, &rawConfig, &rawSecretRefs,
		&rawTypes, &provider.Enabled, &provider.PaymentMode, &provider.SortOrder, &rawLimits,
		&provider.RefundEnabled, &provider.AllowUserRefund, &provider.CreatedAt, &provider.UpdatedAt,
	); err != nil {
		return contracts.PaymentProvider{}, err
	}
	provider.ProviderKey = contracts.PaymentProviderKey(providerKey)
	provider.Config = map[string]string{}
	provider.SecretRefs = map[string]string{}
	provider.Limits = map[string]contracts.PaymentMethodLimit{}
	provider.SupportedTypes = []string{}
	if err := json.Unmarshal(rawConfig, &provider.Config); err != nil {
		return contracts.PaymentProvider{}, err
	}
	if err := json.Unmarshal(rawSecretRefs, &provider.SecretRefs); err != nil {
		return contracts.PaymentProvider{}, err
	}
	if err := json.Unmarshal(rawTypes, &provider.SupportedTypes); err != nil {
		return contracts.PaymentProvider{}, err
	}
	if err := json.Unmarshal(rawLimits, &provider.Limits); err != nil {
		return contracts.PaymentProvider{}, err
	}
	return provider, nil
}

func marshalPaymentProvider(provider contracts.PaymentProvider) ([]byte, []byte, []byte, []byte, error) {
	config, err := json.Marshal(provider.Config)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	secretRefs, err := json.Marshal(provider.SecretRefs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	types, err := json.Marshal(provider.SupportedTypes)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	limits, err := json.Marshal(provider.Limits)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return config, secretRefs, types, limits, nil
}

func (s *PostgresStore) CreatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error) {
	provider := input
	if provider.ID == "" {
		provider.ID = newID("payprov")
	}
	config, secretRefs, types, limits, err := marshalPaymentProvider(provider)
	if err != nil {
		return contracts.PaymentProvider{}, err
	}
	return scanPaymentProvider(s.pool.QueryRow(ctx,
		`INSERT INTO payment_provider_instances
		 (id, provider_key, name, config, secret_refs, supported_types, enabled, payment_mode,
		  sort_order, limits, refund_enabled, allow_user_refund)
		 VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7,$8,$9,$10::jsonb,$11,$12)
		 RETURNING `+paymentProviderColumns,
		provider.ID, string(provider.ProviderKey), provider.Name, string(config), string(secretRefs), string(types),
		provider.Enabled, provider.PaymentMode, provider.SortOrder, string(limits), provider.RefundEnabled, provider.AllowUserRefund))
}

func (s *PostgresStore) GetPaymentProvider(ctx context.Context, id string) (contracts.PaymentProvider, error) {
	provider, err := scanPaymentProvider(s.pool.QueryRow(ctx,
		`SELECT `+paymentProviderColumns+` FROM payment_provider_instances WHERE id=$1`, id))
	if err != nil {
		return contracts.PaymentProvider{}, mapNotFound(err)
	}
	return provider, nil
}

func (s *PostgresStore) ListPaymentProviders(ctx context.Context) ([]contracts.PaymentProvider, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+paymentProviderColumns+` FROM payment_provider_instances ORDER BY sort_order, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.PaymentProvider{}
	for rows.Next() {
		provider, err := scanPaymentProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdatePaymentProvider(ctx context.Context, input contracts.PaymentProvider) (contracts.PaymentProvider, error) {
	config, secretRefs, types, limits, err := marshalPaymentProvider(input)
	if err != nil {
		return contracts.PaymentProvider{}, err
	}
	provider, err := scanPaymentProvider(s.pool.QueryRow(ctx,
		`UPDATE payment_provider_instances SET
		 provider_key=$2, name=$3, config=$4::jsonb, secret_refs=$5::jsonb,
		 supported_types=$6::jsonb, enabled=$7, payment_mode=$8, sort_order=$9,
		 limits=$10::jsonb, refund_enabled=$11, allow_user_refund=$12, updated_at=now()
		 WHERE id=$1 RETURNING `+paymentProviderColumns,
		input.ID, string(input.ProviderKey), input.Name, string(config), string(secretRefs), string(types),
		input.Enabled, input.PaymentMode, input.SortOrder, string(limits), input.RefundEnabled, input.AllowUserRefund))
	if err != nil {
		return contracts.PaymentProvider{}, mapNotFound(err)
	}
	return provider, nil
}

func (s *PostgresStore) DeletePaymentProvider(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM payment_provider_instances WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
