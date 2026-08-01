-- Console sessions. Users are created in 0001 so every account-owned table can
-- carry a foreign key from the moment it is created.
-- password_hash is bcrypt; sessions store only the SHA-256 hash of the token.

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);
