# Deployment

## Topology

```text
Internet
  |
  | 443/80
  v
Caddy (TLS, reverse proxy)
  |
  | 127.0.0.1:8080
  v
stash-it-api  --->  Neon PostgreSQL (TLS)
     |          \->  Redis 127.0.0.1:6379
     |
     +-- outbound: Brevo, Sentry, Google, fetched pages

stash-it-worker ---> Neon PostgreSQL, Redis
```

| Component | Location | Notes |
| --- | --- | --- |
| `stash-it-api` | VPS | HTTP on `127.0.0.1:8080`. |
| `stash-it-worker` | VPS | No HTTP surface. |
| Redis | VPS | `127.0.0.1:6379`, AOF enabled. Rate limits and queue. |
| Caddy | VPS | Public 80/443, TLS, reverse proxy. |
| PostgreSQL | Neon | PostgreSQL 18, TLS, region nearest the VPS. |

VPS baseline: Ubuntu 24.04, 2 vCPU / 2 GB RAM, 40 GB disk, static IPv4, and a
domain pointing at the VPS. Public ports are 22 (administration), 80 and 443.
Ports 6379 and 8080 stay on loopback. There is no inbound PostgreSQL port.
Outbound TCP 5432 must reach Neon; outbound 443 reaches Brevo, Sentry, Google and
fetched pages.

Runtime layout:

```text
/opt/stash-it/
  .env.production
  bin/stash-it-api
  bin/stash-it-worker
  migrations/
```

VPS provisioning—OS packages, SSH, firewall, DNS, Redis and Caddy
installation—is outside this document.

## Configuration

`.env.production` lives at `/opt/stash-it/.env.production`, mode 600, owned by
the service user. `config.Load()` reads `.env.<APP_ENV>` from the process working
directory, and the systemd units set `WorkingDirectory=/opt/stash-it`.

**`APP_ENV` must be supplied by the systemd unit, not by this file.** The loader
reads `APP_ENV` from the process environment to decide which file to load, so a
value inside `.env.production` is read only after that decision has already been
made. With no `APP_ENV` in the environment the loader defaults to `development`,
looks for `.env.development`, finds nothing, and starts with every variable
missing — the first symptom is `Config error: DATABASE_URL is required` even
though the file is correct. Both units below therefore set
`Environment=APP_ENV=production`. An `APP_ENV` line inside the file is harmless
but does nothing, so the sample below omits it to avoid implying otherwise.

```env
DATABASE_URL=postgres://<NEON_USER>:<NEON_PASSWORD>@<NEON_DIRECT_HOST>/<NEON_DB>?sslmode=require
REDIS_URL=redis://:<STRONG_REDIS_PASSWORD>@127.0.0.1:6379/0

VERIFICATION_CODE_SECRET=<RANDOM_SECRET_1>
ACCESS_TOKEN_SECRET=<RANDOM_SECRET_2>
GOOGLE_CLIENT_ID=<PRODUCTION_GOOGLE_CLIENT_ID>
GOOGLE_DESKTOP_CLIENT_ID=<OPTIONAL_DESKTOP_CLIENT_ID>

SESSION_CLEANUP_SECRET=<RANDOM_SECRET_3>

EMAIL_PROVIDER=brevo
BREVO_API_KEY=xkeysib-<BREVO_API_KEY>
EMAIL_FROM_ADDRESS=noreply@example.com
EMAIL_FROM_NAME=Stash It
EMAIL_TIMEOUT_SECONDS=10

SENTRY_DSN=<SENTRY_DSN_OR_EMPTY>
SENTRY_ENVIRONMENT=production
SENTRY_RELEASE=<GIT_COMMIT_SHORT_SHA>
SENTRY_SAMPLE_RATE=1.0
SENTRY_TRACES_SAMPLE_RATE=0
SENTRY_DEBUG=false

CLIENT_IP_SOURCE=proxy
TRUSTED_PROXIES=127.0.0.1,::1
TRUSTED_PROXY_HEADER=X-Forwarded-For

PASSWORD_WORK_CONCURRENCY=1
PASSWORD_WORK_WAIT_MS=1000

PIN_EMAIL_BUDGET_PER_EMAIL_MAX=10
PIN_EMAIL_BUDGET_GLOBAL_MAX=250
PIN_EMAIL_BUDGET_WINDOW_SECONDS=86400

WORKER_CONCURRENCY=5
```

Generate secrets with:

```sh
openssl rand -base64 48
```

`VERIFICATION_CODE_SECRET` and `ACCESS_TOKEN_SECRET` must be different.

Configuration constraints:

- **Neon must be PostgreSQL 18.** The migrations use the built-in `uuidv7()`
  column default. PostgreSQL 17 and earlier fail with
  `function uuidv7() does not exist`.
- **`APP_ENV` comes from the systemd unit**, not from `.env.production`. See
  Configuration for why the file cannot select itself.
- Choose the Neon region nearest the VPS. Free-plan computes suspend after about
  five minutes of inactivity and wake in a few hundred milliseconds; the API
  startup `Ping` wakes the compute.
- Neon supports the `pg_trgm` and `btree_gin` extensions used by migration
  `000023`.
- Use the **direct** Neon host for migrations. The pooled `-pooler` host is for
  application connections when direct connection limits are a problem.
- `sslmode=require` is required for Neon.
- `CLIENT_IP_SOURCE=peer` is the alternative when there is no reverse proxy. In
  that mode, `TRUSTED_PROXIES` and `TRUSTED_PROXY_HEADER` must be absent.
- `TRUSTED_PROXIES` entries must be canonical: `127.0.0.1`, `::1`, or a CIDR such
  as `172.18.0.0/16`.
- URL-encode passwords containing `@`, `:`, `/` or `#` in `DATABASE_URL` and
  `REDIS_URL`.
- `PIN_EMAIL_BUDGET_GLOBAL_MAX` must stay below the Brevo plan's daily quota.
- `PASSWORD_WORK_CONCURRENCY=1` is the safe value on 2 GB. One Argon2id
  derivation uses about 64 MiB.
- `WORKER_CONCURRENCY=5` is a starting point; lower it under memory or CPU
  pressure.
- `SENTRY_DSN` may be empty. Reporting is disabled and the logger remains the
  only error sink.
- `SENTRY_RELEASE` identifies the deployed build. Use
  `git rev-parse --short HEAD` (for example `25fe18e`). Update it when a new
  binary is deployed, not on every commit. It is optional, but it is what makes
  Sentry's release filters and regression tracking useful.

Redis configuration (`/etc/redis/redis.conf`):

```conf
bind 127.0.0.1 ::1
requirepass <STRONG_REDIS_PASSWORD>
appendonly yes
maxmemory-policy noeviction
```

`appendonly yes` is required for queued Asynq tasks. `noeviction` prevents queue
entries and rate-limit state from being evicted.

Caddyfile (`/etc/caddy/Caddyfile`):

```text
api.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
}
```

Caddy terminates TLS and forwards to the API. It sets `X-Forwarded-For`; the
application validates it against `TRUSTED_PROXIES`. Nginx is a valid
alternative, with Certbot and explicit `proxy_set_header` directives.

## Build and release

Build on the development machine with `CGO_ENABLED=0`; the application has no cgo
dependency.

```powershell
New-Item -ItemType Directory -Force build | Out-Null
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"   # arm64 for an ARM VPS
go build -trimpath -ldflags="-s -w" -o build/stash-it-api ./cmd/api
go build -trimpath -ldflags="-s -w" -o build/stash-it-worker ./cmd/worker
```

Release artifacts:

```text
build/stash-it-api
build/stash-it-worker
migrations/
```

Copy them to `/opt/stash-it/bin/` and `/opt/stash-it/migrations/`, owned by the
service user with mode 0755 for binaries and 0600 for `.env.production`.

## Database

The application never applies migrations at runtime.

Use the direct Neon URL:

```sh
export DATABASE_URL='postgres://<NEON_USER>:<NEON_PASSWORD>@<NEON_DIRECT_HOST>/<NEON_DB>?sslmode=require'
migrate -path /opt/stash-it/migrations -database "$DATABASE_URL" up
```

```sh
migrate -path /opt/stash-it/migrations -database "$DATABASE_URL" version
```

Rules:

- Use the direct host, not `-pooler`, for migrations.
- Never edit an applied migration; add a new one.
- Do not run `migrate ... down` without a recovery branch or `pg_dump` and a
  rollback plan.
- Create a Neon branch or a `pg_dump` before later migrations.

### Backups

Neon handles point-in-time restore and branching; the history window depends on
the plan. Use a Neon branch as the primary recovery mechanism.

For a logical backup outside Neon:

```sh
pg_dump -Fc "$DATABASE_URL" > stash-it-$(date +%F).dump
```

PostgreSQL 18 is required, so use `pg_dump` 18. Ubuntu 24.04's default client is
16; install `postgresql-client-18` from the official PGDG repository before
using `pg_dump`. Verify with `pg_dump --version`; if the command still resolves
to 16, use `/usr/lib/postgresql/18/bin/pg_dump`.

Test the restore into a Neon branch or a local PostgreSQL 18 instance.

Redis holds rate-limit counters and queued Asynq tasks. With `appendonly yes`,
the AOF files are under `/var/lib/redis/appendonlydir/`. Losing Redis loses
queued work; rate-limit counters can be lost safely.

## Runtime

Install the units as `/etc/systemd/system/stash-it-api.service` and
`/etc/systemd/system/stash-it-worker.service`:

```ini
[Unit]
Description=Stash It API
After=network-online.target redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=stashit
Group=stashit
# Selects .env.production. It cannot come from the file itself; see Configuration.
Environment=APP_ENV=production
WorkingDirectory=/opt/stash-it
ExecStart=/opt/stash-it/bin/stash-it-api
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/stash-it

[Install]
WantedBy=multi-user.target
```

```ini
[Unit]
Description=Stash It Worker
After=network-online.target redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=stashit
Group=stashit
# Selects .env.production. It cannot come from the file itself; see Configuration.
Environment=APP_ENV=production
WorkingDirectory=/opt/stash-it
ExecStart=/opt/stash-it/bin/stash-it-worker
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/stash-it

[Install]
WantedBy=multi-user.target
```

PostgreSQL is remote, so there is no `After=postgresql.service`.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now stash-it-api stash-it-worker
sudo systemctl status stash-it-api stash-it-worker
sudo journalctl -u stash-it-api -n 80 --no-pager
sudo journalctl -u stash-it-worker -n 80 --no-pager
```

Startup fails if `.env.production` is missing from the working directory or Neon
cannot be reached. The API startup line records the provider and client-address
configuration.

Public ports: 22, 80, 443. Internal: 6379, 8080. No inbound 5432. Outbound 5432
to Neon and outbound 443 to providers. The API listens on `:8080`; the firewall
keeps it private, and Caddy reaches it over loopback.

## Operations

Deploy or update:

1. Build new binaries.
2. Create a Neon branch or `pg_dump` if the release includes a migration.
3. Copy the binaries to `/opt/stash-it/bin/` as `.new` files.
4. Stop `stash-it-api` and `stash-it-worker`.
5. Replace the binaries and fix ownership and permissions.
6. Apply migrations through the direct Neon URL.
7. Start the services, then verify the API and logs.
8. Keep the previous binary until the new one has served traffic successfully.

Migrations are forward-only in normal operation. A rollback must account for
schema changes and be planned before it is needed. A simple deployment has a few
seconds of downtime while the services restart.

External services:

- **Brevo** — authenticate the sending domain, verify `EMAIL_FROM_ADDRESS`,
  create a transactional API key, and authorize the VPS IP. Keep
  `PIN_EMAIL_BUDGET_GLOBAL_MAX` below the plan's daily quota. Brevo binds a key
  to an IP allowlist once one is configured, so the API refuses to start with
  `brevo rejected the api key or the caller's ip address` until the VPS address
  is added at `app.brevo.com/security/authorised_ips`. The startup check is
  deliberate: a key that cannot send is a deployment that cannot register
  anybody.
- **Sentry** — optional. Set `SENTRY_DSN` and `SENTRY_ENVIRONMENT`; an empty DSN
  disables reporting.

Operational references:

- API contract and route behavior: `docs/swagger.json`.
- Session cleanup schedule and its route contract: `documentation/session-cleanup.md`.
- Email budgets and provider behavior: `documentation/pin-email-budgets.md`.
- Architecture and invariants: `AGENTS.md`.

## Troubleshooting

| Symptom | Likely cause | Check |
| --- | --- | --- |
| Caddy returns 502 | API is not running or on the wrong port | `systemctl status stash-it-api`, `journalctl -u stash-it-api` |
| Migration fails with `function uuidv7() does not exist` | Neon project is not PostgreSQL 18 | Neon project settings |
| `pg_dump` fails with a server version mismatch | Client is older than PostgreSQL 18 | `pg_dump --version`; install `postgresql-client-18` |
| API exits with a config error | `.env.production` is missing from `WorkingDirectory` | `ls -l /opt/stash-it/.env.production` |
| `Config error: DATABASE_URL is required` although the file is correct | `APP_ENV` is not set in the unit, so the loader looked for `.env.development` | `systemctl cat stash-it-api \| grep Environment` |
| `email sender unavailable at startup`, Brevo 401 `unrecognised IP address` | The VPS address is not in the key's IP allowlist | `app.brevo.com/security/authorised_ips`, then restart the API |
| Startup cannot reach PostgreSQL | Wrong Neon URL, SSL mode, region or network | `psql "$DATABASE_URL" -c 'select 1;'`, Neon dashboard |
| 503s during a Redis outage | Redis is down or unreachable | `systemctl status redis-server`, `redis-cli -a '<PASSWORD>' ping` |
| First request after idle is slower | Neon scale-to-zero wake-up | Neon compute activity |
| Neon reports too many connections | API/worker pools exceed the plan limit | Neon dashboard; use pooled URL or lower `pool_max_conns` |
| Brevo startup check fails | Wrong API key or VPS IP not authorized | Brevo API keys and Authorized IPs |
| Every client shares one rate-limit budget | Proxy mode or trusted proxies are wrong | startup `client_ip_source`, `trusted_proxies` |
| Worker idle | Worker not running, wrong Redis or empty queue | worker service and logs |
| OOM kill | Memory pressure or Argon2 concurrency too high | `journalctl -k`, `systemd-cgtop` |
| TLS certificate not issued | DNS not propagated or a proxying CDN interferes | DNS resolution, Caddy logs |

## Out of scope

VPS provisioning (OS packages, SSH, firewall, DNS, Redis and Caddy installation),
managed Redis or Redis high availability, multiple API instances, load
balancing, zero-downtime deployment, Docker/Kubernetes, infrastructure as code,
CDN/WAF, Neon plan-specific tuning, email deliverability tuning beyond Brevo
domain authentication, API route documentation, and Sentry alert routing.
