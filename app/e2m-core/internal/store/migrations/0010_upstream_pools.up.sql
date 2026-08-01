-- Managed upstream layer: platform-curated pools/channels, per-instance route
-- plans (desired state), and published bindings (reconcile paper trail).
-- Bindings are opaque Connector-local IDs; plaintext never enters Core.
CREATE TABLE IF NOT EXISTS upstream_pools (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    provider    TEXT NOT NULL DEFAULT '',
    models      JSONB NOT NULL DEFAULT '[]',
    region      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    description TEXT NOT NULL DEFAULT '',
    labels      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS upstream_channels (
    id             TEXT PRIMARY KEY,
    pool_id        TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    provider       TEXT NOT NULL DEFAULT '',
    models         JSONB NOT NULL DEFAULT '[]',
    groups         JSONB NOT NULL DEFAULT '[]',
    credential_binding_id TEXT NOT NULL DEFAULT '',
    proxy_binding_id      TEXT NOT NULL DEFAULT '',
    priority       INTEGER NOT NULL DEFAULT 0,
    weight         INTEGER NOT NULL DEFAULT 0,
    cost_hint      DOUBLE PRECISION NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'active',
    labels         JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_upstream_channels_pool ON upstream_channels (pool_id);

CREATE TABLE IF NOT EXISTS route_plans (
    id           TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL,
    instance_id  TEXT NOT NULL,
    pool_id      TEXT NOT NULL,
    tier         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'draft',
    max_channels INTEGER NOT NULL DEFAULT 0,
    labels       JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_route_plans_user ON route_plans (user_id);
CREATE INDEX IF NOT EXISTS idx_route_plans_instance ON route_plans (instance_id);

CREATE TABLE IF NOT EXISTS published_bindings (
    id          TEXT PRIMARY KEY,
    plan_id     TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    remote_id   TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'pending',
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id, channel_id)
);
CREATE INDEX IF NOT EXISTS idx_published_bindings_plan ON published_bindings (plan_id);
