# interior_api

Go + Gin + PostgreSQL backend for the immersive furniture platform.

## Layout

```text
cmd/api/          API server — composition root (config → logger → pool → router → http.Server)
cmd/migrate/      goose migration runner
internal/config/  environment loading + validation
internal/postgres/  pgxpool setup (no business SQL)
internal/httpapi/ routing, middleware, JSON error format, health endpoints
migrations/       versioned SQL (see migrations/README.md)
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
