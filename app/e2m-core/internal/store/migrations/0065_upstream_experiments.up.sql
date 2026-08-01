CREATE TABLE upstream_shadow_results (
    id text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recommendation_id text NOT NULL,
    recommendation_fingerprint char(64) NOT NULL,
    winner jsonb NOT NULL,
    ranking jsonb NOT NULL,
    evidence_ids jsonb NOT NULL,
    evaluated_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, id),
    CHECK (jsonb_typeof(winner) = 'object'),
    CHECK (jsonb_typeof(ranking) = 'array'),
    CHECK (jsonb_array_length(ranking) > 0),
    CHECK (jsonb_typeof(evidence_ids) = 'array'),
    CHECK (jsonb_array_length(evidence_ids) > 0)
);

CREATE INDEX upstream_shadow_results_owner_recommendation_time_idx
    ON upstream_shadow_results (user_id, recommendation_id, evaluated_at DESC, id DESC);

CREATE TABLE upstream_dry_run_results (
    id text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recommendation_id text NOT NULL,
    recommendation_fingerprint char(64) NOT NULL,
    intelligence_fact_version bigint NOT NULL CHECK (intelligence_fact_version > 0),
    cost_ledger_fact_version bigint NOT NULL CHECK (cost_ledger_fact_version > 0),
    link_fact_version bigint NOT NULL CHECK (link_fact_version > 0),
    plan_generation bigint NOT NULL CHECK (plan_generation > 0),
    plan_id text NOT NULL,
    from_channel_id text NOT NULL,
    to_channel_id text NOT NULL,
    desired_scheduling jsonb NOT NULL,
    reconcile_kind text NOT NULL CHECK (reconcile_kind = 'dry_run'),
    reconcile_plan jsonb NOT NULL,
    action_hash_version text NOT NULL,
    action_set_hash char(64) NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, id),
    CHECK (from_channel_id <> to_channel_id),
    CHECK (jsonb_typeof(desired_scheduling) = 'object'),
    CHECK (jsonb_typeof(reconcile_plan) = 'object')
);

CREATE INDEX upstream_dry_run_results_owner_recommendation_time_idx
    ON upstream_dry_run_results (user_id, recommendation_id, created_at DESC, id DESC);
