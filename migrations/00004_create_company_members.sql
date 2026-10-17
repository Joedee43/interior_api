-- Which users belong to which companies, and in what role. A user may
-- belong to several companies. ON DELETE RESTRICT: removing a company or
-- user with members must be an explicit decision, not a cascade accident.

-- +goose Up
CREATE TABLE company_members (
    company_id uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    role       text        NOT NULL,
    status     text        NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT company_members_pkey            PRIMARY KEY (company_id, user_id),
    CONSTRAINT company_members_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies (id) ON DELETE RESTRICT,
    CONSTRAINT company_members_user_id_fkey    FOREIGN KEY (user_id)    REFERENCES users (id)     ON DELETE RESTRICT,
    CONSTRAINT company_members_role_check      CHECK (role IN ('company_admin', 'company_staff')),
    CONSTRAINT company_members_status_check    CHECK (status IN ('active', 'disabled'))
);

-- The primary key already indexes company_id (its first column); this
-- one serves "which companies does this user belong to?".
CREATE INDEX company_members_user_id_idx ON company_members (user_id);

CREATE TRIGGER company_members_set_updated_at
BEFORE UPDATE ON company_members
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE company_members;
