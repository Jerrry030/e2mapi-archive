-- W6: approval requests — L2/L3 actions gated on a human decision.
CREATE TABLE IF NOT EXISTS approval_requests (
    id           TEXT PRIMARY KEY,
    user_id      BIGINT NOT NULL,
    instance_id  TEXT NOT NULL,
    action       TEXT NOT NULL,
    risk_level   TEXT NOT NULL,
    account_ids  JSONB NOT NULL DEFAULT '[]'::jsonb,
    schedulable  BOOLEAN,
    reason       TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    requested_by TEXT NOT NULL DEFAULT '',
    decided_by   TEXT NOT NULL DEFAULT '',
    decided_at   TIMESTAMPTZ,
    result_note  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT approval_requests_user_fkey
        FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT approval_requests_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_approvals_user_status ON approval_requests (user_id, status);
