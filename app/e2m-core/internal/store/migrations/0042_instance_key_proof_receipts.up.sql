-- A challenge proof is true only for the Connector attached to one concrete
-- gateway instance. The delivery row's legacy proof fields remain a summary,
-- while this table is the source of truth for publish/reveal readiness.
CREATE TABLE upstream_key_proof_receipts (
    channel_id   TEXT NOT NULL REFERENCES upstream_key_deliveries(channel_id) ON DELETE CASCADE,
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    key_version  BIGINT NOT NULL CHECK (key_version > 0),
    connector_id TEXT NOT NULL CHECK (connector_id <> ''),
    status        TEXT NOT NULL CHECK (status IN ('unverified', 'verified', 'mismatch')),
    checked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, instance_id)
);

CREATE INDEX idx_upstream_key_proof_receipts_instance
    ON upstream_key_proof_receipts (instance_id, status, checked_at DESC);
