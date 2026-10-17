-- Tenants. The database default status is 'pending' (the safe choice if
-- some future code path forgets to set it); self-registration currently
-- sets 'active' explicitly.

-- +goose Up
CREATE TABLE companies (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    slug       text        NOT NULL,
    status     text        NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT companies_slug_key     UNIQUE (slug),
    CONSTRAINT companies_slug_check   CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) BETWEEN 3 AND 50),
    CONSTRAINT companies_name_check   CHECK (btrim(name) <> ''),
    CONSTRAINT companies_status_check CHECK (status IN ('pending', 'active', 'suspended'))
);

CREATE TRIGGER companies_set_updated_at
BEFORE UPDATE ON companies
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE companies;
