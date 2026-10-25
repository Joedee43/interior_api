-- Customer-owned saved designs: a room plus placed furniture.
-- A design copies its room size and features when created from a
-- template, so later template edits never change saved designs.

-- +goose Up
CREATE TABLE designs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL,
    template_id uuid,
    name        text        NOT NULL,
    width_mm    integer     NOT NULL,
    depth_mm    integer     NOT NULL,
    height_mm   integer     NOT NULL,
    features    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT designs_user_id_fkey     FOREIGN KEY (user_id)     REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT designs_template_id_fkey FOREIGN KEY (template_id) REFERENCES room_templates (id) ON DELETE SET NULL,
    CONSTRAINT designs_name_check       CHECK (btrim(name) <> '' AND length(name) <= 100),
    CONSTRAINT designs_size_check       CHECK (width_mm BETWEEN 1000 AND 30000
                                           AND depth_mm BETWEEN 1000 AND 30000
                                           AND height_mm BETWEEN 2000 AND 6000),
    CONSTRAINT designs_features_check   CHECK (jsonb_typeof(features) = 'object' AND octet_length(features::text) <= 65536)
);

-- "My designs", most recently edited first, keyset-paginated.
CREATE INDEX designs_user_updated_idx ON designs (user_id, updated_at DESC, id DESC);

CREATE TRIGGER designs_set_updated_at
BEFORE UPDATE ON designs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per placed piece of furniture.
CREATE TABLE design_items (
    id           uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    design_id    uuid         NOT NULL,
    product_id   uuid         NOT NULL,
    variant_id   uuid,
    x_mm         integer      NOT NULL,
    y_mm         integer      NOT NULL DEFAULT 0,
    z_mm         integer      NOT NULL,
    -- Turn around the vertical (Y) axis, in degrees.
    rotation_deg numeric(5,2) NOT NULL DEFAULT 0,
    created_at   timestamptz  NOT NULL DEFAULT now(),
    updated_at   timestamptz  NOT NULL DEFAULT now(),

    CONSTRAINT design_items_design_fkey  FOREIGN KEY (design_id) REFERENCES designs (id) ON DELETE CASCADE,
    -- RESTRICT: a product or variant someone has placed cannot be deleted
    -- (the catalog reports this as product_in_use / variant_in_use).
    CONSTRAINT design_items_product_fkey FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE RESTRICT,
    -- The variant must belong to the placed product (checked only when set).
    CONSTRAINT design_items_variant_fkey FOREIGN KEY (variant_id, product_id)
        REFERENCES product_variants (id, product_id) ON DELETE RESTRICT,
    CONSTRAINT design_items_position_check CHECK (x_mm >= 0 AND y_mm >= 0 AND z_mm >= 0),
    CONSTRAINT design_items_rotation_check CHECK (rotation_deg >= 0 AND rotation_deg < 360)
);

CREATE INDEX design_items_design_id_idx  ON design_items (design_id, created_at, id);
CREATE INDEX design_items_product_id_idx ON design_items (product_id);
CREATE INDEX design_items_variant_id_idx ON design_items (variant_id);

CREATE TRIGGER design_items_set_updated_at
BEFORE UPDATE ON design_items
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE design_items;
DROP TABLE designs;
