package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

const hybridAllocationColumns = `user_id,instance_id,basis,owner_percent,economy_percent,stable_percent,
	owner_burst_max,economy_burst_max,stable_burst_max,model_overrides,daily_budget_micros,
	max_unit_price_micros,routing_generation,version,created_at,updated_at`

func scanHybridAllocation(row rowScanner) (contracts.HybridAllocation, error) {
	var allocation contracts.HybridAllocation
	var overrides []byte
	err := row.Scan(&allocation.UserID, &allocation.InstanceID, &allocation.Basis,
		&allocation.DefaultRule.OwnerPercent, &allocation.DefaultRule.EconomyPercent, &allocation.DefaultRule.StablePercent,
		&allocation.DefaultRule.OwnerBurstMax, &allocation.DefaultRule.EconomyBurstMax, &allocation.DefaultRule.StableBurstMax,
		&overrides, &allocation.DailyBudgetMicros, &allocation.MaxUnitPriceMicros, &allocation.RoutingGeneration, &allocation.Version,
		&allocation.CreatedAt, &allocation.UpdatedAt)
	if err != nil {
		return contracts.HybridAllocation{}, err
	}
	if err := json.Unmarshal(overrides, &allocation.ModelOverrides); err != nil {
		return contracts.HybridAllocation{}, err
	}
	return allocation, nil
}

func (s *PostgresStore) GetHybridAllocation(ctx context.Context, instanceID string) (contracts.HybridAllocation, error) {
	allocation, err := scanHybridAllocation(s.pool.QueryRow(ctx, `SELECT `+hybridAllocationColumns+` FROM hybrid_allocations WHERE instance_id=$1`, strings.TrimSpace(instanceID)))
	if err != nil {
		return contracts.HybridAllocation{}, mapNotFound(err)
	}
	return allocation, nil
}

func (s *PostgresStore) UpsertHybridAllocation(ctx context.Context, input contracts.HybridAllocation, expectedVersion int64) (contracts.HybridAllocation, error) {
	input = input.Normalize()
	if expectedVersion < 0 || !contracts.ValidHybridAllocation(input) {
		return contracts.HybridAllocation{}, ErrInvalid
	}
	overrides, err := json.Marshal(input.ModelOverrides)
	if err != nil {
		return contracts.HybridAllocation{}, ErrInvalid
	}
	var saved contracts.HybridAllocation
	if expectedVersion == 0 {
		saved, err = scanHybridAllocation(s.pool.QueryRow(ctx, `INSERT INTO hybrid_allocations
			(user_id,instance_id,basis,owner_percent,economy_percent,stable_percent,owner_burst_max,economy_burst_max,stable_burst_max,model_overrides,daily_budget_micros,max_unit_price_micros,version)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,1 FROM instances WHERE id=$2 AND user_id=$1
			ON CONFLICT(instance_id) DO NOTHING RETURNING `+hybridAllocationColumns,
			input.UserID, input.InstanceID, input.Basis, input.DefaultRule.OwnerPercent, input.DefaultRule.EconomyPercent,
			input.DefaultRule.StablePercent, input.DefaultRule.OwnerBurstMax, input.DefaultRule.EconomyBurstMax,
			input.DefaultRule.StableBurstMax, string(overrides), input.DailyBudgetMicros, input.MaxUnitPriceMicros))
	} else {
		saved, err = scanHybridAllocation(s.pool.QueryRow(ctx, `UPDATE hybrid_allocations SET
			basis=$3,owner_percent=$4,economy_percent=$5,stable_percent=$6,owner_burst_max=$7,economy_burst_max=$8,
			stable_burst_max=$9,model_overrides=$10::jsonb,daily_budget_micros=$11,max_unit_price_micros=$12,
			version=version+1,updated_at=statement_timestamp()
			WHERE instance_id=$2 AND user_id=$1 AND version=$13 RETURNING `+hybridAllocationColumns,
			input.UserID, input.InstanceID, input.Basis, input.DefaultRule.OwnerPercent, input.DefaultRule.EconomyPercent,
			input.DefaultRule.StablePercent, input.DefaultRule.OwnerBurstMax, input.DefaultRule.EconomyBurstMax,
			input.DefaultRule.StableBurstMax, string(overrides), input.DailyBudgetMicros, input.MaxUnitPriceMicros, expectedVersion))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.HybridAllocation{}, ErrConflict
	}
	return saved, err
}

func scanHybridWallet(row rowScanner) (contracts.Wallet, error) {
	var wallet contracts.Wallet
	err := row.Scan(&wallet.UserID, &wallet.Currency, &wallet.AvailableMicros, &wallet.ReservedMicros, &wallet.Version, &wallet.UpdatedAt)
	return wallet, err
}

func (s *PostgresStore) GetWallet(ctx context.Context, userID int64, currency string) (contracts.Wallet, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if userID <= 0 || !validCurrency(currency) {
		return contracts.Wallet{}, ErrInvalid
	}
	wallet, err := scanHybridWallet(s.pool.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, userID, currency))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Wallet{UserID: userID, Currency: currency}, nil
	}
	return wallet, err
}

func (s *PostgresStore) ListWalletJournals(ctx context.Context, userID int64, limit int) ([]contracts.WalletJournal, error) {
	if userID <= 0 || limit < 0 {
		return nil, ErrInvalid
	}
	if limit == 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id,user_id,kind,currency,amount_micros,idempotency_key,reference_type,reference_id,created_at
		FROM wallet_journals WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.WalletJournal{}
	for rows.Next() {
		var journal contracts.WalletJournal
		if err := rows.Scan(&journal.ID, &journal.UserID, &journal.Kind, &journal.Currency, &journal.AmountMicros,
			&journal.IdempotencyKey, &journal.ReferenceType, &journal.ReferenceID, &journal.CreatedAt); err != nil {
			return nil, err
		}
		entryRows, err := s.pool.Query(ctx, `SELECT id,journal_id,account,direction,amount_micros,currency,created_at FROM wallet_entries WHERE journal_id=$1 ORDER BY id`, journal.ID)
		if err != nil {
			return nil, err
		}
		for entryRows.Next() {
			var entry contracts.WalletEntry
			if err := entryRows.Scan(&entry.ID, &entry.JournalID, &entry.Account, &entry.Direction, &entry.AmountMicros, &entry.Currency, &entry.CreatedAt); err != nil {
				entryRows.Close()
				return nil, err
			}
			journal.Entries = append(journal.Entries, entry)
		}
		entryRows.Close()
		out = append(out, journal)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AdjustWalletBalance(ctx context.Context, userID int64, currency string, deltaMicros int64, idempotencyKey, note string) (contracts.Wallet, contracts.WalletJournal, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	idempotencyKey, note = strings.TrimSpace(idempotencyKey), strings.TrimSpace(note)
	if userID <= 0 || !validCurrency(currency) || deltaMicros == 0 || idempotencyKey == "" || len(idempotencyKey) > 128 || len(note) > 500 {
		return contracts.Wallet{}, contracts.WalletJournal{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var journalID string
	err = tx.QueryRow(ctx, `SELECT id FROM wallet_journals WHERE user_id=$1 AND idempotency_key=$2`, userID, idempotencyKey).Scan(&journalID)
	if err == nil {
		wallet, walletErr := scanHybridWallet(tx.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, userID, currency))
		if walletErr != nil {
			return contracts.Wallet{}, contracts.WalletJournal{}, walletErr
		}
		journal, journalErr := scanWalletJournalTx(ctx, tx, journalID)
		return wallet, journal, journalErr
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM users WHERE id=$1`, userID).Scan(&enabled); err != nil || !enabled {
		if errors.Is(err, pgx.ErrNoRows) || err == nil {
			return contracts.Wallet{}, contracts.WalletJournal{}, ErrNotFound
		}
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	now := nowUTC()
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency,available_micros,reserved_micros,version,updated_at)
		VALUES($1,$2,0,0,1,$3) ON CONFLICT(user_id,currency) DO NOTHING`, userID, currency, now); err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	wallet, err := scanHybridWallet(tx.QueryRow(ctx, `UPDATE wallet_accounts SET available_micros=available_micros+$3,version=version+1,updated_at=$4
		WHERE user_id=$1 AND currency=$2 AND available_micros+$3>=0
		RETURNING user_id,currency,available_micros,reserved_micros,version,updated_at`, userID, currency, deltaMicros, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Wallet{}, contracts.WalletJournal{}, ErrConflict
	}
	if err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	amount := deltaMicros
	debit, credit := contracts.WalletAccountPlatformCash, contracts.WalletAccountUserAvailable
	if amount < 0 {
		amount = -amount
		debit, credit = contracts.WalletAccountUserAvailable, contracts.WalletAccountPlatformCash
	}
	if err := insertWalletJournal(ctx, tx, userID, contracts.WalletJournalAdjustment, currency, amount, idempotencyKey, "admin_balance_adjustment", note, debit, credit, now); err != nil {
		if isUniqueViolation(err) {
			return contracts.Wallet{}, contracts.WalletJournal{}, ErrConflict
		}
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	journals, err := s.ListWalletJournals(ctx, userID, 100)
	if err != nil {
		return contracts.Wallet{}, contracts.WalletJournal{}, err
	}
	for _, journal := range journals {
		if journal.IdempotencyKey == idempotencyKey {
			return wallet, journal, nil
		}
	}
	return contracts.Wallet{}, contracts.WalletJournal{}, ErrConflict
}

func scanWalletJournalTx(ctx context.Context, tx pgx.Tx, journalID string) (contracts.WalletJournal, error) {
	var journal contracts.WalletJournal
	if err := tx.QueryRow(ctx, `SELECT id,user_id,kind,currency,amount_micros,idempotency_key,reference_type,reference_id,created_at
		FROM wallet_journals WHERE id=$1`, journalID).Scan(&journal.ID, &journal.UserID, &journal.Kind, &journal.Currency,
		&journal.AmountMicros, &journal.IdempotencyKey, &journal.ReferenceType, &journal.ReferenceID, &journal.CreatedAt); err != nil {
		return contracts.WalletJournal{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id,journal_id,account,direction,amount_micros,currency,created_at FROM wallet_entries WHERE journal_id=$1 ORDER BY id`, journalID)
	if err != nil {
		return contracts.WalletJournal{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry contracts.WalletEntry
		if err := rows.Scan(&entry.ID, &entry.JournalID, &entry.Account, &entry.Direction, &entry.AmountMicros, &entry.Currency, &entry.CreatedAt); err != nil {
			return contracts.WalletJournal{}, err
		}
		journal.Entries = append(journal.Entries, entry)
	}
	return journal, rows.Err()
}

const virtualKeyColumns = `id,user_id,group_id,instance_id,name,resource_class,prefix,token_hash,secret_ref,key_version,enabled,models,daily_limit_micros,expires_at,last_used_at,created_at,updated_at`

func scanVirtualKey(row rowScanner) (contracts.VirtualKey, error) {
	var key contracts.VirtualKey
	var models []byte
	var groupID, instanceID sql.NullString
	err := row.Scan(&key.ID, &key.UserID, &groupID, &instanceID, &key.Name, &key.ResourceClass, &key.Prefix, &key.TokenHash,
		&key.SecretRef, &key.KeyVersion, &key.Enabled, &models, &key.DailyLimitMicros, &key.ExpiresAt, &key.LastUsedAt, &key.CreatedAt, &key.UpdatedAt)
	if err != nil {
		return contracts.VirtualKey{}, err
	}
	if err := json.Unmarshal(models, &key.Models); err != nil {
		return contracts.VirtualKey{}, err
	}
	if groupID.Valid {
		key.GroupID = groupID.String
	}
	if instanceID.Valid {
		key.InstanceID = instanceID.String
	}
	return key, nil
}

func (s *PostgresStore) CreateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.GroupID = strings.TrimSpace(input.GroupID)
	if !input.Valid() || strings.TrimSpace(input.TokenHash) == "" || strings.TrimSpace(input.SecretRef) == "" {
		return contracts.VirtualKey{}, ErrInvalid
	}
	if input.ID == "" {
		input.ID = newID("vkey")
	}
	if input.KeyVersion == 0 {
		input.KeyVersion = 1
	}
	input.Enabled = true
	key, err := scanVirtualKey(s.pool.QueryRow(ctx, `INSERT INTO virtual_keys
		(id,user_id,group_id,instance_id,name,resource_class,prefix,token_hash,secret_ref,key_version,enabled,models,daily_limit_micros,expires_at)
		SELECT $1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14
		WHERE EXISTS(SELECT 1 FROM users WHERE id=$2 AND enabled AND ('client'=ANY(roles) OR 'admin'=ANY(roles)))
		  AND (($3<>'' AND $4='' AND EXISTS(SELECT 1 FROM upstream_pools WHERE id=$3 AND delivery_mode='supply_gateway' AND status<>'retired'))
		    OR ($3='' AND $4<>'' AND EXISTS(SELECT 1 FROM instances WHERE id=$4 AND user_id=$2)))
		RETURNING `+virtualKeyColumns, input.ID, input.UserID, input.GroupID, input.InstanceID, input.Name, string(input.ResourceClass), input.Prefix,
		input.TokenHash, input.SecretRef, input.KeyVersion, input.Enabled, string(marshalStrings(input.Models)), input.DailyLimitMicros, input.ExpiresAt))
	if isUniqueViolation(err) {
		return contracts.VirtualKey{}, ErrDuplicate
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.VirtualKey{}, ErrInvalid
	}
	return key, err
}

func (s *PostgresStore) GetVirtualKey(ctx context.Context, userID int64, id string) (contracts.VirtualKey, error) {
	key, err := scanVirtualKey(s.pool.QueryRow(ctx, `SELECT `+virtualKeyColumns+` FROM virtual_keys WHERE id=$1 AND user_id=$2`, id, userID))
	if err != nil {
		return contracts.VirtualKey{}, mapNotFound(err)
	}
	return key, nil
}

func (s *PostgresStore) GetVirtualKeyByHash(ctx context.Context, tokenHash string) (contracts.VirtualKey, error) {
	key, err := scanVirtualKey(s.pool.QueryRow(ctx, `SELECT `+virtualKeyColumns+` FROM virtual_keys WHERE token_hash=$1`, tokenHash))
	if err != nil {
		return contracts.VirtualKey{}, mapNotFound(err)
	}
	return key, nil
}

func (s *PostgresStore) ListVirtualKeys(ctx context.Context, userID int64) ([]contracts.VirtualKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+virtualKeyColumns+` FROM virtual_keys WHERE user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.VirtualKey{}
	for rows.Next() {
		key, err := scanVirtualKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateVirtualKey(ctx context.Context, input contracts.VirtualKey) (contracts.VirtualKey, error) {
	if !input.Valid() {
		return contracts.VirtualKey{}, ErrInvalid
	}
	key, err := scanVirtualKey(s.pool.QueryRow(ctx, `UPDATE virtual_keys SET name=$3,enabled=$4,models=$5::jsonb,daily_limit_micros=$6,expires_at=$7,updated_at=statement_timestamp()
		WHERE id=$1 AND user_id=$2 AND group_id IS NOT DISTINCT FROM NULLIF($8,'') AND instance_id IS NOT DISTINCT FROM NULLIF($9,'') AND resource_class=$10 AND token_hash=$11 AND secret_ref=$12 AND key_version=$13 RETURNING `+virtualKeyColumns,
		input.ID, input.UserID, strings.TrimSpace(input.Name), input.Enabled, string(marshalStrings(input.Models)), input.DailyLimitMicros,
		input.ExpiresAt, input.GroupID, input.InstanceID, string(input.ResourceClass), input.TokenHash, input.SecretRef, input.KeyVersion))
	if isUniqueViolation(err) {
		return contracts.VirtualKey{}, ErrDuplicate
	}
	if err != nil {
		return contracts.VirtualKey{}, mapNotFound(err)
	}
	return key, nil
}

func (s *PostgresStore) DeleteVirtualKey(ctx context.Context, userID int64, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM virtual_keys WHERE id=$1 AND user_id=$2 AND NOT EXISTS
		(SELECT 1 FROM wallet_reservations WHERE virtual_key_id=$1 AND status='active')`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

const supplyEndpointColumns = `channel_id,base_url,allow_insecure,secret_ref,masked_value,currency,input_price_micros_per_million,
	output_price_micros_per_million,input_supplier_micros_per_million,output_supplier_micros_per_million,
	max_request_micros,max_concurrency,capacity_percent,enabled,created_at,updated_at`

func scanSupplyEndpoint(row rowScanner) (contracts.SupplyChannelEndpoint, error) {
	var endpoint contracts.SupplyChannelEndpoint
	err := row.Scan(&endpoint.ChannelID, &endpoint.BaseURL, &endpoint.AllowInsecure, &endpoint.SecretRef, &endpoint.MaskedValue, &endpoint.Currency,
		&endpoint.InputPriceMicrosPerMillion, &endpoint.OutputPriceMicrosPerMillion, &endpoint.InputSupplierMicrosPerMillion,
		&endpoint.OutputSupplierMicrosPerMillion, &endpoint.MaxRequestMicros, &endpoint.MaxConcurrency, &endpoint.CapacityPercent,
		&endpoint.Enabled, &endpoint.CreatedAt, &endpoint.UpdatedAt)
	return endpoint, err
}

func (s *PostgresStore) UpsertSupplyChannelEndpoint(ctx context.Context, input contracts.SupplyChannelEndpoint) (contracts.SupplyChannelEndpoint, error) {
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if !input.Valid() || strings.TrimSpace(input.SecretRef) == "" {
		return contracts.SupplyChannelEndpoint{}, ErrInvalid
	}
	endpoint, err := scanSupplyEndpoint(s.pool.QueryRow(ctx, `INSERT INTO supply_channel_endpoints
		(channel_id,base_url,allow_insecure,secret_ref,masked_value,currency,input_price_micros_per_million,output_price_micros_per_million,
		 input_supplier_micros_per_million,output_supplier_micros_per_million,max_request_micros,max_concurrency,capacity_percent,enabled)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14 FROM upstream_channels c JOIN upstream_pools p ON p.id=c.pool_id
		WHERE c.id=$1 AND p.delivery_mode='supply_gateway'
		ON CONFLICT(channel_id) DO UPDATE SET base_url=EXCLUDED.base_url,allow_insecure=EXCLUDED.allow_insecure,secret_ref=EXCLUDED.secret_ref,masked_value=EXCLUDED.masked_value,
		currency=EXCLUDED.currency,input_price_micros_per_million=EXCLUDED.input_price_micros_per_million,
		output_price_micros_per_million=EXCLUDED.output_price_micros_per_million,
		input_supplier_micros_per_million=EXCLUDED.input_supplier_micros_per_million,
		output_supplier_micros_per_million=EXCLUDED.output_supplier_micros_per_million,max_request_micros=EXCLUDED.max_request_micros,
		max_concurrency=EXCLUDED.max_concurrency,capacity_percent=EXCLUDED.capacity_percent,enabled=EXCLUDED.enabled,updated_at=statement_timestamp()
		RETURNING `+supplyEndpointColumns, input.ChannelID, input.BaseURL, input.AllowInsecure, input.SecretRef, input.MaskedValue, input.Currency,
		input.InputPriceMicrosPerMillion, input.OutputPriceMicrosPerMillion, input.InputSupplierMicrosPerMillion,
		input.OutputSupplierMicrosPerMillion, input.MaxRequestMicros, input.MaxConcurrency, input.CapacityPercent, input.Enabled))
	if isUniqueViolation(err) {
		return contracts.SupplyChannelEndpoint{}, ErrDuplicate
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.SupplyChannelEndpoint{}, ErrInvalid
	}
	return endpoint, err
}

func (s *PostgresStore) GetSupplyChannelEndpoint(ctx context.Context, channelID string) (contracts.SupplyChannelEndpoint, error) {
	endpoint, err := scanSupplyEndpoint(s.pool.QueryRow(ctx, `SELECT `+supplyEndpointColumns+` FROM supply_channel_endpoints WHERE channel_id=$1`, channelID))
	if err != nil {
		return contracts.SupplyChannelEndpoint{}, mapNotFound(err)
	}
	return endpoint, nil
}

func (s *PostgresStore) ListSupplyCandidates(ctx context.Context, class contracts.ResourceClass, model string) ([]contracts.SupplyCandidate, error) {
	model = strings.TrimSpace(model)
	if !class.IsPlatformSupply() || model == "" {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT
		p.id,p.resource_class,p.delivery_mode,p.name,p.provider,p.models,p.region,p.status,p.description,p.labels,p.safety_stock_threshold,p.created_at,p.updated_at,
		c.id,c.pool_id,c.source_id,c.account_ownership,c.display_name,c.provider,c.models,c.probe_capability,c.probe_endpoint_path,c.groups,c.credential_binding_id,c.proxy_binding_id,c.priority,c.weight,c.cost_hint,c.status,c.inventory_state,c.labels,c.created_at,c.updated_at,
		e.channel_id,e.base_url,e.allow_insecure,e.secret_ref,e.masked_value,e.currency,e.input_price_micros_per_million,e.output_price_micros_per_million,e.input_supplier_micros_per_million,e.output_supplier_micros_per_million,e.max_request_micros,e.max_concurrency,e.capacity_percent,e.enabled,e.created_at,e.updated_at,
		(SELECT count(*) FROM wallet_reservations r WHERE r.channel_id=c.id AND r.status='active') AS active_reservations
		FROM upstream_pools p JOIN upstream_channels c ON c.pool_id=p.id JOIN supply_channel_endpoints e ON e.channel_id=c.id
		WHERE p.resource_class=$1 AND p.delivery_mode='supply_gateway' AND p.status='active' AND c.status='active'
		AND c.inventory_state IN ('','ready') AND e.enabled AND e.capacity_percent>0
		AND (p.models='[]'::jsonb OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(p.models) m(value) WHERE lower(m.value)=lower($2)))
		AND (c.models='[]'::jsonb OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.models) m(value) WHERE lower(m.value)=lower($2)))
		AND (e.max_concurrency=0 OR (SELECT count(*) FROM wallet_reservations r WHERE r.channel_id=c.id AND r.status='active') < e.max_concurrency)
		ORDER BY active_reservations,c.id`, string(class), model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contracts.SupplyCandidate{}
	for rows.Next() {
		candidate, err := scanSupplyCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetSupplyDailyUsage(ctx context.Context, userID int64, instanceID, virtualKeyID, currency string) (contracts.SupplyDailyUsage, error) {
	instanceID, virtualKeyID = strings.TrimSpace(instanceID), strings.TrimSpace(virtualKeyID)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if userID <= 0 || instanceID == "" || virtualKeyID == "" || !validCurrency(currency) {
		return contracts.SupplyDailyUsage{}, ErrInvalid
	}
	var result contracts.SupplyDailyUsage
	result.UserID, result.InstanceID, result.VirtualKeyID, result.Currency = userID, instanceID, virtualKeyID, currency
	err := s.pool.QueryRow(ctx, `WITH owned AS (
		SELECT 1 FROM virtual_keys WHERE id=$3 AND user_id=$1 AND instance_id=$2
	), daily AS (
		SELECT usage.virtual_key_id,usage.reserved_micros
		FROM supply_usage_records usage
		JOIN wallet_reservations reservation ON reservation.id=usage.reservation_id
		WHERE usage.user_id=$1 AND usage.instance_id=$2 AND reservation.currency=$4
		  AND usage.created_at >= date_trunc('day',statement_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
		  AND usage.status<>'released'
	)
	SELECT date_trunc('day',statement_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
	       COALESCE(sum(daily.reserved_micros),0),
	       COALESCE(sum(daily.reserved_micros) FILTER (WHERE daily.virtual_key_id=$3),0)
	FROM owned LEFT JOIN daily ON TRUE
	GROUP BY owned.*`, userID, instanceID, virtualKeyID, currency).Scan(
		&result.DayStart, &result.InstanceReservedMicros, &result.KeyReservedMicros)
	if err != nil {
		return contracts.SupplyDailyUsage{}, mapNotFound(err)
	}
	return result, nil
}

func (s *PostgresStore) ListSupplyUsage(ctx context.Context, filter contracts.SupplyUsageFilter) ([]contracts.SupplyUsageRecord, error) {
	filter.GroupID = strings.TrimSpace(filter.GroupID)
	filter.VirtualKeyID = strings.TrimSpace(filter.VirtualKeyID)
	if filter.UserID < 0 || filter.Limit < 0 || filter.Status != "" && filter.Status != contracts.SupplyUsageReserved &&
		filter.Status != contracts.SupplyUsageSettled && filter.Status != contracts.SupplyUsageReleased {
		return nil, ErrInvalid
	}
	if filter.Limit == 0 || filter.Limit > 200 {
		filter.Limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT `+supplyUsageColumns+` FROM supply_usage_records
		WHERE ($1=0 OR user_id=$1) AND ($2='' OR group_id=$2) AND ($3='' OR virtual_key_id=$3) AND ($4='' OR status=$4)
		ORDER BY created_at DESC,id DESC LIMIT $5`, filter.UserID, filter.GroupID, filter.VirtualKeyID, string(filter.Status), filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contracts.SupplyUsageRecord, 0)
	for rows.Next() {
		usage, scanErr := scanSupplyUsage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, usage)
	}
	return out, rows.Err()
}

func scanSupplyCandidate(row rowScanner) (contracts.SupplyCandidate, error) {
	var candidate contracts.SupplyCandidate
	var poolClass, deliveryMode, poolStatus, ownership, channelStatus, inventoryState string
	var poolModels, poolLabels, channelModels, channelGroups, channelLabels []byte
	err := row.Scan(
		&candidate.Pool.ID, &poolClass, &deliveryMode, &candidate.Pool.Name, &candidate.Pool.Provider, &poolModels,
		&candidate.Pool.Region, &poolStatus, &candidate.Pool.Description, &poolLabels, &candidate.Pool.SafetyStockThreshold,
		&candidate.Pool.CreatedAt, &candidate.Pool.UpdatedAt,
		&candidate.Channel.ID, &candidate.Channel.PoolID, &candidate.Channel.SourceID, &ownership, &candidate.Channel.DisplayName,
		&candidate.Channel.Provider, &channelModels, &candidate.Channel.ProbeCapability, &candidate.Channel.ProbeEndpointPath,
		&channelGroups, &candidate.Channel.CredentialBindingID, &candidate.Channel.ProxyBindingID, &candidate.Channel.Priority,
		&candidate.Channel.Weight, &candidate.Channel.CostHint, &channelStatus, &inventoryState, &channelLabels,
		&candidate.Channel.CreatedAt, &candidate.Channel.UpdatedAt,
		&candidate.Endpoint.ChannelID, &candidate.Endpoint.BaseURL, &candidate.Endpoint.AllowInsecure, &candidate.Endpoint.SecretRef, &candidate.Endpoint.MaskedValue,
		&candidate.Endpoint.Currency, &candidate.Endpoint.InputPriceMicrosPerMillion, &candidate.Endpoint.OutputPriceMicrosPerMillion,
		&candidate.Endpoint.InputSupplierMicrosPerMillion, &candidate.Endpoint.OutputSupplierMicrosPerMillion,
		&candidate.Endpoint.MaxRequestMicros, &candidate.Endpoint.MaxConcurrency, &candidate.Endpoint.CapacityPercent,
		&candidate.Endpoint.Enabled, &candidate.Endpoint.CreatedAt, &candidate.Endpoint.UpdatedAt, &candidate.Active,
	)
	if err != nil {
		return contracts.SupplyCandidate{}, err
	}
	candidate.Pool.ResourceClass = contracts.ResourceClass(poolClass)
	candidate.Pool.DeliveryMode = contracts.UpstreamDeliveryMode(deliveryMode)
	candidate.Pool.Status = contracts.UpstreamPoolStatus(poolStatus)
	candidate.Pool.Models = unmarshalStrings(poolModels)
	candidate.Pool.Labels = unmarshalLabels(poolLabels)
	candidate.Channel.AccountOwnership = contracts.GatewayAccountOwnership(ownership).Normalize()
	candidate.Channel.Status = contracts.UpstreamChannelStatus(channelStatus)
	candidate.Channel.InventoryState = contracts.UpstreamInventoryState(inventoryState)
	candidate.Channel.Models = unmarshalStrings(channelModels)
	candidate.Channel.Groups = unmarshalStrings(channelGroups)
	candidate.Channel.Labels = unmarshalLabels(channelLabels)
	return candidate, nil
}

const reservationColumns = `id,user_id,virtual_key_id,channel_id,request_id,currency,reserved_micros,settled_micros,status,created_at,updated_at`
const supplyUsageColumns = `id,request_id,reservation_id,user_id,group_id,instance_id,virtual_key_id,resource_class,channel_id,model,
	input_price_micros_per_million,output_price_micros_per_million,input_supplier_micros_per_million,output_supplier_micros_per_million,
	prompt_tokens,completion_tokens,reserved_micros,settled_micros,status,settlement_reason,created_at,completed_at`

func scanWalletReservation(row rowScanner) (contracts.WalletReservation, error) {
	var reservation contracts.WalletReservation
	err := row.Scan(&reservation.ID, &reservation.UserID, &reservation.VirtualKeyID, &reservation.ChannelID, &reservation.RequestID,
		&reservation.Currency, &reservation.ReservedMicros, &reservation.SettledMicros, &reservation.Status,
		&reservation.CreatedAt, &reservation.UpdatedAt)
	return reservation, err
}

func scanSupplyUsage(row rowScanner) (contracts.SupplyUsageRecord, error) {
	var usage contracts.SupplyUsageRecord
	var groupID, instanceID sql.NullString
	err := row.Scan(&usage.ID, &usage.RequestID, &usage.ReservationID, &usage.UserID, &groupID, &instanceID, &usage.VirtualKeyID,
		&usage.ResourceClass, &usage.ChannelID, &usage.Model, &usage.InputPriceMicrosPerMillion, &usage.OutputPriceMicrosPerMillion,
		&usage.InputSupplierMicrosPerMillion, &usage.OutputSupplierMicrosPerMillion, &usage.PromptTokens, &usage.CompletionTokens,
		&usage.ReservedMicros, &usage.SettledMicros, &usage.Status, &usage.SettlementReason, &usage.CreatedAt, &usage.CompletedAt)
	if err != nil {
		return contracts.SupplyUsageRecord{}, err
	}
	if groupID.Valid {
		usage.GroupID = groupID.String
	}
	if instanceID.Valid {
		usage.InstanceID = instanceID.String
	}
	return usage, nil
}

func insertWalletJournal(ctx context.Context, tx pgx.Tx, userID int64, kind contracts.WalletJournalKind, currency string, amount int64, idempotencyKey, referenceType, referenceID string, debit, credit contracts.WalletAccountCode, now time.Time) error {
	if amount <= 0 {
		return nil
	}
	journalID := newID("wjnl")
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_journals(id,user_id,kind,currency,amount_micros,idempotency_key,reference_type,reference_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, journalID, userID, string(kind), currency, amount, idempotencyKey, referenceType, referenceID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO wallet_entries(id,journal_id,account,direction,amount_micros,currency,created_at) VALUES
		($1,$3,$4,'debit',$6,$7,$8),($2,$3,$5,'credit',$6,$7,$8)`, newID("went"), newID("went"), journalID, string(debit), string(credit), amount, currency, now)
	return err
}

func insertSupplySettlementJournal(ctx context.Context, tx pgx.Tx, userID int64, currency string, charged, supplier int64, idempotencyKey, referenceType, referenceID string, now time.Time) error {
	if charged <= 0 || supplier < 0 || supplier > charged {
		if charged == 0 && supplier == 0 {
			return nil
		}
		return ErrInvalid
	}
	journalID := newID("wjnl")
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_journals(id,user_id,kind,currency,amount_micros,idempotency_key,reference_type,reference_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, journalID, userID, string(contracts.WalletJournalSettle), currency, charged, idempotencyKey, referenceType, referenceID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_entries(id,journal_id,account,direction,amount_micros,currency,created_at)
		VALUES($1,$2,$3,'debit',$4,$5,$6)`, newID("went"), journalID, string(contracts.WalletAccountUserReserved), charged, currency, now); err != nil {
		return err
	}
	if supplier > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_entries(id,journal_id,account,direction,amount_micros,currency,created_at)
			VALUES($1,$2,$3,'credit',$4,$5,$6)`, newID("went"), journalID, string(contracts.WalletAccountUpstreamPayable), supplier, currency, now); err != nil {
			return err
		}
	}
	margin := charged - supplier
	if margin > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_entries(id,journal_id,account,direction,amount_micros,currency,created_at)
			VALUES($1,$2,$3,'credit',$4,$5,$6)`, newID("went"), journalID, string(contracts.WalletAccountPlatformRevenue), margin, currency, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ReserveSupplyRequest(ctx context.Context, tokenHash, requestID, model, currency string, excludedChannelIDs []string) (contracts.SupplyReservationResult, error) {
	tokenHash, requestID, model = strings.TrimSpace(tokenHash), strings.TrimSpace(requestID), strings.TrimSpace(model)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if tokenHash == "" || requestID == "" || model == "" || !validCurrency(currency) {
		return contracts.SupplyReservationResult{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	key, err := scanVirtualKey(tx.QueryRow(ctx, `SELECT `+virtualKeyColumns+` FROM virtual_keys WHERE token_hash=$1 FOR UPDATE`, tokenHash))
	if err != nil {
		return contracts.SupplyReservationResult{}, mapNotFound(err)
	}
	if !key.Enabled || key.ExpiresAt != nil && !nowUTC().Before(*key.ExpiresAt) || !containsModel(key.Models, model) {
		return contracts.SupplyReservationResult{}, ErrNotFound
	}
	var userEligible bool
	var platformConcurrency, platformRPM int
	if err := tx.QueryRow(ctx, `SELECT enabled AND ('client'=ANY(roles) OR 'admin'=ANY(roles)), platform_concurrency, platform_rpm FROM users WHERE id=$1`, key.UserID).Scan(&userEligible, &platformConcurrency, &platformRPM); err != nil {
		return contracts.SupplyReservationResult{}, mapNotFound(err)
	}
	if !userEligible {
		return contracts.SupplyReservationResult{}, ErrNotFound
	}
	reservation, err := scanWalletReservation(tx.QueryRow(ctx, `SELECT `+reservationColumns+` FROM wallet_reservations WHERE virtual_key_id=$1 AND request_id=$2`, key.ID, requestID))
	if err == nil {
		usage, usageErr := scanSupplyUsage(tx.QueryRow(ctx, `SELECT `+supplyUsageColumns+` FROM supply_usage_records WHERE reservation_id=$1`, reservation.ID))
		if usageErr != nil {
			return contracts.SupplyReservationResult{}, usageErr
		}
		candidate, candidateErr := getSupplyCandidateTx(ctx, tx, key.ResourceClass, model, key.GroupID, reservation.ChannelID, nil, false)
		if candidateErr != nil {
			return contracts.SupplyReservationResult{}, candidateErr
		}
		wallet, walletErr := scanHybridWallet(tx.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2`, key.UserID, currency))
		if walletErr != nil {
			return contracts.SupplyReservationResult{}, walletErr
		}
		return contracts.SupplyReservationResult{Key: key, Candidate: candidate, Wallet: wallet, Reservation: reservation, Usage: usage}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.SupplyReservationResult{}, err
	}
	// Per-user platform throttles run after the idempotent-replay branch so a
	// retried request never counts against itself.
	if platformConcurrency > 0 {
		var active int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_reservations WHERE user_id=$1 AND status=$2`,
			key.UserID, string(contracts.WalletReservationActive)).Scan(&active); err != nil {
			return contracts.SupplyReservationResult{}, err
		}
		if active >= platformConcurrency {
			return contracts.SupplyReservationResult{}, ErrRateLimited
		}
	}
	if platformRPM > 0 {
		var recent int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM supply_usage_records WHERE user_id=$1 AND created_at >= statement_timestamp() - interval '1 minute'`,
			key.UserID).Scan(&recent); err != nil {
			return contracts.SupplyReservationResult{}, err
		}
		if recent >= platformRPM {
			return contracts.SupplyReservationResult{}, ErrRateLimited
		}
	}
	excluded := make([]string, 0, len(excludedChannelIDs))
	for _, channelID := range excludedChannelIDs {
		if channelID = strings.TrimSpace(channelID); channelID != "" {
			excluded = append(excluded, channelID)
		}
	}
	candidate, err := getSupplyCandidateTx(ctx, tx, key.ResourceClass, model, key.GroupID, "", excluded, true)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return contracts.SupplyReservationResult{}, ErrNoSupply
		}
		return contracts.SupplyReservationResult{}, err
	}
	if candidate.Endpoint.Currency != currency {
		return contracts.SupplyReservationResult{}, ErrInvalid
	}
	var dailyBudget, maxUnitPrice int64
	if key.InstanceID != "" {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(daily_budget_micros,0),COALESCE(max_unit_price_micros,0) FROM hybrid_allocations WHERE instance_id=$1`, key.InstanceID).Scan(&dailyBudget, &maxUnitPrice); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return contracts.SupplyReservationResult{}, err
		}
	}
	reserve := candidate.Endpoint.MaxRequestMicros
	if maxUnitPrice > 0 && reserve > maxUnitPrice {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	var instanceSpentToday, keySpentToday int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(usage.reserved_micros),0),
		COALESCE(sum(usage.reserved_micros) FILTER (WHERE usage.virtual_key_id=$3),0)
		FROM supply_usage_records usage JOIN wallet_reservations reservation ON reservation.id=usage.reservation_id
		WHERE usage.user_id=$1 AND (($2<>'' AND usage.instance_id=$2) OR ($5<>'' AND usage.group_id=$5)) AND reservation.currency=$4
		  AND usage.created_at >= date_trunc('day',statement_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
		  AND usage.status<>'released'`, key.UserID, key.InstanceID, key.ID, currency, key.GroupID).Scan(&instanceSpentToday, &keySpentToday); err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	if dailyBudget > 0 && instanceSpentToday+reserve > dailyBudget ||
		key.DailyLimitMicros > 0 && keySpentToday+reserve > key.DailyLimitMicros {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	wallet, err := scanHybridWallet(tx.QueryRow(ctx, `UPDATE wallet_accounts SET available_micros=available_micros-$3,
		reserved_micros=reserved_micros+$3,version=version+1,updated_at=statement_timestamp()
		WHERE user_id=$1 AND currency=$2 AND available_micros >= $3 RETURNING user_id,currency,available_micros,reserved_micros,version,updated_at`, key.UserID, currency, reserve))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.SupplyReservationResult{}, ErrConflict
	}
	if err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	now := nowUTC()
	reservation = contracts.WalletReservation{ID: newID("wres"), UserID: key.UserID, VirtualKeyID: key.ID, ChannelID: candidate.Channel.ID, RequestID: requestID, Currency: currency, ReservedMicros: reserve, Status: contracts.WalletReservationActive, CreatedAt: now, UpdatedAt: now}
	reservation, err = scanWalletReservation(tx.QueryRow(ctx, `INSERT INTO wallet_reservations(id,user_id,virtual_key_id,channel_id,request_id,currency,reserved_micros,status,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'active',$8,$8) RETURNING `+reservationColumns,
		reservation.ID, reservation.UserID, reservation.VirtualKeyID, reservation.ChannelID, reservation.RequestID, reservation.Currency, reservation.ReservedMicros, now))
	if err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	usage := contracts.SupplyUsageRecord{ID: newID("usage"), RequestID: requestID, ReservationID: reservation.ID, UserID: key.UserID, GroupID: key.GroupID, InstanceID: key.InstanceID, VirtualKeyID: key.ID, ResourceClass: key.ResourceClass, ChannelID: candidate.Channel.ID, Model: model,
		InputPriceMicrosPerMillion: candidate.Endpoint.InputPriceMicrosPerMillion, OutputPriceMicrosPerMillion: candidate.Endpoint.OutputPriceMicrosPerMillion,
		InputSupplierMicrosPerMillion: candidate.Endpoint.InputSupplierMicrosPerMillion, OutputSupplierMicrosPerMillion: candidate.Endpoint.OutputSupplierMicrosPerMillion,
		ReservedMicros: reserve, Status: contracts.SupplyUsageReserved, CreatedAt: now}
	usage, err = scanSupplyUsage(tx.QueryRow(ctx, `INSERT INTO supply_usage_records(id,request_id,reservation_id,user_id,group_id,instance_id,virtual_key_id,resource_class,channel_id,model,
		input_price_micros_per_million,output_price_micros_per_million,input_supplier_micros_per_million,output_supplier_micros_per_million,reserved_micros,status,created_at)
		VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,'reserved',$16) RETURNING `+supplyUsageColumns,
		usage.ID, usage.RequestID, usage.ReservationID, usage.UserID, usage.GroupID, usage.InstanceID, usage.VirtualKeyID, string(usage.ResourceClass), usage.ChannelID, usage.Model,
		usage.InputPriceMicrosPerMillion, usage.OutputPriceMicrosPerMillion, usage.InputSupplierMicrosPerMillion, usage.OutputSupplierMicrosPerMillion, usage.ReservedMicros, now))
	if err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	if err := insertWalletJournal(ctx, tx, key.UserID, contracts.WalletJournalReserve, currency, reserve, "reserve:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserAvailable, contracts.WalletAccountUserReserved, now); err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE virtual_keys SET last_used_at=$2,updated_at=$2 WHERE id=$1`, key.ID, now); err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	key.LastUsedAt = &now
	key.UpdatedAt = now
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplyReservationResult{}, err
	}
	return contracts.SupplyReservationResult{Key: key, Candidate: candidate, Wallet: wallet, Reservation: reservation, Usage: usage}, nil
}

func getSupplyCandidateTx(ctx context.Context, tx pgx.Tx, class contracts.ResourceClass, model, groupID, channelID string, excludedChannelIDs []string, lock bool) (contracts.SupplyCandidate, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF c,e SKIP LOCKED"
	}
	row := tx.QueryRow(ctx, `SELECT
		p.id,p.resource_class,p.delivery_mode,p.name,p.provider,p.models,p.region,p.status,p.description,p.labels,p.safety_stock_threshold,p.created_at,p.updated_at,
		c.id,c.pool_id,c.source_id,c.account_ownership,c.display_name,c.provider,c.models,c.probe_capability,c.probe_endpoint_path,c.groups,c.credential_binding_id,c.proxy_binding_id,c.priority,c.weight,c.cost_hint,c.status,c.inventory_state,c.labels,c.created_at,c.updated_at,
		e.channel_id,e.base_url,e.allow_insecure,e.secret_ref,e.masked_value,e.currency,e.input_price_micros_per_million,e.output_price_micros_per_million,e.input_supplier_micros_per_million,e.output_supplier_micros_per_million,e.max_request_micros,e.max_concurrency,e.capacity_percent,e.enabled,e.created_at,e.updated_at,
		(SELECT count(*) FROM wallet_reservations r WHERE r.channel_id=c.id AND r.status='active') AS active_reservations
		FROM upstream_pools p JOIN upstream_channels c ON c.pool_id=p.id JOIN supply_channel_endpoints e ON e.channel_id=c.id
		WHERE p.resource_class=$1 AND p.delivery_mode='supply_gateway' AND p.status='active' AND c.status='active' AND c.inventory_state='ready'
		AND e.enabled AND e.capacity_percent>0 AND ($3='' OR p.id=$3) AND ($4='' OR c.id=$4) AND NOT (c.id=ANY($5::text[]))
		AND (p.models='[]'::jsonb OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(p.models) m(value) WHERE lower(m.value)=lower($2)))
		AND (c.models='[]'::jsonb OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.models) m(value) WHERE lower(m.value)=lower($2)))
		AND (e.max_concurrency=0 OR (SELECT count(*) FROM wallet_reservations r WHERE r.channel_id=c.id AND r.status='active') < e.max_concurrency)
		ORDER BY c.priority ASC,active_reservations ASC,c.weight DESC,c.id LIMIT 1`+lockClause, string(class), model, groupID, channelID, excludedChannelIDs)
	candidate, err := scanSupplyCandidate(row)
	if err != nil {
		return contracts.SupplyCandidate{}, mapNotFound(err)
	}
	return candidate, nil
}

func (s *PostgresStore) SettleSupplyRequest(ctx context.Context, reservationID string, promptTokens, completionTokens int64) (contracts.SupplySettlementResult, error) {
	if promptTokens < 0 || completionTokens < 0 {
		return contracts.SupplySettlementResult{}, ErrInvalid
	}
	return s.finishSupplyRequest(ctx, reservationID, promptTokens, completionTokens, false, false, "metered")
}

func (s *PostgresStore) SettleSupplyRequestConservatively(ctx context.Context, reservationID, reasonCode string) (contracts.SupplySettlementResult, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" || len(reasonCode) > 64 {
		return contracts.SupplySettlementResult{}, ErrInvalid
	}
	return s.finishSupplyRequest(ctx, reservationID, 0, 0, false, true, reasonCode)
}

func (s *PostgresStore) ReleaseSupplyRequest(ctx context.Context, reservationID, reasonCode string) (contracts.SupplySettlementResult, error) {
	return s.finishSupplyRequest(ctx, reservationID, 0, 0, true, false, strings.TrimSpace(reasonCode))
}

func (s *PostgresStore) finishSupplyRequest(ctx context.Context, reservationID string, promptTokens, completionTokens int64, release, conservative bool, reasonCode string) (contracts.SupplySettlementResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	reservation, err := scanWalletReservation(tx.QueryRow(ctx, `SELECT `+reservationColumns+` FROM wallet_reservations WHERE id=$1 FOR UPDATE`, reservationID))
	if err != nil {
		return contracts.SupplySettlementResult{}, mapNotFound(err)
	}
	usage, err := scanSupplyUsage(tx.QueryRow(ctx, `SELECT `+supplyUsageColumns+` FROM supply_usage_records WHERE reservation_id=$1 FOR UPDATE`, reservationID))
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	wallet, err := scanHybridWallet(tx.QueryRow(ctx, `SELECT user_id,currency,available_micros,reserved_micros,version,updated_at FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`, reservation.UserID, reservation.Currency))
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	if reservation.Status != contracts.WalletReservationActive {
		return contracts.SupplySettlementResult{Wallet: wallet, Reservation: reservation, Usage: usage, ChargedMicros: reservation.SettledMicros, ReleasedMicros: reservation.ReservedMicros - reservation.SettledMicros}, nil
	}
	charged, supplier := int64(0), int64(0)
	if conservative {
		charged, supplier = reservation.ReservedMicros, reservation.ReservedMicros
	} else if !release {
		charged = tokenCharge(usage.InputPriceMicrosPerMillion, promptTokens) + tokenCharge(usage.OutputPriceMicrosPerMillion, completionTokens)
		supplier = tokenCharge(usage.InputSupplierMicrosPerMillion, promptTokens) + tokenCharge(usage.OutputSupplierMicrosPerMillion, completionTokens)
		if charged > reservation.ReservedMicros || supplier > charged {
			return contracts.SupplySettlementResult{}, ErrConflict
		}
	}
	released := reservation.ReservedMicros - charged
	now := nowUTC()
	wallet, err = scanHybridWallet(tx.QueryRow(ctx, `UPDATE wallet_accounts SET reserved_micros=reserved_micros-$3,
		available_micros=available_micros+$4,version=version+1,updated_at=$5 WHERE user_id=$1 AND currency=$2 AND reserved_micros >= $3
		RETURNING user_id,currency,available_micros,reserved_micros,version,updated_at`, reservation.UserID, reservation.Currency, reservation.ReservedMicros, released, now))
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	status, usageStatus := contracts.WalletReservationSettled, contracts.SupplyUsageSettled
	if release {
		status, usageStatus = contracts.WalletReservationReleased, contracts.SupplyUsageReleased
	}
	reservation, err = scanWalletReservation(tx.QueryRow(ctx, `UPDATE wallet_reservations SET settled_micros=$2,status=$3,updated_at=$4 WHERE id=$1 RETURNING `+reservationColumns,
		reservation.ID, charged, string(status), now))
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	usage, err = scanSupplyUsage(tx.QueryRow(ctx, `UPDATE supply_usage_records SET prompt_tokens=$2,completion_tokens=$3,settled_micros=$4,status=$5,settlement_reason=$6,completed_at=$7 WHERE reservation_id=$1 RETURNING `+supplyUsageColumns,
		reservation.ID, promptTokens, completionTokens, charged, string(usageStatus), reasonCode, now))
	if err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	if release {
		if err := insertWalletJournal(ctx, tx, reservation.UserID, contracts.WalletJournalRelease, reservation.Currency, reservation.ReservedMicros, "release:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserReserved, contracts.WalletAccountUserAvailable, now); err != nil {
			return contracts.SupplySettlementResult{}, err
		}
	} else {
		if err := insertSupplySettlementJournal(ctx, tx, reservation.UserID, reservation.Currency, charged, supplier, "settle:"+reservation.ID, "supply_reservation", reservation.ID, now); err != nil {
			return contracts.SupplySettlementResult{}, err
		}
		if err := insertWalletJournal(ctx, tx, reservation.UserID, contracts.WalletJournalRelease, reservation.Currency, released, "settle-release:"+reservation.ID, "supply_reservation", reservation.ID, contracts.WalletAccountUserReserved, contracts.WalletAccountUserAvailable, now); err != nil {
			return contracts.SupplySettlementResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.SupplySettlementResult{}, err
	}
	return contracts.SupplySettlementResult{Wallet: wallet, Reservation: reservation, Usage: usage, ChargedMicros: charged, SupplierMicros: supplier, ReleasedMicros: released}, nil
}
