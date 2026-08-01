-- A monotonically increasing generation fences stale applying workers after a
-- lease expires and another Core instance takes ownership.
ALTER TABLE auto_switch_decisions
    ADD COLUMN IF NOT EXISTS lease_version BIGINT NOT NULL DEFAULT 0;
