BEGIN;

-- Restoring the non-negative constraint requires clearing outstanding debt
-- first; refuse rather than silently rewriting customer balances.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wallet_accounts WHERE available_micros < 0) THEN
        RAISE EXCEPTION 'cannot downgrade 0081 while wallets carry debt: settle or write off negative available_micros first';
    END IF;
END $$;

ALTER TABLE wallet_accounts
    ADD CONSTRAINT wallet_accounts_available_micros_check CHECK (available_micros >= 0);

COMMIT;
