-- Shared trigger function so every table keeps updated_at correct even
-- when rows are changed outside the Go code (psql, scripts). Each table
-- attaches it with:
--
--   CREATE TRIGGER <table>_set_updated_at
--   BEFORE UPDATE ON <table>
--   FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION set_updated_at();
