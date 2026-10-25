# Project status and handoff — interior_api

**Last updated:** 2026-10-25 · **Branch:** `main` at `98c8cba` + this document ·
**Phases done:** 0–9 of 10 (backend only) · **Waiting on:** the developer's Phase 10 decisions

This is the working memory of the project, written for the next Claude Code session (on a
new device) and for the developer. Read it fully before changing anything; then confirm the
facts against the repository (`git log`, the code), which always wins over this document.

---

## 1. What this project is

A **web-first, multi-company furniture commerce platform**: companies publish furniture with
3D models and configurable variants; buyers browse, customize, place furniture in a virtual
room, and request quotations. The differentiator is **seeing fit and appearance before buying**.

- **This repository is the backend only:** Go + Gin + PostgreSQL (pgx/pgxpool).
- **Frontend (later, not started):** SvelteKit + Threlte + Three.js, in a separate folder/repo.
  **Do not create frontend code** until the developer explicitly asks.
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

All of Phases 0–9 are implemented, tested and merged into `main` (30 commits). Highlights:

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

**Not verified yet:** the GitHub Actions workflow has never run (it starts on the first push);
`go test -race` has never run (no C compiler on the old machine — CI provides it).

---

## 4. Setting up the new device

1. **Go 1.26+.** `go.mod` says `go 1.26.0`; an older Go downloads 1.26 automatically
   (`GOTOOLCHAIN=auto`).
2. **PostgreSQL** (18 was used; 16+ should work). Create two databases:
   ```sql
   CREATE DATABASE interior_db;
   CREATE DATABASE interior_test;
   ```
3. **`.env`** (never committed): `copy .env.example .env`, then set your password, port and both
   URLs. The old machine used port **5433**; adjust to your installation.
4. **Migrate and test:**
   ```powershell
   go run ./cmd/migrate up          # interior_db
   go run ./cmd/migrate -test up    # interior_test (tests also migrate it themselves)
   gofmt -l . ; go vet ./... ; go test ./...
   go run ./cmd/api                 # http://localhost:5050/health
   ```
   PowerShell does not load `.env` into the shell, but the code does: `cmd/api`, `cmd/migrate`
   and `cmd/admin` read `.env` themselves. **Integration tests read `TEST_DATABASE_URL` from the
   real environment only** (they skip without it): set it first, e.g.
   `$env:TEST_DATABASE_URL = "postgres://..."` (PowerShell) or `set -a; . ./.env; set +a` (bash).
5. **Development data does not travel with git.** The old machine had demo accounts and media
   (listed in §10). To bring them along, on the old machine run `.\scripts\backup.ps1` and copy
   the two files; restore with the steps in `docs/OPERATIONS.md`. Otherwise just start empty.
6. Optional: `go run ./cmd/admin grant-platform-admin -email you@example.com`.

Paths in docs and scripts assume `D:\postgres-18\bin` — adjust (`scripts/backup.ps1 -PgBin`).

---

## 5. Repository map

```text
cmd/api/            composition root: config → logger → pool → services → router → http.Server
cmd/migrate/        goose runner: up | down | status | create <name>  (-test = TEST_DATABASE_URL)
cmd/admin/          grant/revoke/list platform admins, cleanup-sessions
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
migrations/         00001–00014 SQL (goose), see migrations/README.md for conventions
requests/           .http files (VS Code REST Client) for manual testing of every area
docs/               OPERATIONS.md, SECURITY_REVIEW.md, this file
scripts/            backup.ps1
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

## 7. Phase 10 — pending decisions (ask the developer; nothing built yet)

Phase 10 = prepare the API for the SvelteKit frontend (still **no frontend code**). The
following was presented on 2026-10-25 and is **awaiting an answer**:

1. **How the browser reaches the API (CORS):**
   - **A (recommended for production):** one origin through SvelteKit — the browser talks only to
     `app.example.com`; SvelteKit forwards `/v1/*` to Go internally (Vite dev proxy in
     development). No CORS, first-party Lax cookies keep working. Go then needs a
     `TRUSTED_PROXIES` setting so rate limits/logs see real client IPs.
   - **B:** separate subdomains (`app.` → `api.`): CORS with credentials restricted to
     `ALLOWED_ORIGINS`; Lax cookies still work (same site).
   - **C:** unrelated domains — needs third-party cookies (`SameSite=None`), increasingly blocked
     by browsers; not viable for cookie auth.
   - Recommendation: design for A **and** add B's CORS support behind configuration.
2. **`operationId`s for all 74 operations** (for openapi-typescript / Orval client generation),
   with the drift test also enforcing uniqueness; smoke-test type generation with a pinned tool.
3. **`docs/FRONTEND_HANDOFF.md`:** auth/cookie/Origin flow, error codes, pagination, money/unit
   formatting, Three.js mapping (mm ÷ 1000 → metres, degrees → radians around Y, origin),
   `material_key` ↔ GLTF materials / `KHR_materials_variants`, media URLs, example flows
   (browse, product page, design editor, quote).
4. **`cmd/seed` (development only):** a demo seller, products with variants, images and a
   sample GLB, a customer with a design; refuses to run with `APP_ENV=production`.
5. **`/v1` stability rule:** only additive changes; breaking changes go to `/v2`.

After Phase 10 is accepted, the frontend effort (SvelteKit + Threlte + Three.js) can begin —
only when the developer asks.

---

## 8. Known gaps and accepted risks

See `docs/SECURITY_REVIEW.md` for the full list. Main ones: no CORS yet (Phase 10);
in-memory rate limits (single process only); files type-checked but not virus-scanned;
no `govulncheck` yet; no trusted-proxy config; name search is `ILIKE` (add `pg_trgm` later);
no email (so no invitations, password reset or notifications); no orders/payment.

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

---

## 10. Development data on the old machine (not in git)

Created during live testing; recreate or restore from a backup if wanted:

| Account | Password | What |
|---|---|---|
| `demo@example.com` | `demo password 123` | admin of company `demo-furniture` (ETB) |
| `demo-staff@example.com` | `staff password 123` | staff of `demo-furniture` |
| `demo-customer@example.com` | `customer password 1` | customer with design "My living room" and an accepted quote (62,500 ETB) |

Products: *Oslo 3-Seat Sofa* (published, 48,500 ETB) and *Addis Lounge Chair* (published,
Fabric × Colour, 4 variants, sample PNG + GLB in `./storage`). `interior_db` also contained an
unrelated legacy table `schema_migrations` from an earlier experiment (left untouched).
