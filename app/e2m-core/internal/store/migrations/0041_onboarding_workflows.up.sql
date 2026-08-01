-- Durable automatic onboarding, one workflow for every instance/shared-pool
-- pair. The row stores only opaque IDs and delivered key version numbers.
CREATE TABLE IF NOT EXISTS onboarding_workflows (
    id                  TEXT PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id         TEXT NOT NULL,
    pool_id             TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE CASCADE,
    connector_id        TEXT NOT NULL DEFAULT '',
    stage               TEXT NOT NULL CHECK (stage IN (
                            'waiting_connector', 'checking_gateway',
                            'assigning_keys', 'delivering_bindings',
                            'publishing', 'verifying', 'active',
                            'failed_retryable'
                        )),
    status              TEXT NOT NULL CHECK (status IN (
                            'pending', 'running', 'retryable', 'active'
                        )),
    attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at     TIMESTAMPTZ,
    last_error_code     TEXT NOT NULL DEFAULT '',
    plan_id             TEXT NOT NULL DEFAULT '',
    key_version_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    lease_owner         TEXT NOT NULL DEFAULT '',
    lease_until         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT onboarding_workflows_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE,
    CONSTRAINT onboarding_workflows_identity_key UNIQUE (instance_id, pool_id),
    CONSTRAINT onboarding_workflows_lease_shape_check CHECK (
        (status = 'running' AND lease_owner <> '' AND lease_until IS NOT NULL)
        OR
        (status <> 'running' AND lease_owner = '' AND lease_until IS NULL)
    ),
    CONSTRAINT onboarding_workflows_active_shape_check CHECK (
        status <> 'active' OR stage = 'active'
    )
);

CREATE INDEX IF NOT EXISTS idx_onboarding_workflows_claim
    ON onboarding_workflows (status, next_attempt_at, lease_until, updated_at);
CREATE INDEX IF NOT EXISTS idx_onboarding_workflows_user_updated
    ON onboarding_workflows (user_id, updated_at DESC);

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.quality.probe',
        'gateway.binding.proof',
        'gateway.binding.install',
        'gateway.account.schedulable.set',
        'gateway.account.switch',
        'gateway.scheduling.barrier',
        'gateway.account.create',
        'gateway.account.update',
        'gateway.account.delete'
    ));
