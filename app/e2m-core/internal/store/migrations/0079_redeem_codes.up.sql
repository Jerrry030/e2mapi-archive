BEGIN;

-- Redeem codes are bearer instruments: only the SHA-256 hash of the plaintext
-- code is stored, plus a short display prefix for operator tables.
CREATE TABLE IF NOT EXISTS redeem_codes (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL CHECK (type IN ('balance','invitation')),
    code_hash TEXT NOT NULL UNIQUE,
    code_prefix TEXT NOT NULL DEFAULT '',
    currency TEXT NOT NULL DEFAULT 'CNY',
    amount_micros BIGINT NOT NULL DEFAULT 0 CHECK (amount_micros >= 0),
    status TEXT NOT NULL CHECK (status IN ('unused','used','disabled','expired')),
    batch_id TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    used_by BIGINT,
    used_at TIMESTAMPTZ,
    created_by BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch ON redeem_codes(batch_id);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_type_status ON redeem_codes(type, status);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_used_by ON redeem_codes(used_by) WHERE used_by IS NOT NULL;

ALTER TABLE wallet_journals
    DROP CONSTRAINT IF EXISTS wallet_journals_kind_check;
ALTER TABLE wallet_journals
    ADD CONSTRAINT wallet_journals_kind_check
    CHECK (kind IN ('recharge','adjustment','redeem','reserve','settle','release','refund'));

COMMIT;
