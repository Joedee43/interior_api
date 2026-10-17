# Migrations

Plain SQL files run by [goose](https://github.com/pressly/goose) through
`go run ./cmd/migrate`. The goose version is pinned in `go.mod`, so there is
nothing to install globally.

```powershell
go run ./cmd/migrate status
go run ./cmd/migrate up
go run ./cmd/migrate down            # reverts one migration
go run ./cmd/migrate create add_users
go run ./cmd/migrate -test up        # same, against TEST_DATABASE_URL
```

## Rules

1. **Never edit a migration that has been applied anywhere shared.** Add a
   new one instead. Editing an applied file only changes history on paper.
2. **Every migration has a working `-- +goose Down`.** Before committing, run
   `up`, `down`, `up` against `interior_test`.
3. **One concern per migration** (e.g. `add_users`, `add_company_members`).
4. Multi-statement bodies (functions, `DO` blocks) go between
   `-- +goose StatementBegin` / `-- +goose StatementEnd`.

## Schema conventions

| Thing | Convention |
|---|---|
| Table names | plural `snake_case`: `users`, `company_members` |
| Column names | `snake_case`; foreign keys `<entity>_id` |
| Timestamps | `timestamptz NOT NULL DEFAULT now()` — never `timestamp` without time zone |
| `updated_at` | attach the shared trigger: `CREATE TRIGGER <table>_set_updated_at BEFORE UPDATE ON <table> FOR EACH ROW EXECUTE FUNCTION set_updated_at();` |
| Money | integer minor units (`bigint` cents) + `char(3)` ISO currency, or `numeric(12,2)` — never float |
| Status / enum-like values | `text` + named `CHECK` constraint (easier to evolve than PostgreSQL `ENUM`) |
| Constraint names | explicit: `<table>_<column>_key` (unique), `<table>_<column>_fkey`, `<table>_<rule>_check` — so Go code can map violations with `errors.As(err, &pgErr)` + `pgErr.ConstraintName` |
| Foreign keys | always indexed; choose `ON DELETE` deliberately (`RESTRICT` by default) |
| Tenant tables | carry `company_id NOT NULL` and every query filters on it |
| Primary keys | **decided at the start of Phase 2** (`uuid DEFAULT gen_random_uuid()` vs `bigint GENERATED ALWAYS AS IDENTITY`) |
