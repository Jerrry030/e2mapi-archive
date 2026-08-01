-- Gateway publication and real model-call verification are separate facts.
-- Existing active bindings have been published but have no trustworthy call
-- evidence, so upgrades place them in awaiting_first_request rather than
-- fabricating a successful verification.
ALTER TABLE published_bindings
    ADD COLUMN IF NOT EXISTS verification_status TEXT NOT NULL DEFAULT 'published_pending',
    ADD COLUMN IF NOT EXISTS verification_source TEXT NOT NULL DEFAULT 'publish',
    ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS verification_error_code TEXT NOT NULL DEFAULT '';

UPDATE published_bindings
   SET verification_status = CASE
           WHEN state IN ('active', 'disabled') THEN 'awaiting_first_request'
           ELSE 'published_pending'
       END,
       verification_source = 'publish',
       verified_at = NULL,
       verification_error_code = '';

ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_bindings_verification_status_check,
    DROP CONSTRAINT IF EXISTS published_bindings_verification_source_check,
    DROP CONSTRAINT IF EXISTS published_bindings_verification_shape_check;

ALTER TABLE published_bindings
    ADD CONSTRAINT published_bindings_verification_status_check CHECK (
        verification_status IN (
            'published_pending', 'awaiting_first_request',
            'probe_verified', 'passive_verified', 'verification_failed'
        )
    ),
    ADD CONSTRAINT published_bindings_verification_source_check CHECK (
        verification_source IN ('publish', 'probe', 'passive')
    ),
    ADD CONSTRAINT published_bindings_verification_shape_check CHECK (
        (
            verification_status = 'probe_verified'
            AND verification_source = 'probe'
            AND verified_at IS NOT NULL
            AND verification_error_code = ''
        ) OR (
            verification_status = 'passive_verified'
            AND verification_source = 'passive'
            AND verified_at IS NOT NULL
            AND verification_error_code = ''
        ) OR (
            verification_status IN ('published_pending', 'awaiting_first_request')
            AND verification_source = 'publish'
            AND verified_at IS NULL
        ) OR (
            verification_status = 'verification_failed'
            AND verification_source IN ('probe', 'passive')
            AND verified_at IS NULL
        )
    );

CREATE INDEX IF NOT EXISTS idx_published_bindings_verification
    ON published_bindings (verification_status, updated_at DESC);
