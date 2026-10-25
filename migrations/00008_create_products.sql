-- Company-owned furniture products.
--
-- Units are fixed by column name: millimetres and grams as integers (no
-- floating-point rounding; the 3D frontend divides by 1000 for metres).
-- price_minor is in the smallest unit of the company's currency
-- (santim, cents): 1250000 = 12,500.00 ETB.

-- +goose Up
CREATE TABLE products (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id   uuid        NOT NULL,
    category_id  uuid        NOT NULL,
    name         text        NOT NULL,
    slug         text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    price_minor  bigint      NOT NULL,
    width_mm     integer     NOT NULL,
    depth_mm     integer     NOT NULL,
    height_mm    integer     NOT NULL,
    weight_g     integer,
    status       text        NOT NULL DEFAULT 'draft',
    published_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT products_company_id_fkey  FOREIGN KEY (company_id)  REFERENCES companies (id)  ON DELETE RESTRICT,
    CONSTRAINT products_category_id_fkey FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE RESTRICT,
    -- Slugs are unique per company: URLs are /:companySlug/:productSlug.
    CONSTRAINT products_company_slug_key UNIQUE (company_id, slug),
    CONSTRAINT products_slug_check       CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) BETWEEN 3 AND 100),
    CONSTRAINT products_name_check       CHECK (btrim(name) <> ''),
    CONSTRAINT products_price_check      CHECK (price_minor >= 0),
    CONSTRAINT products_dimensions_check CHECK (width_mm  BETWEEN 1 AND 10000
                                            AND depth_mm  BETWEEN 1 AND 10000
                                            AND height_mm BETWEEN 1 AND 10000),
    CONSTRAINT products_weight_check     CHECK (weight_g IS NULL OR weight_g > 0),
    CONSTRAINT products_status_check     CHECK (status IN ('draft', 'published', 'archived')),
    -- A published product always has a publish time.
    CONSTRAINT products_published_at_check CHECK (status <> 'published' OR published_at IS NOT NULL)
);

-- Company's own list: newest first, keyset-paginated on (created_at, id).
CREATE INDEX products_company_created_idx ON products (company_id, created_at DESC, id DESC);
CREATE INDEX products_category_id_idx     ON products (category_id);

-- Public catalog sorts. Partial indexes cover only published rows, which
-- is all the public catalog ever reads.
CREATE INDEX products_published_newest_idx ON products (published_at DESC, id DESC) WHERE status = 'published';
CREATE INDEX products_published_price_idx  ON products (price_minor, id)            WHERE status = 'published';

CREATE TRIGGER products_set_updated_at
BEFORE UPDATE ON products
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE products;
