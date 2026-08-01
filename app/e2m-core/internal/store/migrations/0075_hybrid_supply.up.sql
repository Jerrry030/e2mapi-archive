ALTER TABLE upstream_pools
    ADD COLUMN IF NOT EXISTS resource_class TEXT NOT NULL DEFAULT 'stable';
ALTER TABLE upstream_pools
    ADD COLUMN IF NOT EXISTS delivery_mode TEXT NOT NULL DEFAULT 'connector';

ALTER TABLE upstream_pools
    DROP CONSTRAINT IF EXISTS ck_upstream_pools_resource_class;
ALTER TABLE upstream_pools
    ADD CONSTRAINT ck_upstream_pools_resource_class
    CHECK (resource_class IN ('economy', 'stable'));
ALTER TABLE upstream_pools
    DROP CONSTRAINT IF EXISTS ck_upstream_pools_delivery_mode;
ALTER TABLE upstream_pools
    ADD CONSTRAINT ck_upstream_pools_delivery_mode
    CHECK (delivery_mode IN ('connector', 'supply_gateway'));

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS provider_order_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_orders_provider_order
    ON payment_orders(provider_instance_id, provider_order_id)
    WHERE provider_instance_id IS NOT NULL AND provider_order_id <> '';

CREATE TABLE IF NOT EXISTS hybrid_allocations (
    instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id),
    basis TEXT NOT NULL DEFAULT 'requests' CHECK (basis = 'requests'),
    owner_percent INTEGER NOT NULL CHECK (owner_percent BETWEEN 0 AND 100),
    economy_percent INTEGER NOT NULL CHECK (economy_percent BETWEEN 0 AND 100),
    stable_percent INTEGER NOT NULL CHECK (stable_percent BETWEEN 0 AND 100),
    owner_burst_max INTEGER NOT NULL CHECK (owner_burst_max BETWEEN owner_percent AND 100),
    economy_burst_max INTEGER NOT NULL CHECK (economy_burst_max BETWEEN economy_percent AND 100),
    stable_burst_max INTEGER NOT NULL CHECK (stable_burst_max BETWEEN stable_percent AND 100),
    model_overrides JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(model_overrides) = 'array'),
    daily_budget_micros BIGINT NOT NULL DEFAULT 0 CHECK (daily_budget_micros >= 0),
    max_unit_price_micros BIGINT NOT NULL DEFAULT 0 CHECK (max_unit_price_micros >= 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_hybrid_allocations_total CHECK (owner_percent + economy_percent + stable_percent = 100)
);
CREATE INDEX IF NOT EXISTS idx_hybrid_allocations_user ON hybrid_allocations(user_id, instance_id);

CREATE TABLE IF NOT EXISTS wallet_accounts (
    user_id BIGINT NOT NULL REFERENCES users(id),
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    available_micros BIGINT NOT NULL DEFAULT 0 CHECK (available_micros >= 0),
    reserved_micros BIGINT NOT NULL DEFAULT 0 CHECK (reserved_micros >= 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, currency)
);

CREATE TABLE IF NOT EXISTS wallet_journals (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK (kind IN ('recharge','reserve','settle','release','refund')),
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_micros BIGINT NOT NULL CHECK (amount_micros > 0),
    idempotency_key TEXT NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_wallet_journals_user_created ON wallet_journals(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS wallet_entries (
    id TEXT PRIMARY KEY,
    journal_id TEXT NOT NULL REFERENCES wallet_journals(id) ON DELETE CASCADE,
    account TEXT NOT NULL CHECK (account IN ('platform_cash','user_available','user_reserved','platform_revenue','upstream_payable')),
    direction TEXT NOT NULL CHECK (direction IN ('debit','credit')),
    amount_micros BIGINT NOT NULL CHECK (amount_micros > 0),
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_wallet_entries_journal ON wallet_entries(journal_id);

CREATE OR REPLACE FUNCTION e2m_assert_wallet_journal_balance(target_id TEXT)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    expected BIGINT;
    expected_currency TEXT;
    debit_total BIGINT;
    credit_total BIGINT;
    entry_count BIGINT;
    currency_mismatch_count BIGINT;
BEGIN
    SELECT amount_micros, currency
      INTO expected, expected_currency
      FROM wallet_journals WHERE id = target_id;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    SELECT count(*),
           COALESCE(sum(amount_micros) FILTER (WHERE direction='debit'), 0),
           COALESCE(sum(amount_micros) FILTER (WHERE direction='credit'), 0),
           count(*) FILTER (WHERE currency <> expected_currency)
      INTO entry_count, debit_total, credit_total, currency_mismatch_count
      FROM wallet_entries WHERE journal_id = target_id;
    IF entry_count < 2 OR debit_total <> credit_total OR debit_total <> expected OR currency_mismatch_count <> 0 THEN
        RAISE EXCEPTION 'wallet journal % is unbalanced', target_id USING ERRCODE = '23514';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION e2m_validate_wallet_journal_balance()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_target_id TEXT;
    new_target_id TEXT;
BEGIN
    IF TG_TABLE_NAME = 'wallet_entries' THEN
        IF TG_OP <> 'INSERT' THEN
            old_target_id := OLD.journal_id;
        END IF;
        IF TG_OP <> 'DELETE' THEN
            new_target_id := NEW.journal_id;
        END IF;
    ELSE
        IF TG_OP <> 'INSERT' THEN
            old_target_id := OLD.id;
        END IF;
        IF TG_OP <> 'DELETE' THEN
            new_target_id := NEW.id;
        END IF;
    END IF;
    IF old_target_id IS NOT NULL THEN
        PERFORM e2m_assert_wallet_journal_balance(old_target_id);
    END IF;
    IF new_target_id IS NOT NULL AND new_target_id IS DISTINCT FROM old_target_id THEN
        PERFORM e2m_assert_wallet_journal_balance(new_target_id);
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS wallet_entries_balance_check ON wallet_entries;
CREATE CONSTRAINT TRIGGER wallet_entries_balance_check
AFTER INSERT OR UPDATE OR DELETE ON wallet_entries
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION e2m_validate_wallet_journal_balance();

DROP TRIGGER IF EXISTS wallet_journals_balance_check ON wallet_journals;
CREATE CONSTRAINT TRIGGER wallet_journals_balance_check
AFTER INSERT OR UPDATE ON wallet_journals
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION e2m_validate_wallet_journal_balance();

CREATE TABLE IF NOT EXISTS virtual_keys (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    resource_class TEXT NOT NULL CHECK (resource_class IN ('economy','stable')),
    prefix TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    secret_ref TEXT NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    models JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(models) = 'array'),
    daily_limit_micros BIGINT NOT NULL DEFAULT 0 CHECK (daily_limit_micros >= 0),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (instance_id, resource_class, name)
);
CREATE INDEX IF NOT EXISTS idx_virtual_keys_user ON virtual_keys(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_virtual_keys_instance_class ON virtual_keys(instance_id, resource_class) WHERE enabled;

CREATE TABLE IF NOT EXISTS supply_channel_endpoints (
    channel_id TEXT PRIMARY KEY REFERENCES upstream_channels(id) ON DELETE CASCADE,
    base_url TEXT NOT NULL,
    secret_ref TEXT NOT NULL UNIQUE,
    masked_value TEXT NOT NULL,
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    input_price_micros_per_million BIGINT NOT NULL CHECK (input_price_micros_per_million >= 0),
    output_price_micros_per_million BIGINT NOT NULL CHECK (output_price_micros_per_million >= 0),
    input_supplier_micros_per_million BIGINT NOT NULL CHECK (input_supplier_micros_per_million BETWEEN 0 AND input_price_micros_per_million),
    output_supplier_micros_per_million BIGINT NOT NULL CHECK (output_supplier_micros_per_million BETWEEN 0 AND output_price_micros_per_million),
    max_request_micros BIGINT NOT NULL CHECK (max_request_micros > 0),
    max_concurrency INTEGER NOT NULL DEFAULT 0 CHECK (max_concurrency >= 0),
    capacity_percent INTEGER NOT NULL DEFAULT 100 CHECK (capacity_percent BETWEEN 0 AND 100),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wallet_reservations (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    virtual_key_id TEXT NOT NULL REFERENCES virtual_keys(id),
    channel_id TEXT NOT NULL REFERENCES upstream_channels(id),
    request_id TEXT NOT NULL,
    currency TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    reserved_micros BIGINT NOT NULL CHECK (reserved_micros > 0),
    settled_micros BIGINT NOT NULL DEFAULT 0 CHECK (settled_micros >= 0),
    status TEXT NOT NULL CHECK (status IN ('active','settled','released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (virtual_key_id, request_id),
    CHECK (settled_micros <= reserved_micros)
);
CREATE INDEX IF NOT EXISTS idx_wallet_reservations_active_channel
    ON wallet_reservations(channel_id, created_at) WHERE status='active';

CREATE TABLE IF NOT EXISTS supply_usage_records (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    reservation_id TEXT NOT NULL UNIQUE REFERENCES wallet_reservations(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    instance_id TEXT NOT NULL REFERENCES instances(id),
    virtual_key_id TEXT NOT NULL REFERENCES virtual_keys(id),
    resource_class TEXT NOT NULL CHECK (resource_class IN ('economy','stable')),
    channel_id TEXT NOT NULL REFERENCES upstream_channels(id),
    model TEXT NOT NULL,
    prompt_tokens BIGINT NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens BIGINT NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    reserved_micros BIGINT NOT NULL CHECK (reserved_micros > 0),
    settled_micros BIGINT NOT NULL DEFAULT 0 CHECK (settled_micros >= 0),
    status TEXT NOT NULL CHECK (status IN ('reserved','settled','released')),
    settlement_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (virtual_key_id, request_id),
    CHECK (settled_micros <= reserved_micros)
);
CREATE INDEX IF NOT EXISTS idx_supply_usage_instance_created ON supply_usage_records(instance_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_supply_usage_key_day ON supply_usage_records(virtual_key_id, created_at);

CREATE TABLE IF NOT EXISTS payment_callback_events (
    id TEXT PRIMARY KEY,
    provider_instance_id TEXT NOT NULL REFERENCES payment_provider_instances(id),
    provider_key TEXT NOT NULL CHECK (provider_key IN ('easypay','alipay','wxpay','stripe','airwallex')),
    event_id TEXT NOT NULL,
    order_id TEXT REFERENCES payment_orders(id) ON DELETE SET NULL,
    body_hash TEXT NOT NULL,
    accepted BOOLEAN NOT NULL DEFAULT FALSE,
    error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_instance_id, event_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_callback_order ON payment_callback_events(order_id, created_at);
