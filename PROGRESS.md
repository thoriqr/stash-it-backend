# PROGRESS.md

Completion tracker for the backend: what is done, what is in flight, what is
left. It does not describe how anything works and it does not hold rules.

| For                                   | Read                                  |
| ------------------------------------- | ------------------------------------- |
| Product scope, principles, roadmap    | `documentation/product.md`            |
| Product behavior, feature by feature  | `documentation/product-behavior.md`   |
| Agent rules and conventions           | `AGENTS.md`                           |
| API contracts                         | `docs/swagger.json` (generated)       |
| Sessions and refresh tokens           | `documentation/auth-sessions.md`      |
| PIN email budgets                     | `documentation/pin-email-budgets.md`  |
| Dead-session cleanup                  | `documentation/session-cleanup.md`    |
| Design decisions                      | `documentation/decisions.md`          |
| Client releases and update flow       | `documentation/client-releases.md`    |
| History                               | `git log`                             |

_State as of the last recorded entry: 2026-11-14._

## Summary

The backend covers steps 1 to 3 of the roadmap in `product.md`: authentication,
saving, the Inbox, collections, search, enrichment with automatic organization,
and the client release check, and it now runs in production. What remains is
**client catch-up** (Flutter is behind several backend changes) and a
**hardening backlog**, plus the operational tasks a live deployment brings.

## Done

### Authentication and accounts

| Area                                                   | State                        | Reference                              |
| ------------------------------------------------------ | ---------------------------- | -------------------------------------- |
| Registration, manual and social, with finalization     | done                         | `internal/api/auth/registration`       |
| Registration recovery after verification              | done                         | same                                   |
| Login, manual and Google, with enumeration resistance  | done                         | `internal/api/auth/login`              |
| Account-link confirmation                              | done                         | same                                   |
| Sessions: rotation, reuse detection, listing, cleanup  | done                         | `auth-sessions.md`, `session-cleanup.md` |
| Password reset, including accounts with no password    | done, two items open        | `internal/api/auth/password_reset`     |
| Profile and password credential (`/users/me`)          | done                         | `internal/api/user`                    |

### Saved items, collections, search

| Area                                                         | State | Reference                    |
| ------------------------------------------------------------ | ----- | ---------------------------- |
| Saved items: save, Inbox, detail, delete                     | done  | `internal/api/saved_item`    |
| Saved item notes: optional note on save, add/edit, delete    | done  | `internal/api/saved_item`, migration `000031` |
| Collections: list with bounded counts, filing with intent, items listing, delete with disposition | done | `internal/api/collection` |
| Search                                                       | done  | —                            |

### Client releases

| Area                                                 | State                      | Reference              |
| ---------------------------------------------------- | -------------------------- | ---------------------- |
| Public release check (`GET /client-releases/latest`) | done, publish tooling open | `client-releases.md`   |

Release-specific details, the publish checklist, R2 layout and the client
contract live in `documentation/client-releases.md`. This file only records that
the feature exists; individual releases are not recorded here.

### Enrichment

| Area                                            | State | Reference                                          |
| ----------------------------------------------- | ----- | -------------------------------------------------- |
| On-demand enrichment (`POST /saved-items/:id/enrich`) | done | `internal/api/enrichment`, `internal/enrichment` |
| Background enrichment worker                    | done  | `cmd/worker`, `internal/worker/{queue,enrichment}` |
| Automatic organization                          | done  | `internal/worker/organization`                     |
| Guarded outbound fetching                       | done  | `internal/security`                                |
| Optional product price (amount + currency)      | done  | `internal/enrichment`, `internal/api/enrichment`, `internal/worker/enrichment` |
| YouTube metadata through oEmbed                 | done  | `internal/enrichment/youtube.go`                   |

YouTube is the one host not read as HTML. Its watch pages are served without any
Open Graph tags when the request comes from a datacenter address, and with them
when it comes from a residential one; the same User-Agent produced both, so no
header recovers the first. oEmbed answers the same question for a few kilobytes
and is not gated by source address, so `youtube.com`, `youtu.be`, `m.`, `music.`
and `youtube-nocookie.com` are read through it. Channel, playlist and search URLs
still take the HTML path. Verified against three live links from the VPS.

### Abuse protection

| Area                                                        | State                        | Reference                                  |
| ----------------------------------------------------------- | ---------------------------- | ------------------------------------------ |
| PIN email budgets (per address, global)                     | done                         | `pin-email-budgets.md`                     |
| Per-IP budgets on unauthenticated routes, refresh included  | done, thresholds unmeasured  | `auth-sessions.md`, `internal/middleware`  |
| Per-user budgets on `POST /saved-items` and `/enrich`       | done, thresholds unmeasured  | `internal/middleware`                      |
| Bounded Argon2id capacity (`PASSWORD_WORK_CONCURRENCY`)     | done                         | `internal/security`                        |

### Observability

| Area                                                    | State       | Reference                                      |
| ------------------------------------------------------- | ----------- | ---------------------------------------------- |
| Sentry HTTP 500 and panic reporting, optional DSN       | done        | `internal/observability`, `internal/httpx`     |
| Origin stack trace for `Internal`/`InternalWith` errors | done        | `internal/apperror/error.go`                   |
| Worker failure reporting                                | not started | `cmd/worker`, `internal/worker`                |

The reporter is verified on production, not only in development: both a deliberate
500 and a deliberate panic produced events with a `sentry_event_id` in the log and
a matching issue in the project, environment `production`.

`internal/api/debug` mounts `POST /debug/sentry/error` and
`POST /debug/sentry/panic` **only** while `SENTRY_DEBUG=true`. They exist because a
5xx is otherwise close to unreachable on demand: every bad input is a 4xx and
every dependency outage is a 503, which is deliberately not reported. With the
flag false the routes are not registered at all, so a normal deployment has 109
handlers and no debug surface. Turn the flag on, verify, turn it off.

Migrations recorded as applied to the development database: `000025`, `000026`.
The state of `000027` to `000029` is not recorded here.

## In flight

Nothing uncommitted. Run `git status` first; this list may be stale.

Recently landed and deployed: registration recovery (`3fc5405`), the Google
Desktop OAuth client (`013020c`), YouTube enrichment through oEmbed (`8789c62`),
and the gated Sentry verification routes (`e5180d9`).

## Remaining

### Deployment

Live since 2026-11-13: Tencent Cloud Lighthouse, Singapore, Ubuntu 24.04,
2 vCPU / 2 GB / 40 GB, behind Caddy on `api.stashit.thoriqr.my.id`. PostgreSQL 18
is Neon in Singapore; Redis and Caddy run on the VPS; binaries are built on the
development machine with `CGO_ENABLED=0` and copied over `scp`. Schema is at
`000031`. The first end-to-end registration delivered a PIN through Brevo, and
enrichment plus automatic organization ran in the worker.

Settled:

- [x] Choose the hosting platform.
- [x] Choose the email provider: Brevo REST API v3
      (`internal/email/brevo_sender.go`).
- [x] Configure Brevo: sending domain `stashit.thoriqr.my.id` authenticated,
      transactional API key created, the VPS address added to the key's IP
      allowlist. An address outside that list makes the API refuse to start.
- [x] Set `CLIENT_IP_SOURCE=proxy` with `TRUSTED_PROXIES=127.0.0.1,::1` behind
      Caddy; confirmed in the startup line.
- [x] Set `SENTRY_DSN` and `SENTRY_ENVIRONMENT=production`, and verify reporting
      on production with the gated debug routes.
- [x] Confirm Redis is reachable with `appendonly yes` and
      `maxmemory-policy noeviction`.
- [x] `APP_ENV` is supplied by the systemd unit, not by `.env.production`
      (`deployment.md`, Configuration).

Open:

- [ ] Configure an external scheduler to call
      `POST /internal/maintenance/sessions/cleanup` daily
      (`session-cleanup.md`). `SESSION_CLEANUP_SECRET` is set, so the route is
      live, but nothing calls it and dead sessions are never removed.
- [ ] Size `PASSWORD_WORK_CONCURRENCY` and the Argon2id parameters to the real
      instance; both are still the conservative defaults.
- [ ] Replace every rate-limit threshold estimate with a value based on real
      traffic.
- [ ] Keep `SENTRY_RELEASE` in step with the deployed binary. It is set from a
      commit that predates the current build.
- [ ] Watch memory under real load. `stash-it-api` idles around 8 MB and the
      worker around 3 MB, so 2 GB is ample today; Redis AOF growth and
      `WORKER_CONCURRENCY` are the levers if that changes.
- [ ] Re-enrich items saved while YouTube pages were being served without
      metadata, through `POST /saved-items/:id/enrich`. There is no backfill; it
      is per item and on demand.

### Flutter client

Backend changes already live that the client does not yet handle:

- [ ] Registration: decode `verification_status` (default `pending`), persist the
      continuation token right after `/verify`, branch to finalization on
      `verified`.
- [ ] Map `409 REGISTRATION_ALREADY_PENDING` from recovery to the normal
      `POST /auth/register/manual` flow.
- [ ] Treat `VERIFICATION_NOT_PENDING` on a just-returned id as "already
      verified; use the stored continuation or retry shortly".
- [ ] **Breaking:** send `collection_intent` on `PUT /saved-items/:id/collection`;
      map `COLLECTION_NAME_ALREADY_EXISTS`; retire `ApiFailureCollectionNameReserved`.
- [ ] **Breaking:** account link — `POST .../confirm` needs `{"id_token": "..."}`;
      the details response carries `masked_user_email` and no longer
      `email_snapshot` or `user_display_name`. A 401 on confirm means re-obtain
      the Google token.

The two breaking changes must ship with, or before, the backend.

- [ ] Client update check: consume `GET /client-releases/latest`, verify the
      downloaded `sha256`, and add the update dialog. See
      `client-releases.md` for the contract and the publish flow.

### Backend backlog

- [ ] Capture worker failures in Sentry: panics, exhausted retries, and
      integrity/programming failures. HTTP 500 and panic are already covered.
- [ ] Password reset has no per-address budget on PIN issuance, and the per-IP
      budget does not bound guessing across several codes for one address. A
      threshold was deliberately not invented.
- [ ] Password reset continuations have no unique index on
      `password_reset_continuations.pending_password_reset_id`; adding one needs a
      migration and a decision on whether the constraint is wanted.
- [ ] Add `-race` to CI (`.github/workflows/test.yml`).
- [ ] Re-run the manual-login rate-limit tests repeatedly; only single runs exist.
- [ ] Decide on a one-off backfill of items enriched before automatic
      organization existed.
- [ ] Decide whether `POST /saved-items` should keep failing closed on a Redis
      outage.
- [ ] Decide whether duplicate enrichment of one URL needs a per-item or per-URL
      budget.
- [ ] Convert `GET /auth/sessions` from offset to cursor pagination, if wanted.
- [ ] Delete expired and confirmed account-link confirmations; the table only
      grows.
- [ ] Reduce what `user_display_name` reveals on the unauthenticated account-link
      route; it is returned in full today.
- [ ] Cover `ACCOUNT_LINK_SESSION_FAILED` end to end (needs fault injection).
- [ ] Index `auth_identities.user_id`. Deferred on purpose while the table is small;
      it is scanned on each profile read.
- [ ] Align password length validation: registration and reset use `min=8`; login
      and `PUT /users/me/password` use `min=8,max=128`.
- [ ] Idle-window clock mix in the refresh path (`auth-sessions.md`, Open items).

### Next on the roadmap

Reminder and return loop, then product metadata enrichment (steps 4 and 5 of the
roadmap in `product.md`). Not started.

### Documentation housekeeping

- [ ] The rate-limiting entries in `decisions.md` on PIN budgets, where the IP budget
      sits and `Retry-After` overlap with `pin-email-budgets.md`. Decide whether they
      stay or defer to that document; the `Retry-After` entry also covers the
      fixed-window limiter.

## Local environment

`go test -race` needs cgo and a C compiler. On the Windows machine, check
`where.exe gcc` resolves and `go env CGO_ENABLED` is `1`. A shell started before
the `Path` change can miss it; for that command only:

```powershell
$env:Path = 'C:\msys64\ucrt64\bin;' + $env:Path
```
