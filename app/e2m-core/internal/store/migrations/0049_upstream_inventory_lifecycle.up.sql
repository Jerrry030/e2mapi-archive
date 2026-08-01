-- Supply admission, rotation safety, explicit channel migration, and durable
-- pool retirement. These are operational controls only; no billing data lives
-- in this migration.
ALTER TABLE upstream_pools
    ADD COLUMN IF NOT EXISTS safety_stock_threshold INTEGER NOT NULL DEFAULT 0
        CHECK (safety_stock_threshold >= 0);

ALTER TABLE upstream_channels
    ADD COLUMN IF NOT EXISTS inventory_state TEXT;
UPDATE upstream_channels SET inventory_state='ready' WHERE inventory_state IS NULL;
ALTER TABLE upstream_channels
    ALTER COLUMN inventory_state SET DEFAULT 'draft',
    ALTER COLUMN inventory_state SET NOT NULL;
ALTER TABLE upstream_channels
    DROP CONSTRAINT IF EXISTS upstream_channels_inventory_state_check;
ALTER TABLE upstream_channels
    ADD CONSTRAINT upstream_channels_inventory_state_check
        CHECK (inventory_state IN ('draft','testing','ready','quarantined','retired'));

-- Existing pools keep their current state; direct database inserts and product
-- creation paths start closed until an operator explicitly activates them.
ALTER TABLE upstream_pools ALTER COLUMN status SET DEFAULT 'maintenance';

CREATE INDEX IF NOT EXISTS idx_upstream_channels_inventory
    ON upstream_channels (pool_id, inventory_state, status);

ALTER TABLE upstream_key_deliveries
    ADD COLUMN IF NOT EXISTS previous_secret_ref TEXT,
    ADD COLUMN IF NOT EXISTS previous_masked_value TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS previous_key_version BIGINT NOT NULL DEFAULT 0
        CHECK (previous_key_version >= 0),
    ADD COLUMN IF NOT EXISTS rotation_status TEXT NOT NULL DEFAULT 'stable'
        CHECK (rotation_status IN ('stable','deploying','rolling_back','finalizing')),
    ADD COLUMN IF NOT EXISTS rotation_resume_status TEXT NOT NULL DEFAULT ''
        CHECK (rotation_resume_status IN ('','deploying','rolling_back')),
    ADD COLUMN IF NOT EXISTS rotation_started_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS uq_upstream_key_previous_secret_ref
    ON upstream_key_deliveries (previous_secret_ref)
    WHERE previous_secret_ref IS NOT NULL;

CREATE TABLE IF NOT EXISTS upstream_channel_migrations (
    id           TEXT PRIMARY KEY,
    channel_id   TEXT NOT NULL REFERENCES upstream_channels(id) ON DELETE RESTRICT,
    from_pool_id TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE RESTRICT,
    to_pool_id   TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE RESTRICT,
    reason       TEXT NOT NULL,
    actor_user_id BIGINT NOT NULL DEFAULT 0,
    migrated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_pool_id <> to_pool_id),
    CHECK (BTRIM(reason) <> '')
);
CREATE INDEX IF NOT EXISTS idx_upstream_channel_migrations_channel
    ON upstream_channel_migrations (channel_id, migrated_at DESC);

CREATE TABLE IF NOT EXISTS pool_retirement_jobs (
    id              TEXT PRIMARY KEY,
    pool_id         TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE RESTRICT,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','partial','finalizing','completed')),
    total_plans     INTEGER NOT NULL DEFAULT 0 CHECK (total_plans >= 0),
    completed_plans INTEGER NOT NULL DEFAULT 0 CHECK (completed_plans >= 0),
    failed_plans    INTEGER NOT NULL DEFAULT 0 CHECK (failed_plans >= 0),
    last_error      TEXT NOT NULL DEFAULT '',
    created_by      BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_pool_retirement_active
    ON pool_retirement_jobs (pool_id)
    WHERE status IN ('pending','running','partial','finalizing');

CREATE TABLE IF NOT EXISTS pool_retirement_items (
    job_id      TEXT NOT NULL REFERENCES pool_retirement_jobs(id) ON DELETE CASCADE,
    plan_id     TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','completed','failed')),
    last_error  TEXT NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, plan_id)
);
CREATE INDEX IF NOT EXISTS idx_pool_retirement_items_work
    ON pool_retirement_items (job_id, status, plan_id);
