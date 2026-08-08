BEGIN;

-- Per-attempt data-plane telemetry. NULL means "not observed" (failed before
-- the first byte, or the row predates telemetry) — never 0ms.
ALTER TABLE supply_usage_records
    ADD COLUMN IF NOT EXISTS first_token_ms BIGINT CHECK (first_token_ms IS NULL OR first_token_ms >= 0),
    ADD COLUMN IF NOT EXISTS duration_ms BIGINT CHECK (duration_ms IS NULL OR duration_ms >= 0);

-- Five-minute reliability buckets per upstream channel, written inside the
-- settlement transaction. requests counts only success/failure samples;
-- neutral endings (client disconnects, deterministic upstream rejections) are
-- excluded so they cannot move a channel's success rate. No FK on channel_id:
-- channel deletes preserve accounting history, and stats follow that rule.
CREATE TABLE IF NOT EXISTS supply_channel_stats (
    channel_id TEXT NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0 CHECK (requests >= 0),
    failures BIGINT NOT NULL DEFAULT 0 CHECK (failures >= 0),
    ttft_sum_ms BIGINT NOT NULL DEFAULT 0 CHECK (ttft_sum_ms >= 0),
    ttft_samples BIGINT NOT NULL DEFAULT 0 CHECK (ttft_samples >= 0),
    duration_sum_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_sum_ms >= 0),
    duration_samples BIGINT NOT NULL DEFAULT 0 CHECK (duration_samples >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, bucket_start),
    CHECK (failures <= requests)
);

COMMIT;
