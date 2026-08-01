-- E2M Core initial schema (W1).
-- User accounts are the resource ownership boundary. Managed instances,
-- supply offers, audit, notification routes.
-- Plaintext credentials are NEVER stored here. Gateway credentials live in the
-- customer-side connector runtime; upstream/supply credentials use refs.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    roles         TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_roles_non_empty CHECK (array_length(roles, 1) >= 1),
    CONSTRAINT users_roles_allowed CHECK (roles <@ ARRAY['admin','client','supplier']::TEXT[]),
    CONSTRAINT users_platform_role_shape CHECK (
        NOT ('admin' = ANY(roles)) OR array_length(roles, 1) = 1
    )
);

CREATE TABLE IF NOT EXISTS instances (
    id            TEXT PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('sub2api', 'newapi', 'cpa')),
    status        TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'active', 'degraded', 'offline', 'maintenance')),
    connector_id  TEXT UNIQUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT instances_id_user_key UNIQUE (id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_instances_user ON instances (user_id);

CREATE TABLE IF NOT EXISTS supply_offers (
    id               TEXT PRIMARY KEY,
    supplier_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('oauth_subscription', 'api_key')),
    provider         TEXT NOT NULL DEFAULT '',
    credential_ref   TEXT NOT NULL,
    proxy_ref        TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending',
    quota            BIGINT NOT NULL DEFAULT 0,
    unit_price       TEXT NOT NULL DEFAULT '',
    labels           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT supply_offers_id_supplier_key UNIQUE (id, supplier_user_id)
);
CREATE INDEX IF NOT EXISTS idx_supply_offers_supplier ON supply_offers (supplier_user_id);

CREATE TABLE IF NOT EXISTS operation_audits (
    id                   TEXT PRIMARY KEY,
    user_id              BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id          TEXT NOT NULL DEFAULT '',
    actor_type           TEXT NOT NULL,
    actor_id             TEXT NOT NULL,
    action               TEXT NOT NULL,
    risk_level           TEXT NOT NULL,
    target_type          TEXT NOT NULL DEFAULT '',
    target_id            TEXT NOT NULL DEFAULT '',
    request_payload_hash TEXT NOT NULL DEFAULT '',
    result               TEXT NOT NULL,
    error_message        TEXT NOT NULL DEFAULT '',
    approval_id          TEXT NOT NULL DEFAULT '',
    workflow_run_id      TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audits_user_created ON operation_audits (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_routes (
    id               TEXT PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    channel          TEXT NOT NULL,
    target_ref       TEXT NOT NULL,
    min_risk_level   TEXT NOT NULL,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    quiet_window     TEXT NOT NULL DEFAULT '',
    escalation_after TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_routes_user ON notification_routes (user_id);
