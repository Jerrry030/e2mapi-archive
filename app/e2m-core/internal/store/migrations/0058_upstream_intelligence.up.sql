-- Owner-scoped, sanitized upstream intelligence facts. Upstream network
-- locations, sensitive connection material, and HTTP response bodies never enter Core.
BEGIN;

CREATE TABLE IF NOT EXISTS upstream_intelligence_fact_versions (
    user_id       BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    fact_version  BIGINT NOT NULL DEFAULT 0 CHECK (fact_version >= 0),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS upstream_intelligence_sources (
    id                    TEXT PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connector_id          TEXT NOT NULL,
    instance_id           TEXT NOT NULL,
    local_ref             TEXT NOT NULL,
    mode                  TEXT NOT NULL CHECK (mode IN ('owned','external')),
    provider              TEXT NOT NULL CHECK (provider = 'sub2api'),
    display_name          TEXT NOT NULL,
    currency              TEXT NOT NULL DEFAULT '',
    poll_interval_seconds INTEGER NOT NULL DEFAULT 300
                            CHECK (poll_interval_seconds BETWEEN 60 AND 3600),
    status                TEXT NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active','paused','disconnected')),
    capability_balance    BOOLEAN NOT NULL DEFAULT FALSE,
    capability_groups     BOOLEAN NOT NULL DEFAULT FALSE,
    capability_rates      BOOLEAN NOT NULL DEFAULT FALSE,
    capability_prices     BOOLEAN NOT NULL DEFAULT FALSE,
    last_run_at           TIMESTAMPTZ,
    last_success_at       TIMESTAMPTZ,
    next_poll_at          TIMESTAMPTZ,
    last_coverage         TEXT NOT NULL DEFAULT ''
                            CHECK (last_coverage IN ('','complete','partial','unavailable')),
    last_error_code       TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upstream_intelligence_sources_id_owner_key
        UNIQUE (id, user_id),
    CONSTRAINT upstream_intelligence_sources_id_owner_connector_key
        UNIQUE (id, user_id, connector_id),
    CONSTRAINT upstream_intelligence_sources_registration_key
        UNIQUE (user_id, connector_id, local_ref),
    -- Historical facts outlive Connector revocation/deletion. The lifecycle
    -- service must mark this source disconnected before a controlled removal.
    CONSTRAINT upstream_intelligence_sources_connector_owner_fkey
        FOREIGN KEY (connector_id, user_id, instance_id)
        REFERENCES connectors(connector_id, user_id, instance_id)
        ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT upstream_intelligence_sources_local_ref_check
        CHECK (BTRIM(local_ref) <> '' AND char_length(local_ref) <= 128),
    CONSTRAINT upstream_intelligence_sources_display_name_check
        CHECK (BTRIM(display_name) <> '' AND char_length(display_name) <= 128),
    CONSTRAINT upstream_intelligence_sources_currency_check
        CHECK (currency = '' OR currency ~ '^[A-Z]{3}$'),
    CONSTRAINT upstream_intelligence_sources_error_code_check
        CHECK (char_length(last_error_code) <= 64)
);

CREATE INDEX IF NOT EXISTS idx_upstream_intelligence_sources_owner_status
    ON upstream_intelligence_sources (user_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_intelligence_sources_connector
    ON upstream_intelligence_sources (connector_id, status);

CREATE TABLE IF NOT EXISTS upstream_collection_runs (
    id              TEXT NOT NULL,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id       TEXT NOT NULL,
    connector_id    TEXT NOT NULL,
    trigger         TEXT NOT NULL CHECK (trigger IN ('scheduled','manual','task')),
    status          TEXT NOT NULL CHECK (status IN ('succeeded','partial','failed')),
    coverage        TEXT NOT NULL CHECK (coverage IN ('complete','partial','unavailable')),
    started_at      TIMESTAMPTZ NOT NULL,
    observed_at     TIMESTAMPTZ NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    snapshot_hash   TEXT NOT NULL DEFAULT '',
    manifest_hash   TEXT NOT NULL DEFAULT '',
    batch_count     INTEGER NOT NULL DEFAULT 0 CHECK (batch_count >= 0),
    fact_count      INTEGER NOT NULL DEFAULT 0 CHECK (fact_count >= 0),
    page_count      INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    error_code      TEXT NOT NULL DEFAULT '',
    retryable       BOOLEAN NOT NULL DEFAULT FALSE,
    finalized_fact_version BIGINT NOT NULL DEFAULT 0 CHECK (finalized_fact_version >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id),
    CONSTRAINT upstream_collection_runs_id_owner_source_key
        UNIQUE (user_id, id, source_id),
    CONSTRAINT upstream_collection_runs_source_owner_fkey
        FOREIGN KEY (source_id, user_id, connector_id)
        REFERENCES upstream_intelligence_sources(id, user_id, connector_id) ON DELETE CASCADE,
    CONSTRAINT upstream_collection_runs_terminal_shape_check CHECK (
        completed_at IS NOT NULL
    ),
    CONSTRAINT upstream_collection_runs_evidence_shape_check CHECK (
        (status = 'succeeded' AND coverage = 'complete') OR
        (status = 'partial' AND coverage = 'partial') OR
        (status = 'failed' AND coverage = 'unavailable')
    ),
    CONSTRAINT upstream_collection_runs_time_order_check
        CHECK (observed_at >= started_at AND (completed_at IS NULL OR completed_at >= observed_at)),
    CONSTRAINT upstream_collection_runs_snapshot_hash_check
        CHECK (snapshot_hash = '' OR snapshot_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upstream_collection_runs_manifest_hash_check
        CHECK (manifest_hash = '' OR manifest_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upstream_collection_runs_error_code_check
        CHECK (
            (status = 'failed' AND error_code IN ('auth_failed','rate_limited','schema_unsupported','response_too_large','upstream_unavailable')) OR
            (status <> 'failed' AND error_code = '' AND retryable = FALSE)
        ),
    CONSTRAINT upstream_collection_runs_zero_fact_shape_check CHECK (
        fact_count <> 0 OR
        (batch_count = 1 AND page_count <= 1 AND status IN ('succeeded','failed'))
    )
);

CREATE INDEX IF NOT EXISTS idx_upstream_collection_runs_source_observed
    ON upstream_collection_runs (source_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_collection_runs_owner_received
    ON upstream_collection_runs (user_id, received_at DESC);

-- Batches retain only the normalized, sanitized envelope needed for durable
-- manifest validation. It is not an upstream HTTP body.
CREATE TABLE IF NOT EXISTS upstream_ingest_batches (
    run_id             TEXT NOT NULL,
    user_id            BIGINT NOT NULL,
    source_id          TEXT NOT NULL,
    batch_no           INTEGER NOT NULL CHECK (batch_no >= 0),
    batch_count        INTEGER NOT NULL CHECK (batch_count > 0 AND batch_no < batch_count),
    payload_hash       TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    manifest_hash      TEXT NOT NULL CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
    wallet_count       INTEGER NOT NULL DEFAULT 0 CHECK (wallet_count >= 0),
    offer_count        INTEGER NOT NULL DEFAULT 0 CHECK (offer_count >= 0),
    received_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, run_id, batch_no),
    CONSTRAINT upstream_ingest_batches_run_owner_fkey
        FOREIGN KEY (user_id, run_id, source_id)
        REFERENCES upstream_collection_runs(user_id, id, source_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_upstream_ingest_batches_owner_received
    ON upstream_ingest_batches (user_id, received_at DESC);

CREATE TABLE IF NOT EXISTS upstream_wallet_observations (
    run_id          TEXT NOT NULL,
    id              TEXT NOT NULL,
    user_id         BIGINT NOT NULL,
    source_id       TEXT NOT NULL,
    balance_amount  NUMERIC(38,18),
    unit_kind       TEXT NOT NULL CHECK (unit_kind IN ('fiat','credit','unknown')),
    currency        TEXT NOT NULL DEFAULT '',
    observed_at     TIMESTAMPTZ NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    fresh_until     TIMESTAMPTZ NOT NULL,
    accuracy        TEXT NOT NULL
                        CHECK (accuracy IN ('exact','derived','estimated','unknown','unattributed')),
    coverage        TEXT NOT NULL
                        CHECK (coverage IN ('complete','partial','unavailable')),
    confidence      NUMERIC(38,18),
    missing_fields  JSONB NOT NULL DEFAULT '[]'::jsonb
                        CHECK (jsonb_typeof(missing_fields) = 'array'),
    reason_code     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, run_id, id),
    CONSTRAINT upstream_wallet_observations_run_owner_fkey
        FOREIGN KEY (user_id, run_id, source_id)
        REFERENCES upstream_collection_runs(user_id, id, source_id) ON DELETE CASCADE,
    CONSTRAINT upstream_wallet_observations_currency_check CHECK (
        (unit_kind = 'fiat' AND currency ~ '^[A-Z]{3}$') OR
        (unit_kind <> 'fiat' AND currency = '')
    ),
    CONSTRAINT upstream_wallet_observations_freshness_check
        CHECK (fresh_until >= observed_at),
    CONSTRAINT upstream_wallet_observations_confidence_check CHECK (
        confidence IS NULL OR
        (accuracy IN ('derived','estimated') AND confidence BETWEEN 0 AND 1)
    ),
    CONSTRAINT upstream_wallet_observations_unknown_reason_check CHECK (
        accuracy <> 'unknown' OR jsonb_array_length(missing_fields) > 0 OR reason_code <> ''
    ),
    CONSTRAINT upstream_wallet_observations_reason_code_check
        CHECK (char_length(reason_code) <= 64)
);

CREATE INDEX IF NOT EXISTS idx_upstream_wallet_observations_source_observed
    ON upstream_wallet_observations (source_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_wallet_observations_owner_observed
    ON upstream_wallet_observations (user_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS upstream_offer_observations (
    run_id                 TEXT NOT NULL,
    id                     TEXT NOT NULL,
    user_id                BIGINT NOT NULL,
    source_id              TEXT NOT NULL,
    group_key              TEXT NOT NULL,
    model_key              TEXT NOT NULL,
    price_dimension        TEXT NOT NULL
                               CHECK (price_dimension IN ('input','output','cached_input','request')),
    settlement_currency    TEXT NOT NULL DEFAULT '',
    group_multiplier       NUMERIC(38,18),
    recharge_yield         NUMERIC(38,18),
    published_unit_price   NUMERIC(38,18),
    per_tokens             BIGINT NOT NULL CHECK (per_tokens > 0),
    effective_multiplier   NUMERIC(38,18),
    effective_unit_cost    NUMERIC(38,18),
    formula_version        TEXT NOT NULL DEFAULT '',
    accuracy               TEXT NOT NULL
                               CHECK (accuracy IN ('exact','derived','estimated','unknown','unattributed')),
    coverage               TEXT NOT NULL
                               CHECK (coverage IN ('complete','partial','unavailable')),
    confidence             NUMERIC(38,18),
    observed_at            TIMESTAMPTZ NOT NULL,
    effective_at           TIMESTAMPTZ NOT NULL,
    received_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    fresh_until            TIMESTAMPTZ NOT NULL,
    valid_until            TIMESTAMPTZ,
    missing_fields         JSONB NOT NULL DEFAULT '[]'::jsonb
                               CHECK (jsonb_typeof(missing_fields) = 'array'),
    reason_code            TEXT NOT NULL DEFAULT '',
    adapter_schema_version INTEGER NOT NULL CHECK (adapter_schema_version > 0),
    source_revision        TEXT NOT NULL DEFAULT '',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, run_id, id),
    CONSTRAINT upstream_offer_observations_identity_key
        UNIQUE (user_id, run_id, group_key, model_key, price_dimension),
    CONSTRAINT upstream_offer_observations_run_owner_fkey
        FOREIGN KEY (user_id, run_id, source_id)
        REFERENCES upstream_collection_runs(user_id, id, source_id) ON DELETE CASCADE,
    CONSTRAINT upstream_offer_observations_identity_check CHECK (
        BTRIM(group_key) <> '' AND char_length(group_key) <= 128 AND
        BTRIM(model_key) <> '' AND char_length(model_key) <= 256
    ),
    CONSTRAINT upstream_offer_observations_currency_check
        CHECK (settlement_currency = '' OR settlement_currency ~ '^[A-Z]{3}$'),
    CONSTRAINT upstream_offer_observations_decimal_sign_check CHECK (
        (group_multiplier IS NULL OR group_multiplier >= 0) AND
        (recharge_yield IS NULL OR recharge_yield > 0) AND
        (published_unit_price IS NULL OR published_unit_price >= 0) AND
        (effective_multiplier IS NULL OR effective_multiplier >= 0) AND
        (effective_unit_cost IS NULL OR effective_unit_cost >= 0)
    ),
    CONSTRAINT upstream_offer_observations_confidence_check CHECK (
        confidence IS NULL OR
        (accuracy IN ('derived','estimated') AND confidence BETWEEN 0 AND 1)
    ),
    CONSTRAINT upstream_offer_observations_time_order_check CHECK (
        fresh_until >= observed_at AND
        (valid_until IS NULL OR valid_until > effective_at)
    ),
    CONSTRAINT upstream_offer_observations_unknown_reason_check CHECK (
        accuracy <> 'unknown' OR jsonb_array_length(missing_fields) > 0 OR reason_code <> ''
    ),
    CONSTRAINT upstream_offer_observations_reason_code_check
        CHECK (char_length(reason_code) <= 64)
);

CREATE INDEX IF NOT EXISTS idx_upstream_offer_observations_comparison
    ON upstream_offer_observations
       (source_id, group_key, model_key, price_dimension, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_offer_observations_owner_observed
    ON upstream_offer_observations (user_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS upstream_snapshot_absences (
    source_id                 TEXT NOT NULL,
    user_id                   BIGINT NOT NULL,
    comparison_key           TEXT NOT NULL,
    consecutive_complete_runs INTEGER NOT NULL DEFAULT 0
                                  CHECK (consecutive_complete_runs >= 0),
    last_present_observation_id TEXT NOT NULL DEFAULT '',
    last_present_run_id       TEXT NOT NULL DEFAULT '',
    first_absent_at           TIMESTAMPTZ,
    last_absent_run_id        TEXT NOT NULL DEFAULT '',
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id, comparison_key),
    CONSTRAINT upstream_snapshot_absences_source_owner_fkey
        FOREIGN KEY (source_id, user_id)
        REFERENCES upstream_intelligence_sources(id, user_id) ON DELETE CASCADE,
    CONSTRAINT upstream_snapshot_absences_comparison_key_check
        CHECK (BTRIM(comparison_key) <> '' AND char_length(comparison_key) <= 512)
);

CREATE INDEX IF NOT EXISTS idx_upstream_snapshot_absences_owner_updated
    ON upstream_snapshot_absences (user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS upstream_change_events (
    id                    TEXT PRIMARY KEY,
    user_id               BIGINT NOT NULL,
    source_id             TEXT NOT NULL,
    event_type            TEXT NOT NULL CHECK (event_type IN (
                              'balance_low','balance_recovered',
                              'group_added','group_changed','group_removed',
                              'model_added','price_increased','price_decreased','model_removed',
                              'source_stale','source_recovered'
                          )),
    event_fingerprint     TEXT NOT NULL,
    before_observation_id TEXT NOT NULL DEFAULT '',
    after_observation_id  TEXT NOT NULL DEFAULT '',
    absolute_change       NUMERIC(38,18),
    percentage_change     NUMERIC(38,18),
    first_detected_at     TIMESTAMPTZ NOT NULL,
    confirmed_at          TIMESTAMPTZ NOT NULL,
    severity              TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
    impact_scope          JSONB NOT NULL DEFAULT '{}'::jsonb
                              CHECK (jsonb_typeof(impact_scope) = 'object'),
    group_key             TEXT NOT NULL DEFAULT '',
    model_key             TEXT NOT NULL DEFAULT '',
    price_dimension       TEXT NOT NULL DEFAULT '' CHECK (
                              price_dimension IN ('','input','output','cached_input','request')
                          ),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upstream_change_events_source_owner_fkey
        FOREIGN KEY (source_id, user_id)
        REFERENCES upstream_intelligence_sources(id, user_id) ON DELETE CASCADE,
    CONSTRAINT upstream_change_events_fingerprint_key
        UNIQUE (source_id, event_fingerprint),
    CONSTRAINT upstream_change_events_fingerprint_check
        CHECK (BTRIM(event_fingerprint) <> '' AND char_length(event_fingerprint) <= 256),
    CONSTRAINT upstream_change_events_time_order_check
        CHECK (confirmed_at >= first_detected_at)
);

CREATE INDEX IF NOT EXISTS idx_upstream_change_events_owner_confirmed
    ON upstream_change_events (user_id, confirmed_at DESC);

CREATE TABLE IF NOT EXISTS upstream_intelligence_links (
    id                           TEXT PRIMARY KEY,
    user_id                      BIGINT NOT NULL,
    intelligence_source_id       TEXT NOT NULL,
    link_scope                   TEXT NOT NULL CHECK (link_scope IN ('source_identity','channel')),
    upstream_source_identity     TEXT NOT NULL DEFAULT '',
    channel_id                   TEXT,
    price_dimension              TEXT NOT NULL DEFAULT '' CHECK (
                                     price_dimension IN ('','input','output','cached_input','request')
                                 ),
    status                       TEXT NOT NULL DEFAULT 'active'
                                     CHECK (status IN ('active','inactive')),
    verified_at                  TIMESTAMPTZ,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upstream_intelligence_links_id_owner_key
        UNIQUE (id, user_id),
    CONSTRAINT upstream_intelligence_links_source_owner_fkey
        FOREIGN KEY (intelligence_source_id, user_id)
        REFERENCES upstream_intelligence_sources(id, user_id) ON DELETE CASCADE,
    CONSTRAINT upstream_intelligence_links_target_shape_check CHECK (
        (link_scope = 'source_identity' AND BTRIM(upstream_source_identity) <> '' AND channel_id IS NULL) OR
        (link_scope = 'channel' AND upstream_source_identity = '' AND BTRIM(channel_id) <> '')
    ),
    CONSTRAINT upstream_intelligence_links_verified_check
        CHECK (status <> 'active' OR verified_at IS NOT NULL)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_upstream_intelligence_links_active_source_identity
    ON upstream_intelligence_links (user_id, upstream_source_identity, price_dimension)
    WHERE status = 'active' AND link_scope = 'source_identity';
CREATE UNIQUE INDEX IF NOT EXISTS uq_upstream_intelligence_links_active_channel
    ON upstream_intelligence_links (user_id, channel_id, price_dimension)
    WHERE status = 'active' AND link_scope = 'channel';
CREATE INDEX IF NOT EXISTS idx_upstream_intelligence_links_source_status
    ON upstream_intelligence_links (intelligence_source_id, status);

-- PostgreSQL requires a matching unique key for the owner-scoped channel FK.
-- channel_id remains the allocation's primary key; this additional key makes
-- the ownership proof available to upstream_intelligence_links.
CREATE UNIQUE INDEX IF NOT EXISTS uq_upstream_channel_allocations_owner_channel
    ON upstream_channel_allocations (user_id, channel_id);

ALTER TABLE upstream_intelligence_links
    ADD CONSTRAINT upstream_intelligence_links_channel_owner_fkey
    FOREIGN KEY (user_id, channel_id)
    REFERENCES upstream_channel_allocations(user_id, channel_id)
    ON DELETE RESTRICT;

COMMIT;
