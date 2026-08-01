-- Durable local collection orders. Provider display fields are immutable
-- snapshots only; provider config and credentials are never copied here.
CREATE TABLE IF NOT EXISTS payment_orders (
    id                    TEXT PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    user_email            TEXT NOT NULL DEFAULT '',
    user_name             TEXT NOT NULL DEFAULT '',
    amount                NUMERIC(20,2) NOT NULL CHECK (amount >= 0),
    pay_amount            NUMERIC(20,2) NOT NULL CHECK (pay_amount >= 0),
    fee_rate              NUMERIC(10,4) NOT NULL DEFAULT 0 CHECK (fee_rate >= 0),
    currency              VARCHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_type          VARCHAR(30) NOT NULL CHECK (payment_type IN ('alipay','wxpay','alipay_direct','wxpay_direct','card','link','stripe','airwallex','easypay')),
    out_trade_no          VARCHAR(64) NOT NULL UNIQUE CHECK (out_trade_no ~ '^[A-Za-z0-9_-]+$'),
    payment_trade_no      VARCHAR(128) NOT NULL DEFAULT '',
    order_type            VARCHAR(20) NOT NULL DEFAULT 'balance' CHECK (order_type IN ('balance','subscription')),
    provider_instance_id  TEXT,
    provider_key          VARCHAR(30) CHECK (provider_key IS NULL OR provider_key IN ('easypay','alipay','wxpay','stripe','airwallex')),
    provider_name         TEXT NOT NULL DEFAULT '',
    provider_snapshot     JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(provider_snapshot) = 'object'),
    status                VARCHAR(30) NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                              'PENDING','PAID','RECHARGING','COMPLETED','EXPIRED','CANCELLED','FAILED',
                              'REFUND_REQUESTED','REFUNDING','REFUND_PENDING','PARTIALLY_REFUNDED','REFUNDED','REFUND_FAILED')),
    refund_amount         NUMERIC(20,2) NOT NULL DEFAULT 0 CHECK (refund_amount >= 0),
    refund_reason         TEXT NOT NULL DEFAULT '',
    refund_requested_at   TIMESTAMPTZ,
    refund_requested_by   TEXT NOT NULL DEFAULT '',
    refund_request_reason TEXT NOT NULL DEFAULT '',
    expires_at            TIMESTAMPTZ NOT NULL,
    paid_at               TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    failed_at             TIMESTAMPTZ,
    failed_reason         TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_payment_orders_created_id
    ON payment_orders (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_user_created
    ON payment_orders (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status_created
    ON payment_orders (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_type_created
    ON payment_orders (payment_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_order_type_created
    ON payment_orders (order_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_provider_created
    ON payment_orders (provider_instance_id, created_at DESC)
    WHERE provider_instance_id IS NOT NULL;