# Project status and handoff — interior_api

**Last updated:** 2026-11-08 · **Branch:** `main` · **Phases done:** 0–10 of 10 (backend), plus
production-deployment support · **Frontend:** `interior_web` Phases 1–9 committed; Phase 10
(accessibility, performance, hardening) built and awaiting merge

This is the working memory of the project, written for the next Claude Code session (on a
new device) and for the developer. Read it fully before changing anything; then confirm the
facts against the repository (`git log`, the code), which always wins over this document.

---

## 1. What this project is

A **web-first, multi-company furniture commerce platform**: companies publish furniture with
3D models and configurable variants; buyers browse, customize, place furniture in a virtual
room, and request quotations. The differentiator is **seeing fit and appearance before buying**.

- **This repository is the backend only:** Go + Gin + PostgreSQL (pgx/pgxpool).
- **Frontend (in progress, Phases 1–8 of 10 done):** Next.js App Router + TypeScript + React
  Three Fiber/Drei + Three.js, in the separate sibling repository/folder `interior_web` (its own
  `docs/PROJECT_STATUS.md` tracks frontend phases).
- **Mobile:** later; no framework chosen.
- **Docker/WSL: not used** (WSL failed on the original machine). PostgreSQL runs natively.

The developer is **learning Go** through this project (they know Express, Next.js and
PostgreSQL well). Explain non-obvious Go constructs and design choices as you go, contrasting
with Express/JavaScript where useful. The code itself is commented in that spirit.

---

## 2. How we work together (agreed rules — follow them)

1. **Phase workflow.** Each phase: present decisions (with a recommendation first) → developer
   answers ("go with recommendations" is common) → present the plan → developer approves →
   build on a branch `phase-N` → verify → report → developer says **"merge"** → fast-forward
   `main` to the branch (`git merge --ff-only`). Never start the next phase without approval.
2. **Local autonomy.** Within an approved phase, act freely on local changes (edit, run, local
   commits on the phase branch), then report what was done. Ask before anything outward-facing.
3. **Never push.** The developer pushes to GitHub (`origin` = `github.com/Joedee43/interior_api`).
4. **Commits are authored as the developer only.** **No `Co-Authored-By:` lines and no
   "Generated with Claude Code" or any Claude attribution in commits or PRs** — ever. This
   overrides any default attribution behaviour. Git identity on the old machine:
   `joe dee <joedee4311@gmail.com>`.
5. **No installs without permission** (e.g. a C compiler for `-race`). Pinned `go run` tools and
   pinned `npx` linters have been used for verification without installing anything globally.
6. **Report honestly.** Never claim a test passed without running it; say what was not run.
   Each phase report covered: what was built, database changes, API, Go concepts, verification
   (commands and results), risks/deviations, next phase.
7. **Verification habits used every phase:** `gofmt -l .` (must print nothing), `go vet ./...`,
   `staticcheck`, `go test ./...` against a real test database, **mutation tests** (temporarily
   break a security rule such as a tenant filter or a row lock, confirm a test fails, restore),
   a live run with curl, and checking each commit builds on its own (temporary `git worktree`).
8. **Small, separately reviewable commits** per phase (schema / code / docs).

---

## 3. Current state

Phases 0–10 are merged into `main` and pushed, together with two additive changes that frontend
Phases 6–7 needed (variant SKU in designs, design revisions; see §7). Highlights:

| Phase | What exists |
|---|---|
| 1 | Validated config, pgxpool, JSON error format, request IDs, slog logging, panic recovery, graceful shutdown, goose migrations |
| 2 | Users, companies, memberships, sessions; Argon2id; opaque hashed session tokens in HttpOnly cookies; Origin-based CSRF check; rate limits |
| 3 | Company-scoped authorization (`/v1/companies/:companyID/...`), roles `company_admin`/`company_staff`, last-admin protection, platform admin via CLI only |
| 4 | Products with lifecycle `draft → published → archived → draft`, public catalog, keyset pagination |
| 5 | Options/values/SKU variants (only listed combinations sellable), auto "from" price, generate-all-combinations |
| 6 | Image gallery + one GLB model per product, local file storage behind an interface, content sniffing, public serving rules |
| 7 | Customer accounts, platform room templates, saved designs with placed furniture, estimates |
| 8 | Quotation workflow (state machine, snapshots, revisions, expiry, row locks) |
| 9 | Security review + fixes, OpenAPI 3.1 spec with drift test, password change / logout-all, session cleanup job, GitHub Actions CI, backups |
| 10 | Next.js boundary: trusted proxies, optional exact-origin CORS, stable OpenAPI operation IDs/types, frontend handoff and repeatable demo seed |

**Not verified yet:** the GitHub Actions workflow has never run (it starts on the first push);
`go test -race` has never run (no C compiler on the old machine — CI provides it).

---

## 4. Setting up the new device

The project now lives on a Mac (macOS, zsh). The original Windows machine is noted where it differs.

1. **Go 1.26+.** `go.mod` says `go 1.26.0`; a newer Go works, an older one downloads 1.26
   automatically (`GOTOOLCHAIN=auto`).
2. **PostgreSQL 16+.** The Mac uses Homebrew `postgresql@17` on the default port **5432**
   (`brew services start postgresql@17`; local connections as your macOS user need no password).
   Windows used PostgreSQL 18 on port **5433**. Create both databases:
   ```bash
   createdb interior_db
   createdb interior_test
   ```
3. **`.env`** (never committed): `cp .env.example .env`, then set `DATABASE_URL` and
   `TEST_DATABASE_URL`, e.g. `postgres://<macos-user>@localhost:5432/interior_db?sslmode=disable`
   and the same URL ending in `/interior_test?sslmode=disable`. Keep both lines uncommented.
4. **Migrate and test** (from `interior_api/`):
   ```bash
   go run ./cmd/migrate up          # interior_db
   go run ./cmd/migrate -test up    # interior_test (tests also migrate it themselves)
   go run ./cmd/migrate status      # 00001–00015 must all be applied
   gofmt -l . ; go vet ./...
   set -a; . ./.env; set +a; go test ./...
   go run ./cmd/api                 # http://localhost:5050/health
   ```
   `cmd/api`, `cmd/migrate`, `cmd/admin` and `cmd/seed` read `.env` themselves. **Integration
   tests read `TEST_DATABASE_URL` from the real environment only** (they skip without it), hence
   the `set -a` line (PowerShell: `$env:TEST_DATABASE_URL = "postgres://..."`).
5. **Development data does not travel with git.** `go run ./cmd/seed` recreates the demo
   accounts, catalog, GLB and design listed in §10. Restoring a backup is optional
   (`docs/OPERATIONS.md`).
6. Optional: `go run ./cmd/admin grant-platform-admin -email you@example.com`.

`scripts/backup.ps1` and the restore commands in `docs/OPERATIONS.md` are Windows-only (they
assume `D:\postgres-18\bin` and port 5433). On the Mac, use Homebrew's `pg_dump`/`pg_restore`
directly with the same flags.

---

## 5. Repository map

```text
cmd/api/            composition root: config → logger → pool → services → router → http.Server
cmd/migrate/        goose runner: up | down | status | create <name>  (-test = TEST_DATABASE_URL)
cmd/admin/          grant/revoke/list platform admins, cleanup-sessions
cmd/seed/           repeatable development-only frontend demo accounts, catalog, GLB and design
internal/config/    env loading + validation (Load/FromLookup)
internal/postgres/  pgxpool, WithTx, DBTX interface, IsUniqueViolation; pgtest/ (test DB helper)
internal/httpapi/   router, middleware (request id, logging, recovery, security headers,
                    origin check, rate limiter), JSON errors, DecodeJSON, ValidUUID
internal/auth/      identity: users, Argon2id, sessions, login/logout/me/register/password
internal/company/   tenancy: register-company, companies, members, RequireMember/RequireRole
internal/catalog/   products, lifecycle, public catalog, options/variants, media, placement API
internal/storage/   Store interface + LocalStore (atomic writes, safe keys)
internal/design/    room templates, designs, placed items (uses catalog via interface)
internal/quote/     quotation workflow (uses catalog, design, auth via interfaces)
internal/apidocs/   openapi.yaml (embedded, GET /openapi.yaml) + drift test
internal/apitest/   full-stack test harness (only imported by _test.go files)
migrations/         00001–00015 SQL (goose), see migrations/README.md for conventions
requests/           .http files (VS Code REST Client) for manual testing of every area
docs/               OPERATIONS.md, SECURITY_REVIEW.md, FRONTEND_HANDOFF.md, DEPLOYMENT.md, this file
scripts/            backup.ps1 (Windows development machine)
deploy/             production kit: Caddyfile, systemd units, backup.sh + daily timer
.github/workflows/  ci.yml (gofmt, vet, staticcheck, build, tests with -race, migrations)
```

**Package dependency direction** (Go forbids import cycles; keep it this way):
`httpapi ← auth ← company ← catalog ← design ← quote` (each may import those to its left).
Lower packages never import higher ones; when a lower package needs something from a higher
one, it declares a **small interface on the consuming side** (e.g. `auth.MembershipLister`
implemented by `company.Service`; `design.Catalog`, `quote.Catalog/Designs/Users`) with a
compile-time check `var _ Iface = (*Impl)(nil)`.

---

## 6. Key decisions (do not silently change)

**General**
- IDs: `uuid` (`gen_random_uuid()`). Timestamps `timestamptz`. Named constraints everywhere
  (Go maps violations by constraint name). Status-like columns are `text` + `CHECK`.
- Units: lengths **integer millimetres**, weights **integer grams**, money **integer minor units**
  (`price_minor`, bigint). **One currency per company** (`companies.currency`, default ETB),
  locked once the company has products.
- Errors: `{"error":{"code","message","details?","request_id"}}`; clients switch on `code`.
  Another tenant's resource returns **404** (indistinguishable from "does not exist"); missing
  role returns 403.
- Pagination: opaque keyset cursors (`next_cursor`, pass back as `cursor`), `limit` 1–100.
- JSON bodies are strict (unknown fields rejected), capped at 1 MiB (uploads have own limits).

**Auth / tenancy**
- Server-side sessions: 256-bit random token in cookie `interior_session`
  (`__Host-interior_session` in production), only SHA-256 stored, 7-day TTL, rotated on login,
  `SameSite=Lax`, HttpOnly, Secure in production. Argon2id m=64 MiB, t=1, p=4.
- CSRF: state-changing `/v1` requests from browsers must carry an `Origin` in `ALLOWED_ORIGINS`.
- Company self-registration creates an **active** company and auto-logs-in. Staff accounts are
  created by an admin (no invitations yet). Platform admins: CLI only, no implicit company access.

**Catalog**
- 9 platform-wide categories. Product slug unique per company; locked after draft.
  Public URL: `/v1/catalog/products/:companySlug/:productSlug`. Staff may manage products.
- Delete only drafts; published/archived are archived/restored instead.
- **Variants are SKU rows**: only listed combinations can be chosen; each has its full price and
  an `available` flag (no stock quantities). Max 3 options × 30 values, 100 variants.
  Options add/remove only on a draft without variants. Product `price_minor` is kept equal to
  the cheapest available variant. Publishing a product with options needs an available variant.
- Option values may carry `swatch_hex` and `material_key` (= a GLTF material name in the model).

**Media**
- Storage behind `storage.Store` (`LocalStore` under `STORAGE_DIR`, default `./storage`,
  git-ignored with the anchored rule `/storage/`). Keys `media/<uuid>`, never user filenames.
- Images JPEG/PNG/WebP ≤ 10 MB, 20 per product, first is primary. One GLB (glTF 2.0, self-
  contained) ≤ 50 MB. Type detected from bytes. Public `GET /v1/media/:id` only for published
  products of active companies (immutable caching, Range support); members use
  `/v1/companies/:companyID/media/:id/file`. `material_check` reports material_keys missing
  from the GLB (advisory).

**Rooms / designs**
- Coordinates (glTF/Three.js): **Y up, millimetres, origin at the room's north-west floor
  corner; x east (width), z south (depth)**; item position = centre of its footprint; rotation
  only around Y (`rotation_deg` in [0, 360)); no scaling.
- Rooms are rectangles + typed `features` JSON (wall colour, floor, door/window openings that
  must fit their wall). Templates are platform-owned (3 seeded). Designs are customer-owned
  (login required — no guest drafts), copy the template at creation, max 200 items, centre must
  be in the room, no collision checks. Variant required when the product has options.
- Placed products/variants cannot be deleted (409 `product_in_use` / `variant_in_use`).

**Quotes**
- One request per company; a design submission is split per seller (shared `submission_id`);
  lines are snapshots. Statuses: pending → reviewing → quoted → accepted/rejected (customer),
  cancelled (customer), declined (company). Company quote prices every line + charges +
  optional `valid_until` + note; re-sending = revision + 1. Accept must send the revision seen
  (`stale_revision`) and fails after expiry. No tax, notifications, payment or orders yet.

---

## 7. Phase 10 — complete on `main`

Phase 10 prepared the API for the Next.js frontend. The developer accepted these decisions on
2026-10-30 and approved implementation on 2026-11-03:

1. **Browser/API topology:** use one origin through Next.js. In development and production,
   Next.js rewrites `/v1/*` to Go; the browser sees only the frontend origin, so first-party Lax
   cookies work without CORS. Also provide optional credentialed CORS, restricted to exact
   `ALLOWED_ORIGINS`, for a deliberate same-site `app.` → `api.` deployment.
2. **Proxy trust and client IP:** add `TRUSTED_PROXIES`; Go trusts only the Next.js server/private
   proxy network. Rewrites carry forwarding headers, and Next.js server-side fetches explicitly
   overwrite `X-Forwarded-For` and `X-Real-IP` with the client IP resolved from trusted request
   context. Never trust arbitrary browser-supplied forwarding chains.
3. **OpenAPI client contract:** add unique `operationId`s to all operations, enforce uniqueness
   in the drift test, and smoke-test generated TypeScript with a pinned tool.
4. **`docs/FRONTEND_HANDOFF.md`:** document auth/cookies, errors, pagination, units/money, media
   and example flows. `ALLOWED_ORIGINS` must contain the frontend origin because forwarded browser
   mutations retain `Origin`; server-side calls carry no `Origin` and are permitted by
   `checkOrigin`. Convert mm ÷ 1000 only at the Three.js boundary. Positive `rotation_deg` is
   counter-clockwise from above and maps directly to positive Three.js Y rotation.
5. **GLB contract:** self-contained glTF 2.0 GLBs use metres, Y-up, real scale, a floor-level
   centred pivot and `KHR_materials_variants`. Each sellable SKU has an exactly matching KHR
   variant name; every `material_key` names an embedded material. The frontend never resizes a
   model and warns in development when any bounding-box dimension differs from the catalog by
   more than 5%. A seller-side size check beside `material_check` is a later backend item.
6. **Development seed:** add `cmd/seed` with a demo seller, variants, images, a conforming sample
   GLB, a customer and a design. It is repeatable and refuses to run in production.
7. **Version stability:** `/v1` changes are additive only; breaking changes go to `/v2`.

Verification completed: formatting, vet, staticcheck, build, all tests against PostgreSQL,
OpenAPI TypeScript generation, production seed refusal, repeatable seed integration, a trusted-
proxy mutation test, live seeded catalog/GLB requests and clean shutdown. Every Phase 10 commit
also builds independently.

Phase 10 is merged; frontend work continues in `interior_web`. Its ten delivery steps are
separate phases following decisions → plan → approval → branch → verify → report → developer
says **"merge"**.

Frontend Phase 6 adds one additive design response field: `DesignItem.product.sku`
is the exact variant SKU or `null` for products without variants. The value was
already resolved by the catalog placement service; no migration or persistence
change was required.

Frontend Phase 7 adds aggregate design revisions for conflict-safe autosave.
Migration 00015 gives each design a monotonic revision. Detail/create/mutation
responses expose it as an ETag, and every design/item mutation requires the
latest value through `If-Match`. Competing editors receive `409 stale_revision`;
failed mutations roll back their revision increment with the transaction. Deleting a design
returns `204` without an ETag (there is no next revision). The optional CORS preflight allows
`If-Match` (fixed 2026-11-08; it was missing).

Contract fixes for frontend Phase 9 (2026-11-08): `omitempty` had removed fields the frontend
requires, so real responses broke where the mock API did not. Quote **detail** responses
(single request, submissions, actions) now always include `lines` and `charges` arrays via
`requestDetailDTO`, which embeds the list-row `requestDTO`. Lists still omit both. Media always
includes `is_primary`, `materials` and `variants`. Before this, a real quote submission showed
an error after succeeding, products with 2+ images failed to load, and GLBs with no named
materials/variants broke the product page. Unit tests in `quote/handler_test.go` and
`catalog/media_handler_test.go` pin the JSON shapes without a database.

Production deployment (frontend Phase 10, 2026-11-08): the developer chose **one Linux server**
(Caddy → Next.js → Go → PostgreSQL, no Docker).
- `SERVER_HOST` (default empty = every interface) lets production listen on `127.0.0.1` only.
  `Addr()` uses `net.JoinHostPort`, which also brackets IPv6 hosts.
- With `APP_ENV=production`, config rejects any non-`https://` entry in `ALLOWED_ORIGINS`: the
  session cookie is Secure, so such an origin could never stay signed in. A mutation check
  confirmed the tests catch its removal.
- `deploy/` holds the Caddyfile (HTTPS, HSTS, `X-Real-IP` overwrite), hardened systemd units for
  API and web, and `backup.sh` with a daily timer.
- `docs/DEPLOYMENT.md` walks through the whole setup and ends with a post-deploy checklist. Nothing
  has been deployed yet.
- `.env.example` needs the new `SERVER_HOST` line added by the developer: the standing rules
  keep Claude out of `.env*` files.

---

## 8. Known gaps and accepted risks

See `docs/SECURITY_REVIEW.md` for the full list. Main ones: in-memory rate limits (single process
only); files type-checked but not virus-scanned; no `govulncheck` yet; name search is `ILIKE`
(add `pg_trgm` later); no email (so no invitations, password reset or notifications); no
seller-side model-size check yet; no orders/payment.

---

## 9. Lessons learned (avoid repeating these mistakes)

- `core.autocrlf=true` on Windows: `.gitattributes` forces LF for Go/SQL so gofmt stays clean.
- An unanchored `.gitignore` rule `storage/` also matched `internal/storage/` — use `/storage/`.
- `ON DELETE RESTRICT` violations are SQLSTATE **23001**, not 23503 (`isForeignKeyViolation`
  accepts both). Immediate FK checks break multi-step cascades → selection FKs are
  `DEFERRABLE INITIALLY DEFERRED`.
- Unique **indexes** (e.g. on `lower(name)`) report the index name as the constraint name.
- `http.Header` keys are canonical — set headers with `.Add/.Set`, not map assignment.
- Rounding can produce `-0` (encoded as `-0` in JSON) — normalize.
- Random hex ids can contain words like "bad" — don't assert on substrings of random data.
- Never start two API servers at once (port 5050 conflict invalidates checks).
- `perl -pi` replacements turn `\n` inside Go strings into real newlines — check after editing.
- Windows has no `kill -INT`; graceful shutdown was verified with a small helper that sends
  `CTRL_BREAK_EVENT` (Go delivers it as `os.Interrupt`).
- The repo `go.mod` was bumped to Go 1.26 by `go get` (goose) in Phase 1 without being noticed
  at first; check `go.mod` after every `go get`.
- `interior_web`'s mock API (`tests/mock-api.mjs`) must answer exactly like Go. It sent an ETag on
  design DELETE, which Go does not, and so hid a frontend bug until a review against this code.
- A new request header used by the frontend (like `If-Match`) must also be added to the optional
  CORS preflight's `Access-Control-Allow-Headers`.
- Never put `omitempty` on a response field the frontend requires: it drops `false`, `0`, `""`
  and empty slices, not just missing values. Also build response slices with `make` (or `[]T{}`),
  because a nil slice encodes as `null`, not `[]`.

---

## 10. Development data (not in git)

Created repeatably by `go run ./cmd/seed` (the command replaces only these documented identities):

| Account | Password | What |
|---|---|---|
| `demo@example.com` | `demo password 123` | admin of company `demo-furniture` (ETB) |
| `demo-staff@example.com` | `staff password 123` | staff of `demo-furniture` |
| `demo-customer@example.com` | `customer password 1` | customer with design "My living room" and a placed grey chair |

Products: *Oslo 3-Seat Sofa* (published, 48,500 ETB) and *Addis Lounge Chair* (published,
Colour with grey/blue SKU variants, generated PNG + conforming `KHR_materials_variants` GLB in
`./storage`). The seed is safe to rerun and refuses `APP_ENV=production`.
