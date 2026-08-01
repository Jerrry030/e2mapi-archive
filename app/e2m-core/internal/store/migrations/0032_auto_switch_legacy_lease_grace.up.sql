-- Rows created by pre-lease Core versions have no durable deadline and their
-- updated_at may reflect a skewed application clock. Give every such applying
-- row one database-clock grace period before a new scheduler may repair it.
UPDATE auto_switch_decisions
SET lease_until = statement_timestamp() + interval '2 minutes',
    lease_version = CASE WHEN lease_version < 1 THEN 1 ELSE lease_version END,
    updated_at = statement_timestamp()
WHERE status = 'applying' AND lease_until IS NULL;
