-- Durable usage-attribution outbox. Connector telemetry is sanitized before
-- it reaches this table; URLs, credentials and raw upstream responses are not
-- part of the persisted shape.
BEGIN;

CREATE TABLE IF NOT EXISTS upstream_cost_attribution_jobs (
    usage_observation_id TEXT PRIMARY KEY
        REFERENCES channel_observations(id) ON DELETE CASCADE,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id            TEXT NOT NULL,
    instance_id           TEXT NOT NULL,
    model_key             TEXT NOT NULL,
    group_key             TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT CHECK (input_tokens >= 0),
    output_tokens         BIGINT CHECK (output_tokens >= 0),
    cached_input_tokens   BIGINT CHECK (cached_input_tokens >= 0),
    request_count         BIGINT CHECK (request_count >= 0),
    occurred_at           TIMESTAMPTZ NOT NULL,
    calculation_version   TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending','processing','retrying','succeeded')),
    attempts              BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code       TEXT NOT NULL DEFAULT '',
    lease_owner           TEXT NOT NULL DEFAULT '',
    lease_until           TIMESTAMPTZ,
    lease_version         BIGINT NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at          TIMESTAMPTZ,
    CONSTRAINT upstream_cost_attribution_jobs_identity_check CHECK (
        BTRIM(usage_observation_id) <> '' AND BTRIM(channel_id) <> '' AND
        BTRIM(instance_id) <> '' AND BTRIM(model_key) <> '' AND
        BTRIM(calculation_version) <> '' AND char_length(group_key) <= 128
    ),
    CONSTRAINT upstream_cost_attribution_jobs_error_code_check
        CHECK (char_length(last_error_code) <= 64),
    CONSTRAINT upstream_cost_attribution_jobs_lease_shape_check CHECK (
        (status = 'processing' AND BTRIM(lease_owner) <> '' AND lease_until IS NOT NULL) OR
        (status <> 'processing' AND lease_owner = '' AND lease_until IS NULL)
    ),
    CONSTRAINT upstream_cost_attribution_jobs_completion_shape_check CHECK (
        (status = 'succeeded' AND completed_at IS NOT NULL) OR
        (status <> 'succeeded' AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_upstream_cost_attribution_jobs_claim
    ON upstream_cost_attribution_jobs (status, next_attempt_at, lease_until, created_at);
CREATE INDEX IF NOT EXISTS idx_upstream_cost_attribution_jobs_owner_occurred
    ON upstream_cost_attribution_jobs (user_id, occurred_at DESC);

COMMIT;
