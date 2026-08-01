-- One instance/pool scheduling domain has exactly one RoutePlan owner. Without
-- this invariant two plan-scoped generations could enqueue opposite mutations
-- for the same gateway account without a shared ordering domain.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM route_plans
        WHERE instance_id <> '' AND pool_id <> ''
        GROUP BY instance_id, pool_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'duplicate route plans exist for an instance/pool; consolidate them before migration 0034';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_route_plans_instance_pool
    ON route_plans (instance_id, pool_id)
    WHERE instance_id <> '' AND pool_id <> '';
