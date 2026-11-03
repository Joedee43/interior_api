# Operations

How to run, back up, restore and maintain the API. Commands are for
Windows PowerShell with PostgreSQL 18 under `D:\postgres-18`; adjust paths.

## Production checklist

| Setting | Value |
|---|---|
| `APP_ENV` | `production` — JSON logs, Gin release mode, `Secure` + `__Host-` session cookie, HSTS |
| HTTPS | Required in production (cookies are `Secure`). Terminate TLS at a reverse proxy or load balancer. |
| `ALLOWED_ORIGINS` | The exact frontend origin(s), e.g. `https://app.example.com` |
| `TRUSTED_PROXIES` | Only the Next.js server/private proxy IPs or CIDRs; forwarding headers from other peers are ignored |
| `ENABLE_CORS` | `false` for the normal same-origin Next.js rewrite; enable only for an intentional separate-origin deployment |
| `DATABASE_URL` | A dedicated database user that owns the schema; never the `postgres` superuser in production |
| `STORAGE_DIR` | An absolute path on a disk that is backed up (uploaded images and 3D models live here) |
| Migrations | Run `go run ./cmd/migrate up` (or the built binary) **before** starting a new API version |
| Proxies | Next.js overwrites `X-Forwarded-For` and `X-Real-IP`; Go accepts them only from `TRUSTED_PROXIES`. Never use a public/unrestricted CIDR. |
| Instances | Run **one** API process: rate limits are in memory per process. |

## Backups

`scripts\backup.ps1` writes two files with the same timestamp:

- `interior_db_<stamp>.dump` — `pg_dump` custom format (compressed)
- `interior_storage_<stamp>.zip` — the media folder

```powershell
.\scripts\backup.ps1                                  # into .\backups (git-ignored)
.\scripts\backup.ps1 -OutDir E:\backups\interior      # somewhere else, e.g. another disk
```

Back up both together: database rows reference media files by key, so a
database without its media (or the reverse) shows broken images.

Schedule it with Windows Task Scheduler (daily is a sensible start) and
copy the files off the machine; a backup on the same disk does not survive
a disk failure.

## Restore

Restore into a **new, empty** database first and check it, rather than over
the live one:

```powershell
$pg = "D:\postgres-18\bin"
& "$pg\psql.exe" -h localhost -p 5433 -U postgres -c "CREATE DATABASE interior_restore;"
& "$pg\pg_restore.exe" --no-owner -h localhost -p 5433 -U postgres -d interior_restore .\backups\interior_db_<stamp>.dump
Expand-Archive .\backups\interior_storage_<stamp>.zip -DestinationPath .\storage-restored
```

Then point `DATABASE_URL` / `STORAGE_DIR` at the restored copies (or swap
the database names) and start the API. Restoring was verified on
2026-10-25: row counts and the migration version matched the source.

## Sessions

Expired or revoked sessions are deleted automatically every hour once they
are more than 30 days old (a background job inside the API). To run it by
hand:

```powershell
go run ./cmd/admin cleanup-sessions -retain 720h
```

Users can sign out everywhere (`POST /v1/auth/logout-all`); changing a
password also revokes every session.

## Development seed

`go run ./cmd/seed` replaces only the documented `demo-furniture` company
and the three `demo*@example.com` accounts. It creates two published products,
images, a KHR-material-variant GLB and a customer design. Re-running it is safe;
old demo media files are removed. The command refuses `APP_ENV=production`.

## Platform admins

```powershell
go run ./cmd/admin grant-platform-admin  -email you@example.com
go run ./cmd/admin revoke-platform-admin -email you@example.com
go run ./cmd/admin list-platform-admins
```

## Health checks

- `GET /health` — the process is up (does not touch the database)
- `GET /health/database` — PostgreSQL is reachable (503 otherwise)

## Continuous integration

`.github/workflows/ci.yml` runs on every push once the repository is on
GitHub: gofmt, `go vet`, staticcheck, build, an OpenAPI-to-TypeScript generation
smoke test, the full test suite **with the race detector** against PostgreSQL
18, and a full migrations down/up round trip.
