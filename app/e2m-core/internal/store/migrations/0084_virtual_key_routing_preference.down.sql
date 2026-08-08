BEGIN;

ALTER TABLE virtual_keys DROP COLUMN IF EXISTS routing_preference;

COMMIT;
