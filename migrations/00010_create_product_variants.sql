-- Sellable combinations (SKU variants). Only combinations listed here can
-- be chosen; each carries its full price and an availability switch.

-- +goose Up
CREATE TABLE product_variants (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id      uuid        NOT NULL,
    company_id      uuid        NOT NULL,
    sku             text        NOT NULL,
    price_minor     bigint      NOT NULL,
    available       boolean     NOT NULL DEFAULT true,
    position        integer     NOT NULL DEFAULT 0,
    -- Sorted value ids joined with ",", computed in Go. The unique
    -- constraint below stops two variants having the same combination.
    combination_key text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_variants_product_fkey FOREIGN KEY (product_id, company_id)
        REFERENCES products (id, company_id) ON DELETE CASCADE,
    CONSTRAINT product_variants_id_product_key  UNIQUE (id, product_id),
    -- SKUs are unique within a company (two sellers may reuse a code).
    CONSTRAINT product_variants_company_sku_key UNIQUE (company_id, sku),
    CONSTRAINT product_variants_combination_key UNIQUE (product_id, combination_key),
    CONSTRAINT product_variants_sku_check       CHECK (sku ~ '^[A-Z0-9][A-Z0-9._-]{0,63}$'),
    CONSTRAINT product_variants_price_check     CHECK (price_minor >= 0),
    CONSTRAINT product_variants_position_check  CHECK (position BETWEEN 0 AND 1000)
);

CREATE INDEX product_variants_product_id_idx ON product_variants (product_id);

CREATE TRIGGER product_variants_set_updated_at
BEFORE UPDATE ON product_variants
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per (variant, option): which value the variant has for it.
CREATE TABLE product_variant_values (
    variant_id uuid NOT NULL,
    product_id uuid NOT NULL,
    option_id  uuid NOT NULL,
    value_id   uuid NOT NULL,

    -- One value per option per variant.
    CONSTRAINT product_variant_values_pkey PRIMARY KEY (variant_id, option_id),
    CONSTRAINT product_variant_values_variant_fkey FOREIGN KEY (variant_id, product_id)
        REFERENCES product_variants (id, product_id) ON DELETE CASCADE,
    -- The option must belong to the same product as the variant...
    CONSTRAINT product_variant_values_option_fkey FOREIGN KEY (option_id, product_id)
        REFERENCES product_options (id, product_id)
        DEFERRABLE INITIALLY DEFERRED,
    -- ...and the value must belong to that option.
    --
    -- Both are checked at COMMIT (deferred), not per statement. Deleting a
    -- draft product cascades to options and to variants as separate
    -- internal steps; checked immediately, the options step would fail
    -- while variant rows still point at them. At commit the cascade is
    -- complete, so it passes — while deleting a value that a variant still
    -- uses is still refused, just at commit time.
    CONSTRAINT product_variant_values_value_fkey FOREIGN KEY (value_id, option_id)
        REFERENCES product_option_values (id, option_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX product_variant_values_value_id_idx ON product_variant_values (value_id);
CREATE INDEX product_variant_values_product_id_idx ON product_variant_values (product_id);

-- +goose Down
DROP TABLE product_variant_values;
DROP TABLE product_variants;
