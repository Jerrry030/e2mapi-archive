-- Reconcile-run history: one row per publish/reconcile execution (dry-run,
-- apply, rollback). Written by the publish engine's unified execution layer so
-- background/automatic switches (health-driven) are recorded too, not just
-- operator-triggered HTTP calls. Actions are stored as JSONB so the console can
-- render what changed without re-deriving the diff.
CREATE TABLE IF NOT EXISTS reconcile_runs (
    id          TEXT PRIMARY KEY,
    plan_id     TEXT NOT NULL,
    instance_id TEXT NOT NULL DEFAULT '',
    user_id   BIGINT NOT NULL DEFAULT 0,
    kind        TEXT NOT NULL,
    trigger     TEXT NOT NULL DEFAULT 'manual',
    actor_type  TEXT NOT NULL DEFAULT '',
    actor_id    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    actions     JSONB NOT NULL DEFAULT '[]',
    error       TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_reconcile_runs_plan ON reconcile_runs (plan_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_reconcile_runs_user ON reconcile_runs (user_id, started_at DESC);
