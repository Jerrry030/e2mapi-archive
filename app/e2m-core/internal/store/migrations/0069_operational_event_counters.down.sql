-- This migration contains monotonic duplicate-attempt and safety-event facts
-- that cannot be reconstructed from business tables. A schema downgrade would
-- silently erase them, so 0069 is intentionally forward-only. Roll back the
-- current application forward. A pre-0069 application is not compatible with
-- the schema-0069 writer fence; reverting to it requires restoring the entire
-- database, including migration metadata, from a verified pre-upgrade backup.
DO $$
BEGIN
    RAISE EXCEPTION USING
        ERRCODE = '0A000',
        MESSAGE = 'migration 0069 is forward-only',
        HINT = 'roll forward with a current application or restore the entire database from a verified pre-0069 backup';
END;
$$;
