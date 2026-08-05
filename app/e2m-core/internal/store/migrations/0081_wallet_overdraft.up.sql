BEGIN;

-- Settlement charges the true metered cost even when it exceeds the hold, so
-- a wallet can end a request in debt. The debt is carried as a negative
-- available balance and is offset by the next credit (recharge, redeem, or an
-- administrator adjustment). Reservations still refuse a wallet at or below
-- zero, so a debtor cannot start new requests until the debt is cleared.
ALTER TABLE wallet_accounts
    DROP CONSTRAINT IF EXISTS wallet_accounts_available_micros_check;

COMMIT;
