-- Channel health metrics: append-only per-request/probe observations and the
-- windowed snapshots aggregated from them. These power health-driven automatic
-- upstream switching (strategy scoring reads snapshots, never raw observations).
CREATE TABLE IF NOT EXISTS channel_observations (
    id             TEXT PRIMARY KEY,
    channel_id     TEXT NOT NULL,
    instance_id    TEXT NOT NULL DEFAULT '',
    pool_id        TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    success        BOOLEAN NOT NULL,
    status_code    INTEGER NOT NULL DEFAULT 0,
    error_type     TEXT NOT NULL DEFAULT '',
    first_token_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_ms       DOUBLE PRECISION NOT NULL DEFAULT 0,
    input_tokens   BIGINT NOT NULL DEFAULT 0,
    output_tokens  BIGINT NOT NULL DEFAULT 0,
    estimated_cost DOUBLE PRECISION NOT NULL DEFAULT 0,
    source         TEXT NOT NULL DEFAULT 'passive',
    observed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_channel_obs_channel ON channel_observations (channel_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_obs_instance ON channel_observations (instance_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_obs_pool ON channel_observations (pool_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS channel_health_snapshots (
    id                            TEXT PRIMARY KEY,
    channel_id                    TEXT NOT NULL,
    pool_id                       TEXT NOT NULL DEFAULT '',
    instance_id                   TEXT NOT NULL DEFAULT '',
    "window"                      TEXT NOT NULL,
    sample_count                  INTEGER NOT NULL DEFAULT 0,
    success_rate                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    ttft_p50                      DOUBLE PRECISION NOT NULL DEFAULT 0,
    ttft_p95                      DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_p50                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_p95                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    error_rate                    DOUBLE PRECISION NOT NULL DEFAULT 0,
    timeout_rate                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    rate_limit_rate               DOUBLE PRECISION NOT NULL DEFAULT 0,
    estimated_cost_per_1k_tokens  DOUBLE PRECISION NOT NULL DEFAULT 0,
    health_score                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    quality_score                 DOUBLE PRECISION NOT NULL DEFAULT 0,
    success_score                 DOUBLE PRECISION NOT NULL DEFAULT 0,
    ttft_score                    DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_score                DOUBLE PRECISION NOT NULL DEFAULT 0,
    stability_score               DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_score                    DOUBLE PRECISION NOT NULL DEFAULT 0,
    risk_score                    DOUBLE PRECISION NOT NULL DEFAULT 0,
    health_state                  TEXT NOT NULL DEFAULT 'unknown',
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, "window")
);
CREATE INDEX IF NOT EXISTS idx_channel_snap_pool ON channel_health_snapshots (pool_id, "window");
CREATE INDEX IF NOT EXISTS idx_channel_snap_instance ON channel_health_snapshots (instance_id, "window");
