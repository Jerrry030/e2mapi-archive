-- Stable supply-source identity is independent from the model vendor. Multiple
-- credentials from one source may exist in the shared catalog, while a route
-- plan publishes at most one of them to a downstream user.
ALTER TABLE upstream_channels
    ADD COLUMN IF NOT EXISTS source_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_upstream_channels_source
    ON upstream_channels (source_id);

-- Key ownership is permanent: disabling, revoking, or recovering a binding
-- never releases the concrete channel for another user. first_plan_id records
-- the original assignment while the same user may reference the key from more
-- than one route plan/instance.
CREATE TABLE IF NOT EXISTS upstream_channel_allocations (
    channel_id   TEXT PRIMARY KEY,
    source_id    TEXT NOT NULL,
    user_id      BIGINT NOT NULL,
    first_plan_id TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_upstream_channel_allocations_user
    ON upstream_channel_allocations (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_upstream_channel_allocations_user_source
    ON upstream_channel_allocations (user_id, source_id);

DO $$
BEGIN
    IF EXISTS (
        SELECT pb.channel_id
          FROM published_bindings pb
          JOIN route_plans rp ON rp.id = pb.plan_id
         GROUP BY pb.channel_id
        HAVING COUNT(DISTINCT rp.user_id) > 1
    ) THEN
        RAISE EXCEPTION 'one upstream channel is already bound to multiple users; resolve ownership before migration 0028';
    END IF;
END $$;

INSERT INTO upstream_channel_allocations (channel_id, source_id, user_id, first_plan_id, created_at)
SELECT DISTINCT ON (pb.channel_id)
       pb.channel_id,
       COALESCE(NULLIF(BTRIM(uc.source_id), ''), pb.channel_id),
       rp.user_id, pb.plan_id, pb.created_at
  FROM published_bindings pb
  JOIN route_plans rp ON rp.id = pb.plan_id
  LEFT JOIN upstream_channels uc ON uc.id = pb.channel_id
 ORDER BY pb.channel_id, pb.created_at, pb.plan_id
ON CONFLICT (channel_id) DO NOTHING;
