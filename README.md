# interior_api

Go + Gin + PostgreSQL backend for the immersive furniture platform.

## Layout

```text
cmd/api/          API server — composition root (config → logger → pool → router → http.Server)
cmd/migrate/      goose migration runner
cmd/admin/        operator CLI (grant/revoke/list platform admins)
internal/config/  environment loading + validation
internal/postgres/  pgxpool setup, WithTx, DBTX (no business SQL); pgtest/ for integration tests
internal/httpapi/ routing, middleware, JSON error format, origin check, rate limiter, health
internal/auth/    identity: users, passwords (Argon2id), sessions, login/logout/me
internal/company/ tenancy: register-company, companies, members, company-scoped authorization
internal/catalog/ products (company CRUD + lifecycle) and the public catalog
internal/apitest/ full-stack test harness (imported only by _test.go files)
migrations/       versioned SQL (see migrations/README.md)
requests/         manual API requests (VS Code REST Client / Postman)
```

Domain packages (`account`, `company`, `catalog`, …) are added under
`internal/` per phase. Each owns its handler, service and store code;
there is no global `models`/`services`/`repositories` layering.

## Setup (Windows, local PostgreSQL 18 on port 5433)

```powershell
copy .env.example .env          # then put your real password in .env
& "D:\postgres-18\bin\psql.exe" -h localhost -p 5433 -U postgres -c "CREATE DATABASE interior_db;"
& "D:\postgres-18\bin\psql.exe" -h localhost -p 5433 -U postgres -c "CREATE DATABASE interior_test;"
go run ./cmd/migrate up
go run ./cmd/migrate -test up
```

## Run

```powershell
go run ./cmd/api
curl http://localhost:5050/health
curl http://localhost:5050/health/database
```

Ctrl+C triggers a graceful shutdown (in-flight requests finish, then the
pool closes).

## Verify

```powershell
gofmt -l .        # must print nothing
go vet ./...
go test ./...     # DB integration tests skip unless TEST_DATABASE_URL is set
```

## API conventions

- Product API lives under `/v1`; `/health` and `/health/database` are
  unversioned operational endpoints.
- Every error response has the same shape:
  `{"error":{"code":"not_found","message":"route not found","request_id":"…"}}`.
  Clients switch on `code`; `message` is human-readable and may change.
- Every response carries `X-Request-ID`; it also appears in the server log line.
- JSON bodies are decoded strictly: unknown fields, trailing data and
  bodies over 1 MiB are rejected.

## Authentication

| Endpoint | Auth | Success |
|---|---|---|
| `POST /v1/auth/register-company` | none | 201 + session cookie |
| `POST /v1/auth/login` | none | 200 + new session cookie |
| `POST /v1/auth/logout` | cookie (optional) | 204, cookie cleared |
| `GET /v1/auth/me` | cookie | 200 user + memberships |

- Session cookie `interior_session`: opaque random token, `HttpOnly`,
  `SameSite=Lax`, `Secure` in production only. The database stores only its
  SHA-256. Lifetime `SESSION_TTL` (default 7 days).
- POST/PATCH/DELETE from a browser must come from an origin listed in
  `ALLOWED_ORIGINS`, otherwise 403 `forbidden_origin` (CSRF defence).
  Requests with no `Origin` header (curl, Postman) are allowed.
- Login and registration are rate limited in memory (per IP, and per email
  for login). This only holds for a single API process.
- Error codes: `validation_failed` (with `details`), `email_taken`,
  `slug_taken`, `invalid_credentials`, `unauthenticated`, `rate_limited`.

## Authorization and company isolation

Company-private routes live under `/v1/companies/:companyID` and pass
three checks in order: session → active membership in an active company
→ role.

| Endpoint | Who |
|---|---|
| `GET /v1/companies/:companyID` | any active member |
| `PATCH /v1/companies/:companyID` (`name`) | `company_admin` |
| `GET /v1/companies/:companyID/members` | any active member |
| `POST /v1/companies/:companyID/members` | `company_admin` — creates a new account |
| `PATCH /v1/companies/:companyID/members/:userID` (`role`, `status`) | `company_admin` |
| `GET /v1/admin/companies?limit=` | platform admin |

- **404, not 403, for other companies**: a non-member, a disabled member, a
  suspended company and a nonexistent ID all look identical (`not_found`),
  so company IDs cannot be probed. Members lacking a role get 403 `forbidden`.
- Every store query is scoped by the authorized member's `company_id`.
- A company always keeps one active admin (409 `last_admin`). Membership
  changes lock the company row, so concurrent changes cannot break this.
- Platform admins are granted only via `go run ./cmd/admin
  grant-platform-admin -email …` and get no implicit access inside companies.

## Catalog

**Units:** dimensions in integer millimetres (`width_mm`, `depth_mm`,
`height_mm`, 1–10000), weight in integer grams (`weight_g`, optional),
price as `price_minor`, an integer in the smallest unit of the company's
currency (4850000 = 48,500.00 ETB). Each company has one `currency`
(default `ETB`), changeable only while it has no products (409
`currency_locked`).

| Endpoint | Who |
|---|---|
| `POST/GET /v1/companies/:companyID/products` | any active member |
| `GET/PATCH/DELETE /v1/companies/:companyID/products/:productID` | any active member; DELETE drafts only |
| `POST …/products/:productID/publish` · `/archive` · `/restore` | any active member |
| `GET /v1/catalog/categories` | public |
| `GET /v1/catalog/products` | public |
| `GET /v1/catalog/products/:companySlug/:productSlug` | public |

- **Lifecycle:** `draft → published → archived → draft`; anything else is
  409 `invalid_transition`. Slugs are unique per company and can change only
  while a product is a draft (409 `slug_locked`), so public URLs stay stable.
- **Public catalog** shows only published products of active companies;
  everything else is 404. Filters: `category`, `company`, `q` (name
  contains), `currency` + `min_price`/`max_price`, `sort=newest|price_asc|price_desc`.
- **Pagination** (both lists): `limit` 1–100 (default 20); responses carry
  `next_cursor` (null on the last page) — send it back unchanged as
  `?cursor=`. Cursors are keyset-based, so inserts while paging never cause
  skipped or repeated items, and a cursor only works with the sort it came from.

## Options and variants

A product can have up to **3 options** (Fabric, Colour, Legs), each with up
to **30 values**, and up to **100 variants** — the combinations actually
sold, each with its own SKU (unique per company), full `price_minor` and
`available` flag. Only listed combinations can be chosen.

Routes live under `/v1/companies/:companyID/products/:productID` (any
active member): `GET /configuration`, `POST /options`,
`PATCH|DELETE /options/:optionID`, `POST /options/:optionID/values`,
`PATCH|DELETE /options/:optionID/values/:valueID`, `POST /variants`,
`POST /variants/generate`, `PATCH|DELETE /variants/:variantID`. Every write
returns the full configuration. See `requests/variants.http`.

- Variants choose values **by name**: `{"options":{"Fabric":"Linen","Colour":"Grey"}}`.
  Every option needs exactly one value; duplicates are 409 `duplicate_combination`.
- Options can be added/removed only on a **draft without variants** (409
  `options_locked`); values can be added any time and removed only if no
  variant uses them (409 `value_in_use`; an option keeps ≥1 value).
- With variants, the product's `price_minor` is the **cheapest available
  variant** ("from" price, kept in sync automatically) and cannot be set
  directly (409 `price_from_variants`).
- Publishing a product with options needs an available variant (409
  `no_available_variants`). Variants of non-draft products are switched off
  (`available: false`), not deleted.
- Values carry optional `swatch_hex` (`#A3B18A`) and `material_key` (GLTF
  material name, used by the 3D viewer in Phase 6).
- The public product page includes `options` and `variants`; unavailable
  variants are listed with `available: false`.

## Go version

`go.mod` requires **Go 1.26** (goose v3.29 and golang.org/x/crypto need it).
With an older local Go, the default `GOTOOLCHAIN=auto` downloads 1.26
automatically the first time you build.
