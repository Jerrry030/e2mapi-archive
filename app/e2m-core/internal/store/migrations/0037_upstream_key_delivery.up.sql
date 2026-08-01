-- A platform-managed key has two deliberately separate references:
-- credential_binding_id is resolved by the customer-side Connector, while
-- this Core-vault ref exists only for controlled delivery to the permanent
-- owner. Neither value is returned by ordinary list APIs.
CREATE TABLE IF NOT EXISTS upstream_key_deliveries (
    id            TEXT PRIMARY KEY,
    channel_id    TEXT NOT NULL UNIQUE REFERENCES upstream_channels(id) ON DELETE RESTRICT,
    secret_ref    TEXT NOT NULL UNIQUE,
    masked_value  TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_upstream_key_deliveries_channel
    ON upstream_key_deliveries (channel_id);
