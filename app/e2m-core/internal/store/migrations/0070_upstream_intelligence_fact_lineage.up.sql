BEGIN;

-- Every owner fact-version advance has one durable, typed cause. The mutation
-- row and the version update are written by one SQL function so no Core writer
-- can publish a version without its lineage or lineage without its version.
CREATE TABLE IF NOT EXISTS upstream_intelligence_fact_mutations (
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fact_version  BIGINT NOT NULL CHECK (fact_version > 0),
    mutation_kind TEXT NOT NULL CHECK (mutation_kind IN (
                       'quality','collection','link','retention','unknown'
                   )),
    evidence_id   TEXT CHECK (
                       evidence_id IS NULL OR (
                           evidence_id = BTRIM(evidence_id)
                           AND evidence_id <> ''
                           AND char_length(evidence_id) <= 256
                       )
                   ),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (user_id, fact_version)
);

-- The watermark is the oldest version for which lineage is known complete.
-- Existing owners start at their current version: versions before the cutover
-- cannot be reconstructed and therefore must fail closed as unknown history.
CREATE TABLE IF NOT EXISTS upstream_intelligence_fact_lineage_watermarks (
    user_id      BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    fact_version BIGINT NOT NULL CHECK (fact_version >= 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

INSERT INTO upstream_intelligence_fact_lineage_watermarks
    (user_id, fact_version, created_at)
SELECT user_id, fact_version, statement_timestamp()
  FROM upstream_intelligence_fact_versions
ON CONFLICT (user_id) DO NOTHING;

CREATE OR REPLACE FUNCTION record_upstream_intelligence_fact_mutation(
    target_user_id BIGINT,
    target_mutation_kind TEXT,
    target_evidence_id TEXT
)
RETURNS TABLE (
    out_user_id BIGINT,
    out_fact_version BIGINT,
    out_updated_at TIMESTAMPTZ
)
LANGUAGE plpgsql
AS $$
DECLARE
    previous_fact_version BIGINT;
    recorded_user_id BIGINT;
    recorded_fact_version BIGINT;
    recorded_updated_at TIMESTAMPTZ;
BEGIN
    IF target_user_id <= 0
       OR target_mutation_kind NOT IN ('quality','collection','link','retention','unknown')
       OR (target_evidence_id IS NOT NULL AND (
               target_evidence_id <> BTRIM(target_evidence_id)
               OR target_evidence_id = ''
               OR char_length(target_evidence_id) > 256
          )) THEN
        RAISE EXCEPTION 'invalid upstream intelligence fact mutation';
    END IF;
    IF target_mutation_kind IN ('quality','collection','link') AND target_evidence_id IS NULL THEN
        RAISE EXCEPTION 'upstream intelligence fact mutation evidence is required';
    END IF;

    -- Ensure every owner has one row we can lock, including a genuinely new
    -- owner. An owner may already have a non-zero version but no watermark
    -- when the first managed mutation races a cutover from an older writer.
    INSERT INTO upstream_intelligence_fact_versions
        (user_id, fact_version, updated_at)
    VALUES (target_user_id, 0, statement_timestamp())
    ON CONFLICT (user_id) DO NOTHING;

    -- The version row is the per-owner serialization point. Read the exact
    -- pre-mutation version while holding its lock, then establish the lineage
    -- watermark from that value. Concurrent first mutations cannot both
    -- choose a stale baseline or allocate the same next version.
    SELECT fact_version
      INTO previous_fact_version
      FROM upstream_intelligence_fact_versions
     WHERE user_id = target_user_id
       FOR UPDATE;

    INSERT INTO upstream_intelligence_fact_lineage_watermarks
        (user_id, fact_version, created_at)
    VALUES (target_user_id, previous_fact_version, statement_timestamp())
    ON CONFLICT (user_id) DO NOTHING;

    UPDATE upstream_intelligence_fact_versions
       SET fact_version = fact_version + 1,
           updated_at = statement_timestamp()
     WHERE user_id = target_user_id
    RETURNING upstream_intelligence_fact_versions.user_id,
              upstream_intelligence_fact_versions.fact_version,
              upstream_intelligence_fact_versions.updated_at
         INTO recorded_user_id, recorded_fact_version, recorded_updated_at;

    INSERT INTO upstream_intelligence_fact_mutations
        (user_id, fact_version, mutation_kind, evidence_id, created_at)
    VALUES (recorded_user_id, recorded_fact_version, target_mutation_kind,
            target_evidence_id, recorded_updated_at);

    RETURN QUERY SELECT recorded_user_id, recorded_fact_version, recorded_updated_at;
END;
$$;

-- Replace migration 0060's quality trigger in place. Ownership still comes
-- only from the durable allocation ledger, while snapshot.id is the exact
-- evidence identity responsible for this version.
CREATE OR REPLACE FUNCTION bump_upstream_intelligence_version_for_quality_snapshot()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_user_id BIGINT;
BEGIN
    SELECT allocation.user_id
      INTO target_user_id
      FROM upstream_channel_allocations AS allocation
     WHERE allocation.channel_id = NEW.channel_id;
    IF FOUND THEN
        PERFORM record_upstream_intelligence_fact_mutation(
            target_user_id, 'quality', NEW.id
        );
    END IF;
    RETURN NEW;
END;
$$;

COMMIT;
