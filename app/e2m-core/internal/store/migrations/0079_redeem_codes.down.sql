BEGIN;

ALTER TABLE wallet_journals
    DROP CONSTRAINT IF EXISTS wallet_journals_kind_check;
ALTER TABLE wallet_journals
    ADD CONSTRAINT wallet_journals_kind_check
    CHECK (kind IN ('recharge','adjustment','reserve','settle','release','refund'));

DROP TABLE IF EXISTS redeem_codes;

COMMIT;
