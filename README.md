# interior_api

Go + Gin + PostgreSQL backend for the immersive furniture platform.

## Layout

```text
cmd/api/          API server — composition root (config → logger → pool → router → http.Server)
cmd/migrate/      goose migration runner
internal/config/  environment loading + validation
internal/postgres/  pgxpool setup, WithTx, DBTX (no business SQL); pgtest/ for integration tests
internal/httpapi/ routing, middleware, JSON error format, origin check, rate limiter, health
internal/auth/    users, passwords (Argon2id), sessions, register/login/logout/me
internal/company/ companies and memberships
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
