-- Platform-curated rooms that a design can start from.
--
-- Coordinates used across rooms and designs (glTF / Three.js convention):
-- right-handed, Y up, millimetres. The origin is the room's north-west
-- floor corner; x runs east along the width, z runs south along the depth,
-- y runs up to the ceiling.

-- +goose Up
CREATE TABLE room_templates (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text        NOT NULL,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    width_mm    integer     NOT NULL,
    depth_mm    integer     NOT NULL,
    height_mm   integer     NOT NULL,
    -- Validated in Go against a fixed shape (wall colour, floor, openings).
    features    jsonb       NOT NULL DEFAULT '{}',
    status      text        NOT NULL DEFAULT 'draft',
    sort_order  integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT room_templates_slug_key        UNIQUE (slug),
    CONSTRAINT room_templates_slug_check      CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) BETWEEN 3 AND 60),
    CONSTRAINT room_templates_name_check      CHECK (btrim(name) <> '' AND length(name) <= 100),
    CONSTRAINT room_templates_size_check      CHECK (width_mm BETWEEN 1000 AND 30000
                                                 AND depth_mm BETWEEN 1000 AND 30000
                                                 AND height_mm BETWEEN 2000 AND 6000),
    CONSTRAINT room_templates_features_check  CHECK (jsonb_typeof(features) = 'object' AND octet_length(features::text) <= 65536),
    CONSTRAINT room_templates_status_check    CHECK (status IN ('draft', 'published'))
);

CREATE TRIGGER room_templates_set_updated_at
BEFORE UPDATE ON room_templates
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO room_templates (slug, name, description, width_mm, depth_mm, height_mm, features, status, sort_order) VALUES
    ('home-office-3x4', 'Home office 3×4 m', 'A compact home office with one door and a window.',
     3000, 4000, 2700,
     '{"wall_color":"#F2EFE9","floor":"oak","openings":[{"type":"door","wall":"south","offset_mm":300,"width_mm":900,"height_mm":2100,"sill_mm":0},{"type":"window","wall":"north","offset_mm":900,"width_mm":1200,"height_mm":1300,"sill_mm":900}]}',
     'published', 10),
    ('living-room-5x6', 'Living room 5×6 m', 'An open living room with two windows.',
     5000, 6000, 2800,
     '{"wall_color":"#FFFFFF","floor":"oak","openings":[{"type":"door","wall":"east","offset_mm":500,"width_mm":900,"height_mm":2100,"sill_mm":0},{"type":"window","wall":"west","offset_mm":1000,"width_mm":1800,"height_mm":1500,"sill_mm":700},{"type":"window","wall":"west","offset_mm":3500,"width_mm":1800,"height_mm":1500,"sill_mm":700}]}',
     'published', 20),
    ('open-office-8x10', 'Open office 8×10 m', 'A shared office floor for a small team.',
     8000, 10000, 3000,
     '{"wall_color":"#E9ECEF","floor":"carpet","openings":[{"type":"door","wall":"south","offset_mm":3500,"width_mm":1800,"height_mm":2200,"sill_mm":0}]}',
     'published', 30);

-- +goose Down
DROP TABLE room_templates;
