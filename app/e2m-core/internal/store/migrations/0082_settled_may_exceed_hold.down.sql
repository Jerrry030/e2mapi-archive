BEGIN;

-- Restoring the ceiling requires that no settled request currently exceeds its
-- hold; refuse rather than silently rewriting settled amounts.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wallet_reservations WHERE settled_micros > reserved_micros)
       OR EXISTS (SELECT 1 FROM supply_usage_records WHERE settled_micros > reserved_micros) THEN
        RAISE EXCEPTION 'cannot downgrade 0082 while settled amounts exceed their hold';
    END IF;
END $$;

ALTER TABLE wallet_reservations
    ADD CONSTRAINT wallet_reservations_check CHECK (settled_micros <= reserved_micros);

ALTER TABLE supply_usage_records
    ADD CONSTRAINT supply_usage_records_check CHECK (settled_micros <= reserved_micros);

COMMIT;
