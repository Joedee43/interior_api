# Deployment: one Linux server

How to run Interior in production on a single Ubuntu server: **Caddy → Next.js → Go API →
PostgreSQL**, with uploaded media on local disk. No Docker. The files referenced below live in
`deploy/`. Replace `app.example.com` with your domain throughout.

```text
browser ──HTTPS──▶ Caddy :443 ──▶ Next.js 127.0.0.1:3000 ──/v1/*──▶ Go API 127.0.0.1:5050 ──▶ PostgreSQL
                   (TLS, HSTS,      (pages, rewrites,               (sessions, rules,           (local socket
                    X-Real-IP)       security headers)               media on disk)              or 127.0.0.1)
```

Only ports 22, 80 and 443 are open. The browser only ever talks to `app.example.com`, so the
session cookie is first-party and no CORS is needed (`docs/FRONTEND_HANDOFF.md`).

## 1. Server

- Ubuntu 24.04 LTS, 2 GB RAM or more (the Next.js build needs it), SSH key login only.
- A DNS `A` (and `AAAA` if you use IPv6) record for `app.example.com` pointing at the server.
  It must resolve **before** Caddy starts, or the certificate request fails.
- Firewall:
  ```bash
  sudo ufw allow OpenSSH
  sudo ufw allow 80,443/tcp
  sudo ufw enable
  ```

## 2. Packages

```bash
sudo apt update && sudo apt install -y postgresql   # 16 on 24.04; 16+ is supported
# Caddy: follow https://caddyserver.com/docs/install#debian-ubuntu-raspbian (official apt repo)
# Node.js: the major version in interior_web/.nvmrc (24), e.g. from NodeSource
node --version
```

Go is **not** needed on the server: build the API on your Mac for Linux (step 5).

## 3. User and folders

```bash
sudo adduser --system --group --home /opt/interior interior
sudo mkdir -p /opt/interior/api /opt/interior/web /var/lib/interior/storage /var/backups/interior /etc/interior
sudo chown -R interior:interior /opt/interior /var/lib/interior /var/backups/interior
sudo chown root:interior /etc/interior && sudo chmod 750 /etc/interior
```

## 4. Database

```bash
sudo -u postgres createuser --pwprompt interior        # choose a long random password
sudo -u postgres createdb --owner interior interior_db
```

The `interior` role owns its schema and is not a superuser.

## 5. API

On your Mac, from `interior_api/`. Use `GOARCH=arm64` if `uname -m` on the server says `aarch64`:

```bash
GOOS=linux GOARCH=amd64 go build -o dist/bin/api ./cmd/api
GOOS=linux GOARCH=amd64 go build -o dist/bin/migrate ./cmd/migrate
GOOS=linux GOARCH=amd64 go build -o dist/bin/admin ./cmd/admin
cp -R migrations deploy dist/
scp -r dist/* you@server:/tmp/interior-api/
```

On the server:

```bash
sudo cp -R /tmp/interior-api/* /opt/interior/api/ && sudo chown -R interior:interior /opt/interior/api
sudo install -m 640 -o root -g interior /dev/null /etc/interior/api.env
sudo nano /etc/interior/api.env
```

`/etc/interior/api.env`:

```ini
APP_ENV=production
SERVER_HOST=127.0.0.1
SERVER_PORT=5050
DATABASE_URL=postgres://interior:<password>@127.0.0.1:5432/interior_db?sslmode=disable
ALLOWED_ORIGINS=https://app.example.com
TRUSTED_PROXIES=127.0.0.1
STORAGE_DIR=/var/lib/interior/storage
```

The API refuses to start in production with an `http://` origin, because the session cookie is
HTTPS-only.

**Run the migrations yourself before the first start**, and before starting every new
version:

```bash
cd /opt/interior/api
sudo -u interior bash -c 'set -a; . /etc/interior/api.env; set +a; ./bin/migrate up && ./bin/migrate status'
```

Never run `cmd/seed` in production; it refuses `APP_ENV=production` anyway.

## 6. Web (Next.js)

```bash
sudo -u interior git clone https://github.com/Joedee43/interior_web.git /opt/interior/web
sudo install -m 640 -o root -g interior /dev/null /etc/interior/web.env
sudo nano /etc/interior/web.env
```

`/etc/interior/web.env`:

```ini
API_INTERNAL_URL=http://127.0.0.1:5050
NEXT_PUBLIC_APP_URL=https://app.example.com
TRUSTED_CLIENT_IP_HEADER=x-real-ip
```

Build **with those values loaded**. `next.config.ts` bakes the `/v1` rewrite target, and
`NEXT_PUBLIC_*` values are fixed into the build, so a build without them points at the defaults:

```bash
cd /opt/interior/web
sudo -u interior bash -c 'set -a; . /etc/interior/web.env; set +a; npm ci && npm run build'
```

## 7. Services and Caddy

```bash
sudo cp /opt/interior/api/deploy/*.service /opt/interior/api/deploy/*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now interior-api interior-web interior-backup.timer
sudo cp /opt/interior/api/deploy/Caddyfile /etc/caddy/Caddyfile   # then set your domain in it
sudo systemctl reload caddy
```

First platform admin (after registering that account in the browser):

```bash
cd /opt/interior/api
sudo -u interior bash -c 'set -a; . /etc/interior/api.env; set +a; ./bin/admin grant-platform-admin -email you@example.com'
```

## 8. Post-deploy checklist

- [ ] `curl -sI https://app.example.com/` shows `strict-transport-security`,
      `content-security-policy` and `x-content-type-options: nosniff`.
- [ ] On the server, `curl -s http://127.0.0.1:5050/health` answers and
      `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:5050/health/database` prints
      `200`. They are unversioned, so the public `/v1` rewrite does not expose them.
- [ ] From **another machine**, `curl -m 5 http://<server-ip>:5050/health` and `:3000` both
      fail. Only Caddy is reachable.
- [ ] Sign in through the browser: the cookie is `__Host-interior_session`, `Secure`,
      `HttpOnly`, `SameSite=Lax`.
- [ ] `journalctl -u interior-api -n 20` shows your real IP in `client_ip`, not `127.0.0.1`.
      If it shows `127.0.0.1`, check `TRUSTED_PROXIES`, the Caddyfile `header_up` line and
      `TRUSTED_CLIENT_IP_HEADER`.
- [ ] Open a product with a 3D model and a room in the editor; there should be no CSP errors in
      the browser console.
- [ ] `sudo systemctl start interior-backup` then `ls -l /var/backups/interior` shows a
      `.dump` and a `.tar.gz`. `systemctl list-timers interior-backup.timer` shows the next run.
- [ ] Copy that backup **off the server** and try one restore (section 11).

## 9. Updating

1. Take a backup: `sudo systemctl start interior-backup`.
2. API: build and copy new binaries as in step 5, keeping the old one as `bin/api.prev`. Run
   `migrate up`, then `sudo systemctl restart interior-api`.
3. Web: `cd /opt/interior/web && sudo -u interior git pull`, rebuild with the env loaded (step 6),
   then `sudo systemctl restart interior-web`.
4. Re-check the parts of section 8 that the change touches.

**Rollback:** put `bin/api.prev` back and restart. For the web, `git checkout` the previous
commit, rebuild and restart. Only run `migrate down` when the migration's down step loses no
data; otherwise restore the backup taken in step 1.

## 10. Logs

```bash
journalctl -u interior-api -f      # JSON request logs with request_id and client_ip
journalctl -u interior-web -f
journalctl -u caddy -f
```

A request ID shown in the UI ("Request ID: …") can be found in the API log with
`journalctl -u interior-api | grep <request-id>`.

## 11. Restore

```bash
sudo -u postgres createdb --owner interior interior_restore
sudo -u postgres pg_restore --no-owner --role=interior -d interior_restore /var/backups/interior/interior_db_<stamp>.dump
sudo -u interior mkdir /var/lib/interior/storage-restored
sudo -u interior tar -xzf /var/backups/interior/interior_storage_<stamp>.tar.gz -C /var/lib/interior/storage-restored
```

Then point `DATABASE_URL` and `STORAGE_DIR` in `/etc/interior/api.env` at the restored copies
(or swap the names), add the new folder to `ReadWritePaths` in `interior-api.service` if it
differs, and `sudo systemctl daemon-reload && sudo systemctl restart interior-api`.

## Known limits

- **Run one API process:** rate limits live in memory (`docs/SECURITY_REVIEW.md`).
- **Large uploads through the Next.js rewrite** (3D models up to 50 MB) are not verified yet,
  because the seller UI is not built. Test them when it is.
