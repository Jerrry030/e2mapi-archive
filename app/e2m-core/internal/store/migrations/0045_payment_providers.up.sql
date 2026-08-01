-- Platform-managed collection provider instances. Credentials are stored in
-- the Vault; secret_refs contains opaque references only, never plaintext.
CREATE TABLE IF NOT EXISTS payment_provider_instances (
    id                 TEXT PRIMARY KEY,
    provider_key       TEXT NOT NULL CHECK (provider_key IN ('easypay', 'alipay', 'wxpay', 'stripe', 'airwallex')),
    name               TEXT NOT NULL,
    config             JSONB NOT NULL DEFAULT '{}',
    secret_refs        JSONB NOT NULL DEFAULT '{}',
    supported_types    JSONB NOT NULL DEFAULT '[]',
    enabled            BOOLEAN NOT NULL DEFAULT false,
    payment_mode       TEXT NOT NULL DEFAULT '',
    sort_order         INTEGER NOT NULL DEFAULT 0,
    limits             JSONB NOT NULL DEFAULT '{}',
    refund_enabled     BOOLEAN NOT NULL DEFAULT false,
    allow_user_refund  BOOLEAN NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT allow_user_refund OR refund_enabled)
);

CREATE INDEX IF NOT EXISTS idx_payment_provider_instances_order
    ON payment_provider_instances (sort_order, created_at);
