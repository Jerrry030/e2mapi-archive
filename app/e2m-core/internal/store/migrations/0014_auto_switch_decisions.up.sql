-- Auto-switch decisions (Phase 4): one row per health-driven automatic switch
-- intent and its dry-run/apply/observe/rollback outcome. The decision layer
-- never bypasses reconcile; this table is the auditable record of what the
-- orchestrator decided, why, how risky it was, and how it resolved. dry_run
-- (the reconcile preview it was graded on) is stored as JSONB.
CREATE TABLE IF NOT EXISTS auto_switch_decisions (
    id               TEXT PRIMARY KEY,
    user_id        BIGINT NOT NULL DEFAULT 0,
    plan_id          TEXT NOT NULL,
    instance_id      TEXT NOT NULL DEFAULT '',
    pool_id          TEXT NOT NULL DEFAULT '',
    strategy         TEXT NOT NULL DEFAULT '',
    trigger          TEXT NOT NULL DEFAULT 'auto',
    trigger_reason   TEXT NOT NULL DEFAULT '',
    from_channel_id  TEXT NOT NULL DEFAULT '',
    to_channel_id    TEXT NOT NULL DEFAULT '',
    risk_level       TEXT NOT NULL DEFAULT '',
    risk_reason      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,
    auto_applied     BOOLEAN NOT NULL DEFAULT false,
    fingerprint      TEXT NOT NULL DEFAULT '',
    dry_run          JSONB NOT NULL DEFAULT '{}',
    error            TEXT NOT NULL DEFAULT '',
    observation_note TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at       TIMESTAMPTZ,
    observe_until    TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_auto_switch_plan ON auto_switch_decisions (plan_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_auto_switch_user ON auto_switch_decisions (user_id, created_at DESC);
-- One live decision per (plan, fingerprint): the idempotency guard for a
-- failure window. Terminal states are excluded so a resolved decision never
-- blocks a fresh switch.
CREATE UNIQUE INDEX IF NOT EXISTS uq_auto_switch_active_fp ON auto_switch_decisions (plan_id, fingerprint)
    WHERE status IN ('proposed', 'applying', 'observing');
