BEGIN;

CREATE TABLE upstream_intelligence_ingest_capacity_windows (
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    window_start  TIMESTAMPTZ NOT NULL,
    window_seconds INTEGER NOT NULL CHECK (window_seconds BETWEEN 1 AND 86400),
	expires_at     TIMESTAMPTZ NOT NULL CHECK (expires_at > window_start),
    batches_used  INTEGER NOT NULL DEFAULT 0 CHECK (batches_used >= 0),
    facts_used    BIGINT NOT NULL DEFAULT 0 CHECK (facts_used >= 0),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (user_id, window_start, window_seconds)
);

CREATE TABLE upstream_intelligence_ingest_capacity_keys (
    user_id       BIGINT NOT NULL,
    window_start  TIMESTAMPTZ NOT NULL,
    window_seconds INTEGER NOT NULL,
    run_id        TEXT NOT NULL,
    batch_no      INTEGER NOT NULL CHECK (batch_no >= 0),
    payload_hash  TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    fact_count    INTEGER NOT NULL CHECK (fact_count >= 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (user_id, window_start, window_seconds, run_id, batch_no, payload_hash),
    FOREIGN KEY (user_id, window_start, window_seconds)
        REFERENCES upstream_intelligence_ingest_capacity_windows(user_id, window_start, window_seconds)
        ON DELETE CASCADE
);

CREATE INDEX idx_upstream_intelligence_ingest_capacity_keys_created
    ON upstream_intelligence_ingest_capacity_keys (created_at);

CREATE INDEX idx_upstream_intelligence_ingest_capacity_windows_expiry
	ON upstream_intelligence_ingest_capacity_windows (expires_at, user_id, window_start, window_seconds);

CREATE OR REPLACE FUNCTION admit_upstream_intelligence_ingest(
    p_user_id BIGINT,
    p_run_id TEXT,
    p_batch_no INTEGER,
    p_payload_hash TEXT,
    p_fact_count INTEGER,
    p_window_seconds BIGINT,
    p_max_batches INTEGER,
    p_max_facts BIGINT
) RETURNS TABLE (
    window_start TIMESTAMPTZ,
    window_end TIMESTAMPTZ,
    batches_used INTEGER,
    facts_used BIGINT,
	admitted BOOLEAN,
    replay BOOLEAN
) LANGUAGE plpgsql AS $$
DECLARE
    v_window_start TIMESTAMPTZ;
    v_batches INTEGER;
    v_facts BIGINT;
BEGIN
    IF p_user_id <= 0 OR BTRIM(p_run_id) = '' OR p_batch_no < 0 OR
       p_payload_hash !~ '^[0-9a-f]{64}$' OR p_fact_count < 0 OR
       p_window_seconds < 1 OR p_window_seconds > 86400 OR
       p_max_batches <= 0 OR p_max_facts <= 0 THEN
        RAISE EXCEPTION 'invalid upstream intelligence ingest capacity request' USING ERRCODE = '22023';
    END IF;

    v_window_start := to_timestamp(floor(extract(epoch FROM statement_timestamp()) / p_window_seconds) * p_window_seconds);

	-- Expired capacity keys are never needed for cross-window idempotency:
	-- durable ingest receipts provide that proof. Each admission removes a
	-- bounded, index-ordered page globally, including windows from owners that
	-- stopped ingesting. SKIP LOCKED keeps concurrent Core instances moving.
	WITH expired AS (
		SELECT capacity.user_id,capacity.window_start,capacity.window_seconds
		  FROM upstream_intelligence_ingest_capacity_windows AS capacity
		 WHERE capacity.expires_at <= v_window_start
		 ORDER BY capacity.expires_at,capacity.user_id,capacity.window_start,capacity.window_seconds
		 LIMIT 1000
		 FOR UPDATE SKIP LOCKED
	)
	DELETE FROM upstream_intelligence_ingest_capacity_windows AS capacity
	 USING expired
	 WHERE capacity.user_id=expired.user_id AND capacity.window_start=expired.window_start
	   AND capacity.window_seconds=expired.window_seconds;

    -- Once ingest persisted a durable receipt, every later replay of the exact
    -- canonical payload is free even after the original capacity window has
    -- expired. This check is owner scoped and uses the receipt primary key.
    IF EXISTS (
        SELECT 1 FROM upstream_ingest_batches AS receipt
         WHERE receipt.user_id=p_user_id AND receipt.run_id=p_run_id
           AND receipt.batch_no=p_batch_no AND receipt.payload_hash=p_payload_hash
    ) THEN
        SELECT capacity.batches_used,capacity.facts_used INTO v_batches,v_facts
          FROM upstream_intelligence_ingest_capacity_windows AS capacity
         WHERE capacity.user_id=p_user_id AND capacity.window_start=v_window_start
           AND capacity.window_seconds=p_window_seconds;
		RETURN QUERY SELECT v_window_start,v_window_start+make_interval(secs=>p_window_seconds::int),
		                    COALESCE(v_batches,0),COALESCE(v_facts,0),TRUE,TRUE;
        RETURN;
    END IF;

    INSERT INTO upstream_intelligence_ingest_capacity_windows
		(user_id,window_start,window_seconds,expires_at,batches_used,facts_used)
		VALUES (p_user_id,v_window_start,p_window_seconds,
		        v_window_start+make_interval(secs=>p_window_seconds::int),0,0)
        ON CONFLICT DO NOTHING;
    SELECT capacity.batches_used,capacity.facts_used INTO v_batches,v_facts
      FROM upstream_intelligence_ingest_capacity_windows AS capacity
     WHERE capacity.user_id=p_user_id AND capacity.window_start=v_window_start
       AND capacity.window_seconds=p_window_seconds
     FOR UPDATE;

    IF EXISTS (
        SELECT 1 FROM upstream_intelligence_ingest_capacity_keys AS capacity_key
         WHERE capacity_key.user_id=p_user_id AND capacity_key.window_start=v_window_start
           AND capacity_key.window_seconds=p_window_seconds AND capacity_key.run_id=p_run_id
           AND capacity_key.batch_no=p_batch_no AND capacity_key.payload_hash=p_payload_hash
    ) THEN
		RETURN QUERY SELECT v_window_start,v_window_start+make_interval(secs=>p_window_seconds::int),v_batches,v_facts,TRUE,TRUE;
        RETURN;
    END IF;
    IF v_batches + 1 > p_max_batches OR v_facts + p_fact_count > p_max_facts THEN
		RETURN QUERY SELECT v_window_start,v_window_start+make_interval(secs=>p_window_seconds::int),v_batches,v_facts,FALSE,FALSE;
        RETURN;
    END IF;

    INSERT INTO upstream_intelligence_ingest_capacity_keys
        (user_id,window_start,window_seconds,run_id,batch_no,payload_hash,fact_count)
        VALUES (p_user_id,v_window_start,p_window_seconds,p_run_id,p_batch_no,p_payload_hash,p_fact_count);
    UPDATE upstream_intelligence_ingest_capacity_windows AS capacity
       SET batches_used=capacity.batches_used+1,facts_used=capacity.facts_used+p_fact_count,
           updated_at=statement_timestamp()
     WHERE capacity.user_id=p_user_id AND capacity.window_start=v_window_start
       AND capacity.window_seconds=p_window_seconds
     RETURNING capacity.batches_used,capacity.facts_used INTO v_batches,v_facts;
	RETURN QUERY SELECT v_window_start,v_window_start+make_interval(secs=>p_window_seconds::int),v_batches,v_facts,TRUE,FALSE;
END;
$$;

COMMIT;
