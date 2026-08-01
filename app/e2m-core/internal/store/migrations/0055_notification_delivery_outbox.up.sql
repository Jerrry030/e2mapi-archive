-- Durable notification outbox. It stores only the route identity and a
-- non-secret event snapshot; destination URLs and credentials remain in the
-- notification route/Vault boundary and are resolved at send time.
CREATE TABLE IF NOT EXISTS notification_deliveries (
    id                 TEXT PRIMARY KEY,
    -- Keep the immutable delivery audit even if the owning user is removed.
    user_id            BIGINT NOT NULL,
    route_id           TEXT NOT NULL,
    route_name         TEXT NOT NULL,
    target_ref         TEXT NOT NULL,
    template           TEXT NOT NULL DEFAULT '',
    channel            TEXT NOT NULL CHECK (channel IN ('feishu','qq','webhook')),
    kind               TEXT NOT NULL CHECK (kind IN ('event','test')),
    status             TEXT NOT NULL CHECK (status IN ('pending','processing','retrying','succeeded','failed')),
    event_level        TEXT NOT NULL CHECK (event_level IN ('L0','L1','L2','L3')),
    risk_level         TEXT NOT NULL CHECK (risk_level IN ('L0','L1','L2','L3')),
    result             TEXT NOT NULL DEFAULT '',
    instance_id        TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL DEFAULT '',
    text               TEXT NOT NULL DEFAULT '',
    fields             JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempts           INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts       INTEGER NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code    TEXT NOT NULL DEFAULT '',
    last_error_message TEXT NOT NULL DEFAULT '',
    lease_owner        TEXT NOT NULL DEFAULT '',
    lease_until        TIMESTAMPTZ,
    lease_version      BIGINT NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
    sent_at            TIMESTAMPTZ,
    retried_from_id    TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_notification_delivery_work
    ON notification_deliveries (status, next_attempt_at, lease_until);
CREATE INDEX IF NOT EXISTS idx_notification_delivery_user_created
    ON notification_deliveries (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_delivery_route_created
    ON notification_deliveries (route_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_delivery_retry_source
    ON notification_deliveries (retried_from_id)
    WHERE retried_from_id IS NOT NULL;
