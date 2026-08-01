-- PublishedBinding carries two kinds of data: a stable local binding identity
-- and the gateway execution identity currently owned by a scheduling
-- generation. Protect both at the database boundary so a legacy or direct SQL
-- writer cannot silently transplant verified callability to another account.
CREATE OR REPLACE FUNCTION enforce_published_binding_execution_identity()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.plan_id IS DISTINCT FROM OLD.plan_id
       OR NEW.instance_id IS DISTINCT FROM OLD.instance_id
       OR NEW.channel_id IS DISTINCT FROM OLD.channel_id THEN
        RAISE EXCEPTION 'published binding stable identity is immutable'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'published_binding_stable_identity_immutable';
    END IF;

    IF NEW.scheduling_generation < OLD.scheduling_generation THEN
        RAISE EXCEPTION 'published binding scheduling generation cannot move backwards'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'published_binding_generation_monotonic';
    END IF;

    IF NEW.remote_id IS DISTINCT FROM OLD.remote_id THEN
        IF BTRIM(NEW.remote_id) = '' THEN
            RAISE EXCEPTION 'published binding remote identity cannot be cleared'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'published_binding_remote_identity_not_cleared';
        END IF;
        IF BTRIM(OLD.remote_id) <> ''
           AND NEW.scheduling_generation = OLD.scheduling_generation THEN
            RAISE EXCEPTION 'published binding remote identity cannot drift within one generation'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'published_binding_remote_identity_generation_fence';
        END IF;

        -- A different concrete remote in a newer generation is a new execution
        -- identity. Force a fresh publication/callability proof even when the
        -- caller attempts to carry old verified fields into the update.
        IF BTRIM(OLD.remote_id) <> ''
           AND NEW.scheduling_generation > OLD.scheduling_generation THEN
            NEW.verification_status := 'published_pending';
            NEW.verification_source := 'publish';
            NEW.verified_at := NULL;
            NEW.verification_error_code := '';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_enforce_published_binding_execution_identity ON published_bindings;
CREATE TRIGGER trg_enforce_published_binding_execution_identity
BEFORE UPDATE ON published_bindings
FOR EACH ROW
EXECUTE FUNCTION enforce_published_binding_execution_identity();
