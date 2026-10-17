-- Platform-wide user accounts. Company-specific roles live in
-- company_members, not here; is_platform_admin is the only global role
-- and is never settable through the public API.
--
-- email is normalized (trimmed + lowercased) in Go before it reaches the
-- database, so a plain UNIQUE constraint is enough.

-- +goose Up
CREATE TABLE users (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text        NOT NULL,
    password_hash     text        NOT NULL,
    full_name         text        NOT NULL,
    status            text        NOT NULL DEFAULT 'active',
    is_platform_admin boolean     NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_email_key        UNIQUE (email),
    CONSTRAINT users_email_check      CHECK (email <> '' AND email = btrim(email)),
    CONSTRAINT users_full_name_check  CHECK (btrim(full_name) <> ''),
    CONSTRAINT users_status_check     CHECK (status IN ('active', 'disabled'))
);

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE users;
