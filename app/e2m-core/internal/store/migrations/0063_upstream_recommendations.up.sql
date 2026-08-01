-- Evidence-bound, owner-scoped advisory recommendations. Decision inputs are
-- immutable; lifecycle transitions update only status and dry_run_id.
BEGIN;

CREATE TABLE upstream_recommendations (
    id                        TEXT NOT NULL,
    user_id                   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status                    TEXT NOT NULL,
    intelligence_fact_version BIGINT NOT NULL CHECK (intelligence_fact_version > 0),
    cost_ledger_fact_version  BIGINT NOT NULL CHECK (cost_ledger_fact_version > 0),
    link_fact_version         BIGINT NOT NULL CHECK (link_fact_version > 0),
    plan_generation           BIGINT NOT NULL CHECK (plan_generation > 0),
    from_source_id            TEXT NOT NULL,
    from_channel_id           TEXT NOT NULL,
    from_group_key            TEXT NOT NULL,
    to_source_id              TEXT NOT NULL,
    to_channel_id             TEXT NOT NULL,
    to_group_key              TEXT NOT NULL,
    model_key                 TEXT NOT NULL,
    price_dimension           TEXT NOT NULL,
    settlement_currency       TEXT NOT NULL,
    per_tokens                BIGINT NOT NULL CHECK (per_tokens > 0),
    affected_plan_ids         JSONB NOT NULL CHECK (jsonb_typeof(affected_plan_ids) = 'array'),
    affected_downstreams      JSONB NOT NULL CHECK (jsonb_typeof(affected_downstreams) = 'array'),
    evidence_ids              JSONB NOT NULL CHECK (jsonb_typeof(evidence_ids) = 'array'),
    constraints               JSONB NOT NULL CHECK (jsonb_typeof(constraints) = 'array'),
    from_cost_lower           NUMERIC(38,18) NOT NULL CHECK (from_cost_lower >= 0),
    from_cost_expected        NUMERIC(38,18) NOT NULL CHECK (from_cost_expected >= from_cost_lower),
    from_cost_upper           NUMERIC(38,18) NOT NULL CHECK (from_cost_upper >= from_cost_expected),
    to_cost_lower             NUMERIC(38,18) NOT NULL CHECK (to_cost_lower >= 0),
    to_cost_expected          NUMERIC(38,18) NOT NULL CHECK (to_cost_expected >= to_cost_lower),
    to_cost_upper             NUMERIC(38,18) NOT NULL CHECK (to_cost_upper >= to_cost_expected),
    savings_amount_lower      NUMERIC(38,18) NOT NULL CHECK (savings_amount_lower > 0),
    savings_amount_expected   NUMERIC(38,18) NOT NULL CHECK (savings_amount_expected >= savings_amount_lower),
    savings_amount_upper      NUMERIC(38,18) NOT NULL CHECK (savings_amount_upper >= savings_amount_expected),
    savings_percent_lower     NUMERIC(38,18) NOT NULL CHECK (savings_percent_lower > 0),
    savings_percent_expected  NUMERIC(38,18) NOT NULL CHECK (savings_percent_expected >= savings_percent_lower),
    savings_percent_upper     NUMERIC(38,18) NOT NULL CHECK (savings_percent_upper >= savings_percent_expected),
    formula_version           TEXT NOT NULL,
    strategy_version          TEXT NOT NULL,
    fingerprint               TEXT NOT NULL,
    dry_run_id                TEXT NOT NULL DEFAULT '',
    created_at                TIMESTAMPTZ NOT NULL,
    expires_at                TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id,id),
    CONSTRAINT upstream_recommendations_owner_fingerprint UNIQUE (user_id,fingerprint),
    CONSTRAINT upstream_recommendations_status_check CHECK (status IN (
        'open','shadowing','ready_for_dry_run','dry_running','dry_run_passed','dry_run_blocked','dismissed','expired'
    )),
    CONSTRAINT upstream_recommendations_dimension_check CHECK (price_dimension IN ('input','output','cached_input','request')),
    CONSTRAINT upstream_recommendations_currency_check CHECK (settlement_currency ~ '^[A-Z]{3}$'),
    CONSTRAINT upstream_recommendations_fingerprint_check CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upstream_recommendations_identity_check CHECK (
        id=btrim(id) AND id<>'' AND char_length(id)<=256 AND
        from_source_id=btrim(from_source_id) AND from_source_id<>'' AND char_length(from_source_id)<=256 AND
        to_source_id=btrim(to_source_id) AND to_source_id<>'' AND char_length(to_source_id)<=256 AND
        from_channel_id=btrim(from_channel_id) AND from_channel_id<>'' AND char_length(from_channel_id)<=256 AND
        to_channel_id=btrim(to_channel_id) AND to_channel_id<>'' AND char_length(to_channel_id)<=256 AND
        from_group_key=btrim(from_group_key) AND from_group_key<>'' AND char_length(from_group_key)<=128 AND
        to_group_key=btrim(to_group_key) AND to_group_key<>'' AND char_length(to_group_key)<=128 AND
        model_key=btrim(model_key) AND model_key<>'' AND char_length(model_key)<=256 AND
        formula_version=btrim(formula_version) AND formula_version<>'' AND char_length(formula_version)<=128 AND
        strategy_version=btrim(strategy_version) AND strategy_version<>'' AND char_length(strategy_version)<=128 AND
        from_source_id<>to_source_id AND from_channel_id<>to_channel_id AND expires_at>created_at
    ),
    CONSTRAINT upstream_recommendations_dry_run_shape CHECK (
        (status IN ('dry_running','dry_run_passed','dry_run_blocked') AND dry_run_id<>'') OR
        (status NOT IN ('dry_running','dry_run_passed','dry_run_blocked'))
    )
);

CREATE INDEX idx_upstream_recommendations_owner_created
    ON upstream_recommendations (user_id,created_at DESC,id DESC);
CREATE INDEX idx_upstream_recommendations_owner_status
    ON upstream_recommendations (user_id,status,created_at DESC);
CREATE INDEX idx_upstream_recommendations_owner_expiry
    ON upstream_recommendations (user_id,expires_at);

COMMIT;
