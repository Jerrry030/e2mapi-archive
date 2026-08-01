-- Durable downstream-scoped quality circuit state. A provider regression is
-- evaluated independently for each route plan; this table must never be keyed
-- only by channel/provider or a local overload could eject every downstream.
CREATE TABLE IF NOT EXISTS quality_circuit_runtimes (
    plan_id                    TEXT NOT NULL,
    channel_id                 TEXT NOT NULL,
    state                      TEXT NOT NULL DEFAULT 'closed',
    opened_at                  TIMESTAMPTZ,
    probe_after                TIMESTAMPTZ,
    half_open_since            TIMESTAMPTZ,
    last_probe_at              TIMESTAMPTZ,
    last_transition_at         TIMESTAMPTZ,
    open_count                 INTEGER NOT NULL DEFAULT 0,
    consecutive_probe_successes INTEGER NOT NULL DEFAULT 0,
    last_score                 DOUBLE PRECISION NOT NULL DEFAULT 100,
    last_reason_code           TEXT NOT NULL DEFAULT '',
    last_reason_text           TEXT NOT NULL DEFAULT '',
    version                    BIGINT NOT NULL DEFAULT 1,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plan_id, channel_id),
    CONSTRAINT quality_circuit_state_check
        CHECK (state IN ('closed', 'open', 'half_open')),
    CONSTRAINT quality_circuit_counts_check
        CHECK (open_count >= 0 AND consecutive_probe_successes >= 0),
    CONSTRAINT quality_circuit_score_check
        CHECK (last_score >= 0 AND last_score <= 100),
    CONSTRAINT quality_circuit_version_check
        CHECK (version > 0)
);

CREATE INDEX IF NOT EXISTS idx_quality_circuit_probe_due
    ON quality_circuit_runtimes (probe_after, plan_id, channel_id)
    WHERE state = 'open' AND probe_after IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_quality_circuit_channel
    ON quality_circuit_runtimes (channel_id, state);
