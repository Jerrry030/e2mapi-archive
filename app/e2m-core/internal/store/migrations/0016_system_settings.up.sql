-- Platform-admin system settings. Values are JSONB so narrow settings surfaces
-- can evolve without schema churn; sensitive values must never be returned by
-- public/admin API handlers unless explicitly safe.
CREATE TABLE IF NOT EXISTS system_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
