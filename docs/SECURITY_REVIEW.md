# Security review — Phase 9 (2026-10-25)

Scope: the whole API as of Phase 8 (auth, companies, catalog, variants,
media, designs, quotes). Method: code read-through of every handler,
service and SQL builder; automated checks (`go vet`, staticcheck,
OpenAPI drift test, Redocly lint); mutation tests on authorization paths in
Phases 3–8. Not covered: penetration testing, dependency CVE scanning
(run `govulncheck` in CI later), infrastructure.

## Fixed in this phase

| # | Finding | Fix |
|---|---|---|
| 1 | Invalid JSON errors echoed Go internals (`Go struct field createRequest.price_minor of type int64`). | `DecodeJSON` maps json errors to plain messages (`field "price_minor" must be a whole number`). Tested. |
| 2 | No defensive headers on API responses. | Every response: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`; HSTS in production. |
| 3 | Session cookie could be shadowed by a sibling subdomain. | Production cookie is `__Host-interior_session` (browser enforces Secure, Path=/, no Domain). |
| 4 | No way to revoke a stolen session or change a password. | `POST /v1/auth/logout-all`; `POST /v1/auth/password` (revokes all sessions, issues a new one, rate limited). |
| 5 | Dead sessions accumulated forever. | Hourly cleanup job + `cmd/admin cleanup-sessions`. |
| 6 | Header size used Go's 1 MiB default. | `MaxHeaderBytes` 64 KiB. |

## Verified (no change needed)

- **Tenant isolation.** Every company-, user- and customer-scoped query
  filters by an id taken from the session or the authorized membership,
  never from the request alone. Each private area has a test where another
  company/user swaps ids (including another tenant's ids through one's own
  URL), and mutation tests confirmed those tests fail when the filter is
  removed (Phases 3, 4, 6, 7, 8).
- **SQL injection.** All dynamic SQL builders (`catalog.sqlBuilder`,
  `updateRow`, `design.setClause`, quote scopes) take column names from
  constants in code and values only as `$n` parameters.
- **Passwords.** Argon2id (64 MiB, t=1, p=4) with per-hash salt; constant-time
  comparison; timing equalized for unknown emails; generic login errors.
- **Sessions.** 256-bit random tokens; only SHA-256 stored; HttpOnly,
  SameSite=Lax; rotated on login; revocable.
- **CSRF.** State-changing `/v1` requests from a browser must carry an
  allowed `Origin`; combined with SameSite=Lax cookies.
- **Uploads.** Type detected from content; size capped while streaming;
  GLBs referencing external resources rejected; storage keys generated
  (no path traversal, tested); files served with `nosniff` and a sandboxed
  CSP; drafts' files are not public.
- **Error responses** never include database errors; 500s log details
  server-side with the request id.
- **Concurrency.** Row locks on companies (membership/currency), products
  (variants), designs and quote requests; races tested, with mutation
  tests showing the failures the locks prevent.

## Accepted risks / later work

| Risk | Note |
|---|---|
| Email enumeration on registration (`email_taken`) | Normal for sign-up; rate limited. |
| In-memory rate limits | Single-process only; move to PostgreSQL/Redis before running several instances. |
| No CORS headers yet | The frontend on another origin cannot read responses until CORS is configured — **Phase 10** (depends on deployment topology). |
| Files are not virus-scanned | Type-checked only; documented. |
| No `govulncheck` | Add to CI when the repository is pushed. |
| Trusted proxies | None configured; rate limits key on the proxy's IP when behind one. |
| Name search uses `ILIKE '%q%'` | Fine at current scale (measured < 5 ms at 5,000 products); add a `pg_trgm` index when the catalog grows. |
