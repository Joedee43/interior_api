-- Configurable attributes of a product (Fabric, Colour, Legs) and their
-- values (Linen, Grey, Oak).
--
-- Composite foreign keys carry company_id/product_id down the tree, so the
-- database itself guarantees an option belongs to the same company as its
-- product, and (in 00010) a variant's chosen value belongs to the option it
-- claims. A Go bug cannot link rows across companies or products.

-- +goose Up
-- Target for composite foreign keys (id is already unique; this adds the pair).
ALTER TABLE products ADD CONSTRAINT products_id_company_key UNIQUE (id, company_id);

CREATE TABLE product_options (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id uuid        NOT NULL,
    company_id uuid        NOT NULL,
    name       text        NOT NULL,
    position   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_options_product_fkey FOREIGN KEY (product_id, company_id)
        REFERENCES products (id, company_id) ON DELETE CASCADE,
    CONSTRAINT product_options_id_product_key UNIQUE (id, product_id),
    CONSTRAINT product_options_name_check     CHECK (btrim(name) <> '' AND length(name) <= 50),
    CONSTRAINT product_options_position_check CHECK (position BETWEEN 0 AND 1000)
);

-- Case-insensitive uniqueness: "Fabric" and "fabric" are the same option.
CREATE UNIQUE INDEX product_options_product_name_key ON product_options (product_id, lower(name));

CREATE TRIGGER product_options_set_updated_at
BEFORE UPDATE ON product_options
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE product_option_values (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    option_id    uuid        NOT NULL,
    name         text        NOT NULL,
    position     integer     NOT NULL DEFAULT 0,
    -- Display colour for swatches, e.g. #A3B18A.
    swatch_hex   text,
    -- Name of the GLTF material/mesh slot this value maps to (Phase 6).
    material_key text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_option_values_option_fkey FOREIGN KEY (option_id)
        REFERENCES product_options (id) ON DELETE CASCADE,
    CONSTRAINT product_option_values_id_option_key UNIQUE (id, option_id),
    CONSTRAINT product_option_values_name_check     CHECK (btrim(name) <> '' AND length(name) <= 50),
    CONSTRAINT product_option_values_position_check CHECK (position BETWEEN 0 AND 1000),
    CONSTRAINT product_option_values_swatch_check   CHECK (swatch_hex IS NULL OR swatch_hex ~ '^#[0-9A-F]{6}$'),
    CONSTRAINT product_option_values_material_check CHECK (material_key IS NULL OR material_key ~ '^[a-z0-9_]{1,64}$')
);

CREATE UNIQUE INDEX product_option_values_option_name_key ON product_option_values (option_id, lower(name));

CREATE TRIGGER product_option_values_set_updated_at
BEFORE UPDATE ON product_option_values
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE product_option_values;
DROP TABLE product_options;
ALTER TABLE products DROP CONSTRAINT products_id_company_key;
