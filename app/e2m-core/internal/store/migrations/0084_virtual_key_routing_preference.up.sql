BEGIN;

-- Per-key routing intent for the platform pool. NULL follows the platform
-- default order, so every existing key keeps its exact current behaviour.
-- The vocabulary matches the owner routing preference product choices.
ALTER TABLE virtual_keys
    ADD COLUMN IF NOT EXISTS routing_preference TEXT
    CHECK (routing_preference IS NULL OR routing_preference IN ('smart_auto','price_first','speed_first','success_first'));

COMMIT;
