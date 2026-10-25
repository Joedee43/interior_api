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
internal/design/  room templates and customer designs (uses catalog via an interface)
internal/quote/   requests for quotation (uses catalog, design and auth via interfaces)
internal/storage/ file storage interface + local disk store
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

## Images and 3D models

Files are stored by `internal/storage` (a small `Store` interface; today a
`LocalStore` under `STORAGE_DIR`, default `./storage`, git-ignored). Keys are
generated (`media/<uuid>`), never derived from upload names. Writes go to a
temp file and are renamed into place, so a failed upload leaves nothing.

| Endpoint | Who |
|---|---|
| `GET …/products/:productID/media` | member — gallery, model, `material_check` |
| `POST …/products/:productID/images` (multipart `file`, optional `alt_text`) | member |
| `PATCH/DELETE …/products/:productID/images/:mediaID` | member |
| `PUT/DELETE …/products/:productID/model` (multipart `file`) | member |
| `GET /v1/companies/:companyID/media/:mediaID/file` | member, any product status |
| `GET /v1/media/:mediaID` | public, published products of active companies only |

- **Accepted:** JPEG/PNG/WebP images ≤ 10 MB (20 per product); one GLB
  (binary glTF 2.0) model ≤ 50 MB. Type is detected from the file's bytes;
  GLBs referencing external buffers/textures are rejected. Oversized: 413.
  **Files are type-checked, not virus-scanned.**
- The first image is primary; deleting the primary promotes the next.
  Product responses include `primary_image_url`; the public product page
  includes `images` and `model` (with its material names).
- **Material check:** each option value's `material_key` should name a
  material in the GLB (case-insensitive); `GET …/media` lists the ones that
  do not (`missing_material_keys`). Advisory only.
- Public files are served with `Cache-Control: public, max-age=31536000,
  immutable` (a new upload always gets a new id), an ETag, `nosniff` and a
  restrictive CSP; range requests (206) are supported for large models.
- Deleting an image, a model or a draft product deletes the files after the
  database commit; failures are logged and leave a harmless orphan file.
- Upload requests get a 5-minute read/write deadline instead of the server
  defaults, so large models survive slow connections.

## Rooms and saved designs

Customers sign up with `POST /v1/auth/register` (a user with no company);
any logged-in user can keep designs.

**Coordinates** (glTF / Three.js): millimetres, Y up, origin at the room's
north-west floor corner; x runs east along `width_mm`, z runs south along
`depth_mm`. An item's position is the centre of its footprint; furniture
only turns around the vertical axis (`rotation_deg`, stored in [0, 360)).
Furniture is always real size: there is no scale.

| Endpoint | Who |
|---|---|
| `GET /v1/room-templates` · `GET /v1/room-templates/:slug` | public (published) |
| `GET/POST /v1/admin/room-templates` · `PATCH /v1/admin/room-templates/:id` | platform admin |
| `POST/GET /v1/designs` · `GET/PATCH/DELETE /v1/designs/:id` | owner (others get 404) |
| `POST /v1/designs/:id/items` · `PATCH/DELETE /v1/designs/:id/items/:itemId` | owner |

- A design starts from a published template (`{"template":"living-room-5x6"}`)
  or a custom room (1–30 m wide/deep, 2–6 m high). Room `features` (wall
  colour, floor, door/window openings) have a fixed, strictly validated
  shape; openings must fit their wall.
- Placing needs a published product of an active company; products with
  options need one of their own available `variant_id`s. The item's centre
  must be inside the room. Max 200 items. No collision checking.
- Resizing a room that would leave items outside is 409 `items_outside_room`.
- `GET /v1/designs/:id` returns each item with current product details,
  `available` (false once archived/switched off) and an **estimate** — totals
  per seller and currency at current prices. It is not a quote.
- Products and variants placed in designs cannot be deleted (409
  `product_in_use` / `variant_in_use`); archive or switch them off instead.

## Quotations

A customer asks a seller to price products; the seller quotes; the
customer accepts or rejects. Each request concerns **one company**.

| Endpoint | Who |
|---|---|
| `POST /v1/quote-requests/from-design` (`design_id`) | customer — one request per seller in the design |
| `POST /v1/quote-requests` (`items`) | customer — products from one seller |
| `GET /v1/quote-requests` · `GET /v1/quote-requests/:id` | the customer who asked |
| `POST /v1/quote-requests/:id/accept` (`revision`) · `/reject` · `/cancel` | the customer who asked |
| `GET /v1/companies/:companyID/quote-requests` · `GET …/:id` | any active member |
| `POST …/:id/review` · `PUT …/:id/quote` · `POST …/:id/decline` | any active member |

**Workflow** (anything else is 409 `invalid_transition`):

| From | Action | By | To |
|---|---|---|---|
| pending | review | company | reviewing |
| pending, reviewing, quoted | quote (again = revision +1) | company | quoted |
| pending, reviewing | decline | company | declined |
| quoted | accept / reject | customer | accepted / rejected |
| pending, reviewing, quoted | cancel | customer | cancelled |

- **Snapshot:** lines copy product name, SKU, options, dimensions,
  quantity and list price at submit time; later catalog changes never
  alter a request. Identical design pieces become one line with a quantity;
  unavailable design items are skipped and listed in the response.
- **Quoting** prices every line, adds charges (delivery, assembly...), an
  optional `valid_until` date and a note. The total is computed server-side.
  No tax is calculated — state it in the note.
- **Accept** must send the `revision` being accepted (409 `stale_revision`
  if the company revised it meanwhile) and fails after `valid_until` (409
  `quote_expired`). Every action runs under the request's row lock.
- Customers see only their own requests; companies only requests to them
  (others get 404). Submissions are limited to ~20/hour per user.
- Not included yet: notifications, chat, payment/checkout, orders.

## Go version

`go.mod` requires **Go 1.26** (goose v3.29 and golang.org/x/crypto need it).
With an older local Go, the default `GOTOOLCHAIN=auto` downloads 1.26
automatically the first time you build.
