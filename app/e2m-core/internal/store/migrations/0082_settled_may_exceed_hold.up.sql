BEGIN;

-- 0081 let a wallet carry debt, but two table-level constraints still encoded
-- the older invariant that a request can never be charged more than it held.
-- With metered settlement charging the true cost, settled_micros legitimately
-- exceeds reserved_micros; leaving these in place silently forced every such
-- request onto the conservative fallback, undercharging it.
ALTER TABLE wallet_reservations
    DROP CONSTRAINT IF EXISTS wallet_reservations_check;

ALTER TABLE supply_usage_records
    DROP CONSTRAINT IF EXISTS supply_usage_records_check;

COMMIT;
