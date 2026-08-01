DROP INDEX IF EXISTS idx_upstream_channels_source;

DROP TABLE IF EXISTS upstream_channel_allocations;

ALTER TABLE upstream_channels
    DROP COLUMN IF EXISTS source_id;
