BEGIN;

-- Composite identities make every rollout reference owner-scoped instead of
-- relying on application joins that could accidentally pair another owner.
CREATE UNIQUE INDEX IF NOT EXISTS recommendation_rollout_route_plan_owner_idx
    ON route_plans (id, user_id);

CREATE TABLE recommendation_rollouts (
    id                              TEXT PRIMARY KEY,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id                         TEXT NOT NULL REFERENCES route_plans(id) ON DELETE RESTRICT,
    instance_id                     TEXT NOT NULL REFERENCES instances(id) ON DELETE RESTRICT,
    recommendation_id               TEXT NOT NULL,
    recommendation_plan_generation  BIGINT NOT NULL CHECK (recommendation_plan_generation > 0),
    recommendation_fingerprint      CHAR(64) NOT NULL CHECK (recommendation_fingerprint ~ '^[0-9a-f]{64}$'),
    fact_version                    BIGINT NOT NULL CHECK (fact_version > 0),
    evidence_ids                    JSONB NOT NULL CHECK (jsonb_typeof(evidence_ids) = 'array' AND jsonb_array_length(evidence_ids) > 0),
    from_channel_id                 TEXT NOT NULL,
    to_channel_id                   TEXT NOT NULL,
    from_account_id                 TEXT NOT NULL,
    to_account_id                   TEXT NOT NULL,
    baseline_weights                JSONB NOT NULL CHECK (jsonb_typeof(baseline_weights) = 'object' AND baseline_weights <> '{}'::jsonb),
    baseline_fingerprint            CHAR(64) NOT NULL CHECK (baseline_fingerprint ~ '^[0-9a-f]{64}$'),
    scheduling_generation           BIGINT NOT NULL CHECK (scheduling_generation > 0),
    status                          TEXT NOT NULL CHECK (status IN ('ready','applying','observing','rollback_required','completed','rolled_back','blocked')),
    stage                           INTEGER NOT NULL CHECK (stage IN (0,10,25,50,100)),
    pending_stage                   INTEGER NOT NULL CHECK (pending_stage IN (0,10,25,50,100)),
    observation_seconds             INTEGER NOT NULL CHECK (observation_seconds > 0 AND observation_seconds <= 604800),
    recommendation_expires_at       TIMESTAMPTZ NOT NULL,
    started_at                      TIMESTAMPTZ NOT NULL,
    stage_started_at                TIMESTAMPTZ,
    observe_until                   TIMESTAMPTZ,
    last_after_evidence             JSONB,
    rollback_reasons                JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(rollback_reasons) = 'array'),
    version                         BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    last_operation_id               TEXT NOT NULL DEFAULT '',
    created_at                      TIMESTAMPTZ NOT NULL,
    updated_at                      TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (user_id, recommendation_id) REFERENCES upstream_recommendations(user_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (plan_id, user_id) REFERENCES route_plans(id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (instance_id, user_id) REFERENCES instances(id, user_id) ON DELETE RESTRICT,
    CONSTRAINT recommendation_rollouts_owner_identity_key UNIQUE (id,user_id,plan_id),
    CONSTRAINT recommendation_rollouts_identity_check CHECK (
        id=btrim(id) AND id<>'' AND char_length(id)<=256 AND
        plan_id=btrim(plan_id) AND plan_id<>'' AND char_length(plan_id)<=256 AND
        instance_id=btrim(instance_id) AND instance_id<>'' AND char_length(instance_id)<=256 AND
        recommendation_id=btrim(recommendation_id) AND recommendation_id<>'' AND char_length(recommendation_id)<=256 AND
        from_channel_id=btrim(from_channel_id) AND from_channel_id<>'' AND char_length(from_channel_id)<=256 AND
        to_channel_id=btrim(to_channel_id) AND to_channel_id<>'' AND char_length(to_channel_id)<=256 AND
        from_account_id=btrim(from_account_id) AND from_account_id<>'' AND char_length(from_account_id)<=256 AND
        to_account_id=btrim(to_account_id) AND to_account_id<>'' AND char_length(to_account_id)<=256 AND
        from_channel_id<>to_channel_id AND from_account_id<>to_account_id AND
        recommendation_expires_at>started_at
    ),
    CONSTRAINT recommendation_rollouts_state_shape_check CHECK (
        (status='applying' AND observe_until IS NULL AND (
            (stage=0 AND pending_stage=10) OR (stage=10 AND pending_stage=25) OR
            (stage=25 AND pending_stage=50) OR (stage=50 AND pending_stage=100)
        )) OR
        (status='observing' AND pending_stage=0 AND stage IN (10,25,50,100) AND
            stage_started_at IS NOT NULL AND observe_until IS NOT NULL AND observe_until>stage_started_at) OR
        (status='completed' AND stage=100 AND pending_stage=0) OR
        (status='rolled_back' AND stage=0 AND pending_stage=0) OR
        (status IN ('ready','rollback_required','blocked') AND pending_stage=0)
    )
);

CREATE UNIQUE INDEX recommendation_rollouts_one_active_plan_idx
    ON recommendation_rollouts (plan_id)
    WHERE status IN ('ready','applying','observing','rollback_required','blocked');
CREATE INDEX recommendation_rollouts_owner_time_idx
    ON recommendation_rollouts (user_id, updated_at DESC, id DESC);
CREATE INDEX recommendation_rollouts_status_time_idx
    ON recommendation_rollouts (status, updated_at, id);

CREATE TABLE recommendation_rollout_operations (
    id            TEXT PRIMARY KEY,
    rollout_id    TEXT NOT NULL REFERENCES recommendation_rollouts(id) ON DELETE CASCADE,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id       TEXT NOT NULL REFERENCES route_plans(id) ON DELETE RESTRICT,
    action        TEXT NOT NULL CHECK (action IN ('apply_stage','rollback')),
    target_stage  INTEGER NOT NULL CHECK (target_stage IN (0,10,25,50,100)),
    status        TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','superseded')),
    attempts      INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error_code    TEXT NOT NULL DEFAULT '' CHECK (error_code IN (
        '', 'capability_unsupported','ownership_lost','plan_changed','mapping_invalid',
        'weight_unknown','baseline_changed','revalidation_blocked','gateway_unavailable',
        'write_failed','readback_failed','verification_failed','internal_error'
    )),
    version       BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    lease_owner   TEXT NOT NULL DEFAULT '',
    lease_until   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (rollout_id,user_id,plan_id) REFERENCES recommendation_rollouts(id,user_id,plan_id) ON DELETE CASCADE,
    CONSTRAINT recommendation_rollout_operations_action_shape CHECK (
        (action='apply_stage' AND target_stage IN (10,25,50,100)) OR
        (action='rollback' AND target_stage=0)
    ),
    CONSTRAINT recommendation_rollout_operations_lease_shape CHECK (
        (status='running' AND lease_owner<>'' AND lease_until IS NOT NULL) OR
        (status<>'running' AND lease_owner='' AND lease_until IS NULL)
    ),
    CONSTRAINT recommendation_rollout_operations_error_shape CHECK (
        (status='failed' AND error_code<>'') OR (status<>'failed' AND error_code='')
    )
);

CREATE UNIQUE INDEX recommendation_rollout_operations_one_active_idx
    ON recommendation_rollout_operations (rollout_id)
    WHERE status IN ('pending','running');
CREATE INDEX recommendation_rollout_operations_due_idx
    ON recommendation_rollout_operations (status, updated_at, id);
CREATE INDEX recommendation_rollout_operations_rollout_time_idx
    ON recommendation_rollout_operations (rollout_id, created_at DESC, id DESC);

COMMIT;
