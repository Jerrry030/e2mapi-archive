BEGIN;

-- Source status is a recommendation callability fact. Extend the typed
-- lineage vocabulary before Core starts publishing source mutations.
ALTER TABLE upstream_intelligence_fact_mutations
    DROP CONSTRAINT upstream_intelligence_fact_mutations_mutation_kind_check;
ALTER TABLE upstream_intelligence_fact_mutations
    ADD CONSTRAINT upstream_intelligence_fact_mutations_mutation_kind_check
    CHECK (mutation_kind IN (
        'quality','collection','link','source','retention','unknown'
    ));

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
       OR target_mutation_kind NOT IN ('quality','collection','link','source','retention','unknown')
       OR (target_evidence_id IS NOT NULL AND (
               target_evidence_id <> BTRIM(target_evidence_id)
               OR target_evidence_id = ''
               OR char_length(target_evidence_id) > 256
          )) THEN
        RAISE EXCEPTION 'invalid upstream intelligence fact mutation';
    END IF;
    IF target_mutation_kind IN ('quality','collection','link','source')
       AND target_evidence_id IS NULL THEN
        RAISE EXCEPTION 'upstream intelligence fact mutation evidence is required';
    END IF;

    INSERT INTO upstream_intelligence_fact_versions
        (user_id, fact_version, updated_at)
    VALUES (target_user_id, 0, statement_timestamp())
    ON CONFLICT (user_id) DO NOTHING;

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

COMMIT;
