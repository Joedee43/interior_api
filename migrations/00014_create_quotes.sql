-- Requests for quotation: a customer asks one company to price a set of
-- products; the company answers with prices, charges and a validity date;
-- the customer accepts or rejects.
--
-- Lines are a SNAPSHOT taken at submit time (names, SKU, options, sizes,
-- list price). product_id/variant_id are kept only for traceability and
-- become NULL if the catalog row is ever deleted; the quote stays complete.

-- +goose Up
CREATE TABLE quote_requests (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- One customer submit can create several requests (one per seller);
    -- they share a submission_id.
    submission_id      uuid        NOT NULL,
    company_id         uuid        NOT NULL,
    customer_id        uuid        NOT NULL,
    design_id          uuid,
    status             text        NOT NULL DEFAULT 'pending',
    -- 0 until the first quote; +1 for every quote sent.
    revision           integer     NOT NULL DEFAULT 0,
    currency           char(3)     NOT NULL,
    contact_name       text        NOT NULL,
    contact_email      text        NOT NULL,
    contact_phone      text        NOT NULL DEFAULT '',
    delivery_address   text        NOT NULL DEFAULT '',
    customer_note      text        NOT NULL DEFAULT '',
    company_note       text        NOT NULL DEFAULT '',
    valid_until        date,
    quoted_total_minor bigint,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    quoted_at          timestamptz,
    decided_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT quote_requests_company_fkey  FOREIGN KEY (company_id)  REFERENCES companies (id) ON DELETE RESTRICT,
    CONSTRAINT quote_requests_customer_fkey FOREIGN KEY (customer_id) REFERENCES users (id)     ON DELETE RESTRICT,
    CONSTRAINT quote_requests_design_fkey   FOREIGN KEY (design_id)   REFERENCES designs (id)   ON DELETE SET NULL,
    CONSTRAINT quote_requests_status_check  CHECK (status IN ('pending', 'reviewing', 'quoted', 'accepted', 'rejected', 'cancelled', 'declined')),
    CONSTRAINT quote_requests_revision_check CHECK (revision >= 0),
    CONSTRAINT quote_requests_total_check    CHECK (quoted_total_minor IS NULL OR quoted_total_minor >= 0),
    -- Being (or having been) quoted requires a total and a revision.
    CONSTRAINT quote_requests_quoted_check   CHECK (status NOT IN ('quoted', 'accepted', 'rejected')
                                                    OR (quoted_total_minor IS NOT NULL AND revision >= 1)),
    CONSTRAINT quote_requests_text_check     CHECK (length(contact_phone) <= 40 AND length(delivery_address) <= 500
                                                    AND length(customer_note) <= 2000 AND length(company_note) <= 2000)
);

CREATE INDEX quote_requests_customer_idx   ON quote_requests (customer_id, submitted_at DESC, id DESC);
CREATE INDEX quote_requests_company_idx    ON quote_requests (company_id, submitted_at DESC, id DESC);
CREATE INDEX quote_requests_submission_idx ON quote_requests (submission_id);

CREATE TRIGGER quote_requests_set_updated_at
BEFORE UPDATE ON quote_requests
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE quote_lines (
    id                      uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id              uuid    NOT NULL,
    position                integer NOT NULL,
    product_id              uuid,
    variant_id              uuid,
    product_name            text    NOT NULL,
    product_slug            text    NOT NULL,
    sku                     text,
    options                 jsonb   NOT NULL DEFAULT '{}',
    width_mm                integer NOT NULL,
    depth_mm                integer NOT NULL,
    height_mm               integer NOT NULL,
    quantity                integer NOT NULL,
    list_unit_price_minor   bigint  NOT NULL,
    quoted_unit_price_minor bigint,
    line_note               text    NOT NULL DEFAULT '',

    CONSTRAINT quote_lines_request_fkey FOREIGN KEY (request_id) REFERENCES quote_requests (id) ON DELETE CASCADE,
    CONSTRAINT quote_lines_product_fkey FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE SET NULL,
    CONSTRAINT quote_lines_variant_fkey FOREIGN KEY (variant_id) REFERENCES product_variants (id) ON DELETE SET NULL,
    CONSTRAINT quote_lines_quantity_check CHECK (quantity BETWEEN 1 AND 999),
    CONSTRAINT quote_lines_price_check    CHECK (list_unit_price_minor >= 0
                                             AND (quoted_unit_price_minor IS NULL OR quoted_unit_price_minor >= 0)),
    CONSTRAINT quote_lines_note_check     CHECK (length(line_note) <= 500)
);

CREATE INDEX quote_lines_request_idx ON quote_lines (request_id, position);
CREATE INDEX quote_lines_product_idx ON quote_lines (product_id);
CREATE INDEX quote_lines_variant_idx ON quote_lines (variant_id);

CREATE TABLE quote_charges (
    id           uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id   uuid    NOT NULL,
    position     integer NOT NULL,
    label        text    NOT NULL,
    amount_minor bigint  NOT NULL,

    CONSTRAINT quote_charges_request_fkey  FOREIGN KEY (request_id) REFERENCES quote_requests (id) ON DELETE CASCADE,
    CONSTRAINT quote_charges_label_check   CHECK (btrim(label) <> '' AND length(label) <= 100),
    CONSTRAINT quote_charges_amount_check  CHECK (amount_minor BETWEEN 0 AND 1000000000000)
);

CREATE INDEX quote_charges_request_idx ON quote_charges (request_id, position);

-- +goose Down
DROP TABLE quote_charges;
DROP TABLE quote_lines;
DROP TABLE quote_requests;
