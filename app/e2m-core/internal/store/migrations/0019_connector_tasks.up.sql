-- Typed outbound connector task queue. Connector identity tables are created
-- first by 0019 so task ownership can be enforced by foreign keys.

CREATE TABLE IF NOT EXISTS connectors (
    connector_id     TEXT PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id      TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL CHECK (status IN ('online', 'offline', 'revoked')),
    token_hash       TEXT NOT NULL,
    version          TEXT NOT NULL CHECK (version <> '' AND char_length(version) <= 64),
    protocol_version INT NOT NULL DEFAULT 1 CHECK (protocol_version = 1),
    gateway_state    JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_seen_at     TIMESTAMPTZ,
    revoked_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status = 'revoked' OR token_hash <> ''),
    UNIQUE (connector_id, user_id, instance_id),
    CONSTRAINT connectors_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_connectors_user_status
    ON connectors (user_id, status);

CREATE UNIQUE INDEX IF NOT EXISTS idx_connectors_token_hash
    ON connectors (token_hash)
    WHERE token_hash <> '';

ALTER TABLE instances
    ADD CONSTRAINT instances_connector_binding_fkey
    FOREIGN KEY (connector_id, user_id, id)
    REFERENCES connectors(connector_id, user_id, instance_id)
    ON DELETE SET NULL (connector_id);

CREATE TABLE IF NOT EXISTS connector_enrollments (
    id            TEXT PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id   TEXT NOT NULL,
    connector_id  TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    token_hash    TEXT NOT NULL UNIQUE,
    expires_at    TIMESTAMPTZ NOT NULL,
    used_at       TIMESTAMPTZ,
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connector_enrollments_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_connector_enrollments_active_connector
    ON connector_enrollments (connector_id)
    WHERE used_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_connector_enrollments_active_instance
    ON connector_enrollments (instance_id)
    WHERE used_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_connector_enrollments_user_created
    ON connector_enrollments (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS connector_tasks (
    id              TEXT PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id     TEXT NOT NULL,
    connector_id    TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN (
                        'gateway.health.get',
                        'gateway.accounts.list',
                        'gateway.account.schedulable.set',
                        'gateway.account.switch'
                    )),
    schema_version  INT NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    risk_level      TEXT NOT NULL CHECK (risk_level IN ('L0', 'L1', 'L2', 'L3')),
    status          TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'succeeded', 'failed', 'expired')),
    input           JSONB NOT NULL DEFAULT 'null'::jsonb,
    result          JSONB NOT NULL DEFAULT 'null'::jsonb,
    error           JSONB NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key TEXT NOT NULL DEFAULT '',
    lease_owner     TEXT NOT NULL DEFAULT '',
    lease_nonce     TEXT NOT NULL DEFAULT '',
    lease_until     TIMESTAMPTZ,
    attempts        INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts    INT NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connector_tasks_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (connector_id, user_id, instance_id)
        REFERENCES connectors(connector_id, user_id, instance_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_connector_tasks_connector_status
    ON connector_tasks (connector_id, status, created_at);

CREATE INDEX IF NOT EXISTS idx_connector_tasks_user_created
    ON connector_tasks (user_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_connector_tasks_idempotency
    ON connector_tasks (connector_id, idempotency_key)
    WHERE idempotency_key <> '' AND status IN ('pending', 'leased');
