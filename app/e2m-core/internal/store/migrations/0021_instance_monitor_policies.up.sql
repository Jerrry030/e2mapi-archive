CREATE TABLE instance_monitor_policies (
    instance_id            TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
    user_id                BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    check_interval_seconds INT NOT NULL DEFAULT 60 CHECK (check_interval_seconds IN (30, 60, 300)),
    fail_streak            INT NOT NULL DEFAULT 2 CHECK (fail_streak BETWEEN 1 AND 5),
    auto_switch            BOOLEAN NOT NULL DEFAULT FALSE,
    cooldown_seconds       INT NOT NULL DEFAULT 300 CHECK (cooldown_seconds IN (300, 900, 1800)),
    drift_detection        BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT instance_monitor_policy_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE
);

CREATE INDEX idx_instance_monitor_policies_user
    ON instance_monitor_policies (user_id);
