-- Immutable, owner-scoped historical upstream cost ledger. This table stores
-- normalized financial facts only; transport locations and secrets remain
-- confined to Connector.
BEGIN;

CREATE TABLE IF NOT EXISTS upstream_cost_fact_versions (
    user_id       BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    fact_version  BIGINT NOT NULL DEFAULT 0 CHECK (fact_version >= 0),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS upstream_cost_facts (
    id                     TEXT PRIMARY KEY,
    idempotency_key        TEXT NOT NULL,
    user_id                BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fact_version           BIGINT NOT NULL CHECK (fact_version > 0),
    usage_observation_id   TEXT NOT NULL,
    channel_id             TEXT NOT NULL DEFAULT '',
    instance_id            TEXT NOT NULL DEFAULT '',
    intelligence_source_id TEXT NOT NULL DEFAULT '',
    model_key              TEXT NOT NULL DEFAULT '',
    group_key              TEXT NOT NULL DEFAULT '',
    price_dimension        TEXT NOT NULL CHECK (price_dimension IN ('input','output','cached_input','request')),
    quantity               BIGINT CHECK (quantity >= 0),
    per_tokens             BIGINT NOT NULL DEFAULT 0 CHECK (per_tokens >= 0),
    price_observation_id   TEXT NOT NULL DEFAULT '',
    price_effective_at     TIMESTAMPTZ,
    price_valid_until      TIMESTAMPTZ,
    unit_cost              NUMERIC(38,18),
    amount                 NUMERIC(38,18),
    currency               TEXT NOT NULL DEFAULT '',
    attribution            TEXT NOT NULL CHECK (attribution IN ('exact','derived','estimated','unknown','unattributed')),
    price_status           TEXT NOT NULL CHECK (price_status IN ('valid','expired','unavailable')),
    calculation_version    TEXT NOT NULL,
    reason_code            TEXT NOT NULL DEFAULT '',
    missing_fields         JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(missing_fields) = 'array'),
    occurred_at            TIMESTAMPTZ NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upstream_cost_facts_owner_idempotency_key UNIQUE (user_id, idempotency_key),
    CONSTRAINT upstream_cost_facts_idempotency_shape_check
        CHECK (idempotency_key ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upstream_cost_facts_identity_shape_check CHECK (
        BTRIM(usage_observation_id) <> '' AND BTRIM(calculation_version) <> '' AND
        char_length(channel_id) <= 256 AND char_length(instance_id) <= 256 AND
        char_length(model_key) <= 256 AND char_length(group_key) <= 128
    ),
    CONSTRAINT upstream_cost_facts_decimal_sign_check
        CHECK ((unit_cost IS NULL OR unit_cost >= 0) AND (amount IS NULL OR amount >= 0)),
    CONSTRAINT upstream_cost_facts_currency_check
        CHECK (currency = '' OR currency ~ '^[A-Z]{3}$'),
    CONSTRAINT upstream_cost_facts_reason_code_check
        CHECK (char_length(reason_code) <= 64),
    CONSTRAINT upstream_cost_facts_time_order_check CHECK (
        (price_valid_until IS NULL OR price_effective_at IS NOT NULL) AND
        (price_valid_until IS NULL OR price_valid_until > price_effective_at)
    ),
    CONSTRAINT upstream_cost_facts_evidence_shape_check CHECK (
        (attribution IN ('exact','derived','estimated') AND price_status = 'valid' AND
         quantity IS NOT NULL AND per_tokens > 0 AND BTRIM(channel_id) <> '' AND
         BTRIM(model_key) <> '' AND BTRIM(group_key) <> '' AND
         BTRIM(intelligence_source_id) <> '' AND BTRIM(price_observation_id) <> '' AND
         price_effective_at IS NOT NULL AND unit_cost IS NOT NULL AND amount IS NOT NULL AND
         currency ~ '^[A-Z]{3}$' AND reason_code = '' AND jsonb_array_length(missing_fields) = 0) OR
        (attribution IN ('unknown','unattributed') AND amount IS NULL AND unit_cost IS NULL AND
         BTRIM(reason_code) <> '' AND jsonb_array_length(missing_fields) > 0)
    )
);

CREATE INDEX IF NOT EXISTS idx_upstream_cost_facts_owner_occurred
    ON upstream_cost_facts (user_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_cost_facts_owner_source_occurred
    ON upstream_cost_facts (user_id, intelligence_source_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_cost_facts_owner_channel_model_occurred
    ON upstream_cost_facts (user_id, channel_id, model_key, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_cost_facts_owner_fact_version
    ON upstream_cost_facts (user_id, fact_version);

COMMIT;
