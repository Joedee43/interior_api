-- Product images (gallery) and 3D models. File bytes live in the storage
-- backend under storage_key; this table holds metadata only.

-- +goose Up
CREATE TABLE product_media (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id        uuid        NOT NULL,
    company_id        uuid        NOT NULL,
    kind              text        NOT NULL,
    storage_key       text        NOT NULL,
    -- Detected from the file's bytes at upload, never from the client.
    content_type      text        NOT NULL,
    size_bytes        bigint      NOT NULL,
    sha256            bytea       NOT NULL,
    original_filename text        NOT NULL DEFAULT '',
    -- Images only.
    width             integer,
    height            integer,
    alt_text          text        NOT NULL DEFAULT '',
    position          integer     NOT NULL DEFAULT 0,
    is_primary        boolean     NOT NULL DEFAULT false,
    -- Models only: material names and KHR_materials_variants names found
    -- in the GLB, compared against option values' material_key.
    model_materials   text[]      NOT NULL DEFAULT '{}',
    model_variants    text[]      NOT NULL DEFAULT '{}',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_media_product_fkey FOREIGN KEY (product_id, company_id)
        REFERENCES products (id, company_id) ON DELETE CASCADE,
    CONSTRAINT product_media_storage_key_key UNIQUE (storage_key),
    CONSTRAINT product_media_kind_check      CHECK (kind IN ('image', 'model')),
    CONSTRAINT product_media_type_check      CHECK (
        (kind = 'image' AND content_type IN ('image/jpeg', 'image/png', 'image/webp'))
        OR (kind = 'model' AND content_type = 'model/gltf-binary')),
    CONSTRAINT product_media_size_check      CHECK (size_bytes > 0),
    CONSTRAINT product_media_sha256_check    CHECK (octet_length(sha256) = 32),
    CONSTRAINT product_media_dimensions_check CHECK (
        (kind = 'image' AND width > 0 AND height > 0)
        OR (kind = 'model' AND width IS NULL AND height IS NULL)),
    CONSTRAINT product_media_primary_check   CHECK (NOT is_primary OR kind = 'image'),
    CONSTRAINT product_media_alt_text_check  CHECK (length(alt_text) <= 300),
    CONSTRAINT product_media_filename_check  CHECK (length(original_filename) <= 255),
    CONSTRAINT product_media_position_check  CHECK (position BETWEEN 0 AND 1000)
);

CREATE INDEX product_media_product_id_idx ON product_media (product_id, kind, position);

-- At most one 3D model and at most one primary image per product.
CREATE UNIQUE INDEX product_media_one_model_key   ON product_media (product_id) WHERE kind = 'model';
CREATE UNIQUE INDEX product_media_one_primary_key ON product_media (product_id) WHERE is_primary;

CREATE TRIGGER product_media_set_updated_at
BEFORE UPDATE ON product_media
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE product_media;
