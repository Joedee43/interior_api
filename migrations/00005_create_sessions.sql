-- Server-side login sessions. Only the SHA-256 of the cookie token is
-- stored, so a leaked database cannot be replayed as logins.

-- +goose Up
CREATE TABLE sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL,
    token_hash   bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    user_agent   text,
    ip           inet,

    CONSTRAINT sessions_token_hash_key   UNIQUE (token_hash),
    CONSTRAINT sessions_token_hash_check CHECK (octet_length(token_hash) = 32),
    CONSTRAINT sessions_expiry_check     CHECK (expires_at > created_at),
    CONSTRAINT sessions_user_id_fkey     FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE INDEX sessions_user_id_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at); -- for a future expired-session cleanup job

-- +goose Down
DROP TABLE sessions;
