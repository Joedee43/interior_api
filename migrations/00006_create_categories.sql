-- Platform-wide product categories, shared by all companies so buyers can
-- browse "all chairs" across sellers. Seeded here; platform admins may add
-- more later.

-- +goose Up
CREATE TABLE categories (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text        NOT NULL,
    name       text        NOT NULL,
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT categories_slug_key   UNIQUE (slug),
    CONSTRAINT categories_slug_check CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    CONSTRAINT categories_name_check CHECK (btrim(name) <> '')
);

CREATE TRIGGER categories_set_updated_at
BEFORE UPDATE ON categories
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO categories (slug, name, sort_order) VALUES
    ('chairs',       'Chairs',              10),
    ('office-chairs','Office Chairs',       20),
    ('sofas',        'Sofas & Armchairs',   30),
    ('tables',       'Tables',              40),
    ('desks',        'Desks',               50),
    ('storage',      'Storage & Cabinets',  60),
    ('shelving',     'Shelving',            70),
    ('beds',         'Beds',                80),
    ('outdoor',      'Outdoor',             90);

-- +goose Down
DROP TABLE categories;
