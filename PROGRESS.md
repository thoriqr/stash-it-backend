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
| History                               | `git log`                             |

_State as of the last recorded entry: 2026-10-27._

## Summary

The backend covers steps 1 to 3 of the roadmap in `product.md`: authentication,
saving, the Inbox, collections, search, and enrichment with automatic
organization. What remains is **deployment** (hosting platform and email provider
are not chosen), **client catch-up** (Flutter is behind several backend changes),
and a **hardening backlog**.

## Done

### Authentication and accounts

| Area                                                   | State                        | Reference                              |
| ------------------------------------------------------ | ---------------------------- | -------------------------------------- |
| Registration, manual and social, with finalization     | done                         | `internal/api/auth/registration`       |
| Registration recovery after verification              | done, **uncommitted**        | same                                   |
| Login, manual and Google, with enumeration resistance  | done                         | `internal/api/auth/login`              |
| Account-link confirmation                              | done                         | same                                   |
| Sessions: rotation, reuse detection, listing, cleanup  | done                         | `auth-sessions.md`, `session-cleanup.md` |
| Password reset, including accounts with no password    | done, two items open        | `internal/api/auth/password_reset`     |
| Profile and password credential (`/users/me`)          | done                         | `internal/api/user`                    |

### Saved items, collections, search

| Area                                                         | State | Reference                    |
| ------------------------------------------------------------ | ----- | ---------------------------- |
| Saved items: save, Inbox, detail, delete                     | done  | `internal/api/saved_item`    |
| Collections: list with bounded counts, filing with intent, items listing, delete with disposition | done | `internal/api/collection` |
| Search                                                       | done  | —                            |

### Enrichment

| Area                                            | State | Reference                                          |
| ----------------------------------------------- | ----- | -------------------------------------------------- |
| On-demand enrichment (`POST /saved-items/:id/enrich`) | done | `internal/api/enrichment`, `internal/enrichment` |
| Background enrichment worker                    | done  | `cmd/worker`, `internal/worker/{queue,enrichment}` |
| Automatic organization                          | done  | `internal/worker/organization`                     |
| Guarded outbound fetching                       | done  | `internal/security`                                |
| Optional product price (amount + currency)      | done  | `internal/enrichment`, `internal/api/enrichment`, `internal/worker/enrichment` |

### Abuse protection

| Area                                                        | State                        | Reference                                  |
| ----------------------------------------------------------- | ---------------------------- | ------------------------------------------ |
| PIN email budgets (per address, global)                     | done                         | `pin-email-budgets.md`                     |
| Per-IP budgets on unauthenticated routes, refresh included  | done, thresholds unmeasured  | `auth-sessions.md`, `internal/middleware`  |
| Per-user budgets on `POST /saved-items` and `/enrich`       | done, thresholds unmeasured  | `internal/middleware`                      |
| Bounded Argon2id capacity (`PASSWORD_WORK_CONCURRENCY`)     | done                         | `internal/security`                        |

Migrations recorded as applied to the development database: `000025`, `000026`.
The state of `000027` to `000029` is not recorded here.

## In flight

**Uncommitted work.** Run `git status` first; this list may be incomplete.

- Registration recovery: `POST /auth/register/recovery/manual` eligibility,
  `verification_status` on `POST /auth/register/manual`, migration
  `000029_add_registration_recovery_purpose`. Verified 2026-10-27.
- Login timing-test fix in `internal/api/auth/login/` (`types.go`, `service.go`,
  `dummy_verification_seam_test.go`, `login_rate_limit_service_test.go`).
  Verified, including `-race` twice.

## Remaining

### Blocked on a deployment decision

Hosting platform and email provider are not selected. Nothing here is a code gap;
each needs information only a chosen environment can supply.

- [ ] Choose the hosting platform.
- [ ] Verify forwarding-header behaviour on a deployed instance, then set
      `CLIENT_IP_SOURCE` and `TRUSTED_PROXIES` from what was observed. Left at
      the default behind a proxy, every client shares one counter.
- [ ] Choose the email provider (Brevo is a candidate). Until then non-development
      builds use `email.NewUnconfiguredSender()`, which errors on send.
- [ ] Confirm Redis availability and TLS. Limiters fail closed, so an outage
      blocks login, Google authentication, PIN issuance and refresh.
- [ ] Size `PASSWORD_WORK_CONCURRENCY` and the Argon2id parameters to the real
      instance.
- [ ] Decide Cloud Run specifics for the worker.
- [ ] Set `SESSION_CLEANUP_SECRET` and configure an external scheduler to call
      `POST /internal/maintenance/sessions/cleanup` daily (`session-cleanup.md`).
      Nothing in the process schedules it, so until then dead sessions and their
      refresh tokens are never removed.
- [ ] Replace every rate-limit threshold estimate with a value based on real
      traffic.

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

### Backend backlog

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
