BEGIN;
DROP FUNCTION IF EXISTS admit_upstream_intelligence_ingest(BIGINT,TEXT,INTEGER,TEXT,INTEGER,BIGINT,INTEGER,BIGINT);
DROP TABLE IF EXISTS upstream_intelligence_ingest_capacity_keys;
DROP TABLE IF EXISTS upstream_intelligence_ingest_capacity_windows;
COMMIT;
