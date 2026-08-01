-- Separate an operation's authorization risk from the severity of its outcome.
-- Structured details are allowlisted, non-secret labels used by the console.
ALTER TABLE operation_audits
    ADD COLUMN IF NOT EXISTS event_level TEXT,
    ADD COLUMN IF NOT EXISTS details JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE operation_audits
   SET event_level=CASE result
       WHEN 'running' THEN 'L0'
       WHEN 'retrying' THEN 'L1'
       WHEN 'paused' THEN 'L1'
       WHEN 'rejected' THEN 'L1'
       WHEN 'failed' THEN 'L2'
       WHEN 'accepted' THEN CASE WHEN risk_level='L0' THEN 'L0' ELSE 'L1' END
       WHEN 'success' THEN CASE WHEN risk_level='L0' THEN 'L0' ELSE 'L1' END
       ELSE CASE WHEN risk_level='L0' THEN 'L0' ELSE 'L1' END
   END
 WHERE event_level IS NULL OR event_level='';

ALTER TABLE operation_audits
    ALTER COLUMN event_level SET NOT NULL,
    ALTER COLUMN event_level SET DEFAULT 'L0',
    DROP CONSTRAINT IF EXISTS operation_audits_event_level_check;
ALTER TABLE operation_audits
    ADD CONSTRAINT operation_audits_event_level_check
        CHECK (event_level IN ('L0','L1','L2','L3'));

ALTER TABLE notification_routes
    ADD COLUMN IF NOT EXISTS min_event_level TEXT;
UPDATE notification_routes
   SET min_event_level=min_risk_level
 WHERE min_event_level IS NULL OR min_event_level='';
ALTER TABLE notification_routes
    ALTER COLUMN min_event_level SET NOT NULL,
    ALTER COLUMN min_event_level SET DEFAULT 'L2',
    DROP CONSTRAINT IF EXISTS notification_routes_min_event_level_check;
ALTER TABLE notification_routes
    ADD CONSTRAINT notification_routes_min_event_level_check
        CHECK (min_event_level IN ('L0','L1','L2','L3'));