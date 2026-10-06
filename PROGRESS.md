# PROGRESS.md

Handoff notes for the next coding session. Read `AGENTS.md` first, then
`documentation/product.md`, then this file.

This is a handoff, not a diary. Keep it accurate and short.

## Current Status

### Saved Items — Phase A backend complete

All four core Saved Item endpoints are implemented, wired into `main.go`
through `saved_item.RegisterModule`, and covered by tests.

| Endpoint                  | Status | Notes                             |
| ------------------------- | ------ | --------------------------------- |
| `POST /saved-items`       | done   | 201, requires Bearer              |
| `GET /saved-items`        | done   | 200, paginated, `created_at DESC` |
| `GET /saved-items/:id`    | done   | 200, owner-scoped                 |
| `DELETE /saved-items/:id` | done   | 200, hard delete, owner-scoped    |

Update/edit (`PATCH` or `PUT`) is deliberately out of scope for now.

Feature package: `internal/api/saved_item/`, following the project's
vertical-slice convention.

### Authentication and ownership

- All Saved Item endpoints require a Bearer access token via `middleware.Auth`.
- `user_id` always comes from the token's `sub` claim, never from the request.
- Saved Item reads and deletes are owner-scoped by `id AND user_id`.
- Missing and foreign items both return `RESOURCE_NOT_FOUND` / 404, so the API
  does not disclose whether another user's ID exists.
- Invalid UUID path parameters return 400 `BAD_REQUEST`.

### Domain derivation

`domain` is derived server-side from the submitted URL using Go's `net/url`.
The remote page is never contacted during save.

Normalization is intentionally minimal:

1. Parse the URL and take `Hostname()`.
2. Lowercase the hostname.
3. Remove one leading `www.` if present.

No public-suffix or registrable-domain detection is performed.
`m.youtube.com` and `youtube.com` remain distinct.

### Collections

`internal/api/collection/` is implemented as a complete vertical slice.

| Endpoint                          | Status | Notes                              |
| --------------------------------- | ------ | ---------------------------------- |
| `PUT /saved-items/:id/collection` | done   | 200, owner-scoped, requires Bearer |

The endpoint accepts `{"collection_name": "..."}` and files one saved item
into one user collection, creating that collection when necessary.

Important behavior:

- Collection creation and item movement are atomic.
- Filing an item into its current collection is idempotent.
- Unknown and foreign item IDs return the same 404 response.
- The `Unsorted` system collection is resolved by `system_key`, never display name.
- System collection names are reserved.
- User collection names are trimmed and compared case-insensitively.
- Enrichment does not participate in collection movement.

Committed as `1e85c5d feat: add saved item collection flow`.

### Search

`internal/api/search/` is implemented as a complete vertical slice.

| Endpoint      | Status | Notes                              |
| ------------- | ------ | ---------------------------------- |
| `GET /search` | done   | 200, owner-scoped, requires Bearer |

Search is PostgreSQL-native and uses `pg_trgm` with `word_similarity()` as
the fuzzy fallback. No external search engine or PostgreSQL full-text search
is used for v1.

Search behavior:

- `q` is trimmed and must be 2–128 runes.
- Results are always arrays; no-match is a successful 200.
- Saved items match on `title`, `domain`, and `url`.
- Collections match on `name`.
- Results are user-scoped.
- Scores are internal and are not part of the public API contract.
- Saved items default to 20 results and cap at 50.
- Collections cap at 5.
- Cursor pagination, autocomplete, and suggestions are not implemented.

Search is complete and committed.

### SQLC generated-model cleanup

`omit_unused_structs: true` is enabled across all SQLC targets.

Each target now generates only the models required by its own queries.
Generated query code was not changed by the cleanup.

Saved Item owns its projected `saved_item.SavedItem` type rather than borrowing
an unrelated generated table model. Cross-feature models with real consumers,
such as `sessiondb.Session`, remain generated.

`AGENTS.md` documents the code-generation rule.

---

## Enrichment — synchronous per-item enrichment implemented

The enrichment foundation and synchronous application integration are complete.

The current system has three relevant layers:

1. `internal/security` — guarded outbound HTTP and SSRF protection.
2. `internal/enrichment` — reusable metadata fetching and extraction.
3. `internal/api/enrichment` — synchronous Saved Item application integration.

There is currently **no Asynq enrichment task and no background enrichment**.
Enrichment only runs when explicitly requested for one saved item.

### Enrichment schema

The current `saved_items` schema contains:

| Column              | Meaning                                           |
| ------------------- | ------------------------------------------------- |
| `title`             | optional metadata-derived title                   |
| `platform`          | optional metadata-derived content/source platform |
| `description`       | optional enriched description                     |
| `image_url`         | optional enriched preview image                   |
| `enrichment_status` | `pending`, `completed`, or `failed`               |
| `last_enriched_at`  | time of the last successful enrichment            |

`pending` means enrichment has not successfully completed yet.

`completed` means the enrichment process succeeded. It does **not** mean every
metadata field was found. A page may produce only a title, only an image, or no
usable optional metadata and still be `completed`.

`failed` means the enrichment process itself failed.

Enrichment failure never invalidates the Saved Item. The URL remains the actual
saved data regardless of enrichment status.

### Outbound fetch security

`internal/security/outbound_fetch.go` is the SSRF boundary for server-side
fetching of user-supplied URLs.

The guarded HTTP client:

- allows only `http` and `https`;
- allows only ports 80 and 443;
- validates resolved addresses at dial time;
- blocks loopback, private, link-local, multicast, unspecified, unique-local,
  metadata, and other special-purpose addresses;
- protects against DNS rebinding;
- limits redirects, timeouts, and response size;
- does not use an environment proxy;
- rejects local-only hostnames and unsafe URL forms early.

No second outbound client or duplicate SSRF implementation should be added.

### Metadata extraction

`internal/enrichment/` is independent of Fiber, SQLC, persistence, and Asynq.

It extracts metadata such as:

- title
- description
- image URL
- canonical URL
- platform
- author/site name when available

Supported sources include HTML title/meta tags, Open Graph, Twitter metadata,
canonical links, and JSON-LD.

Metadata is optional. Missing metadata is not an enrichment failure.

`platform` is derived only from metadata published by the page. It is never
inferred from the hostname or from the client's `X-Platform` header.

Relative metadata URLs are resolved against the final page URL. The extractor
does not perform additional network requests for metadata URLs.

`CanonicalURL`, `Author`, and `SiteName` are currently extraction outputs only;
they are not persisted to `saved_items`.

### Synchronous per-item endpoint

`POST /saved-items/:id/enrich` is implemented and manually verified.

Behavior:

- authenticated and owner-scoped;
- operates on exactly one Saved Item;
- resolves ownership before performing any outbound request;
- uses the existing guarded HTTP client and enrichment extractor;
- can be called repeatedly regardless of the current enrichment status;
- never modifies `url`, `domain`, or `collection_id`;
- never performs automatic collection organization.

On successful enrichment:

- `title`, `platform`, `description`, and `image_url` are written from the
  current enrichment result;
- missing fields are written as `NULL`, so stale metadata is not retained;
- `enrichment_status` becomes `completed`;
- `last_enriched_at` is updated.

On enrichment failure:

- `enrichment_status` becomes `failed`;
- previously stored metadata is preserved;
- `last_enriched_at` is unchanged.

The endpoint returns 200 for both completed and failed enrichment outcomes.
The outcome is represented by `enrichment_status`; internal failure details
are logged rather than exposed through the normal API response.

### Verification

The feature has:

- 8 service unit tests;
- 15 integration tests;
- 90.5% combined feature-package coverage;
- 95% statement coverage in `internal/enrichment`;
- 100% statement coverage for `internal/security/outbound_fetch.go`.

Manual Postman verification was performed against real external URLs:

| Case                               | Result                             |
| ---------------------------------- | ---------------------------------- |
| YouTube                            | `completed`, rich metadata         |
| Spotify                            | `completed`, platform and metadata |
| Aura                               | `completed`, partial metadata      |
| `127.0.0.1`                        | `failed`, blocked by SSRF policy   |
| `169.254.169.254`                  | `failed`, blocked by SSRF policy   |
| Public host with a non-80/443 port | `failed`, blocked by port policy   |

The real extractor has therefore been verified end-to-end in addition to the
automated tests.

---

## Worker infrastructure

The worker infrastructure exists, but background jobs are not implemented.

Current infrastructure includes:

- local Redis service in `docker-compose.dev.yml`;
- `go-redis/v9`;
- separate `WorkerConfig` / `LoadWorker`;
- `cmd/worker` binary;
- Redis connectivity check and graceful signal blocking.

`cmd/worker` currently imports nothing from `internal/api` and does not perform
enrichment or organization.

### Still not implemented

- Asynq queue configuration and task processing.
- Enqueueing enrichment work when a Saved Item is created.
- Worker-side enrichment execution and retry classification.
- Worker graceful-shutdown coordination with the API process.
- Automatic collection organization.
- Endpoint rate limiting.

Worker architecture remains a future implementation concern. Do not redesign
the current API enrichment package solely for the worker before that work
actually begins.

---

## Testing and verification status

The current tree has been verified with:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test ./... -count=1` — pass
- `sqlc generate` — pass
- `mockgen` — pass
- `swag init -g cmd/api/main.go -parseInternal` — pass

Integration tests require Docker and use the project's PostgreSQL test container.

The enrichment foundation was also separately verified with:

- `internal/security` — 100% statement coverage
- `internal/enrichment` — 95% statement coverage

The current worker binary itself has only been manually verified for Redis
connectivity/startup. It has no queue processing yet.

---

## Important Decisions

Do not change these casually. Revisit only with an explicit decision.

1. **`saved_items.platform` is the content/source platform, not the client
   platform.** It represents services such as YouTube or Spotify, not web,
   Android, or iOS.

2. **`X-Platform` is session-only.** It populates `sessions.platform` and must
   never be used by Saved Item enrichment.

3. **`platform` is metadata-derived.** It must not be inferred from the URL
   hostname, client input, or an implicit domain-to-platform mapping.

4. **`domain` is derived and normalized server-side.** Only lowercase and one
   leading `www.` are removed. Do not add public-suffix or registrable-domain
   logic without an explicit decision.

5. **Duplicate URLs are allowed.** There is no unique constraint, deduplication,
   or `url_hash`.

6. **No Saved Item update/PATCH endpoint exists yet.** Do not add one unless
   explicitly requested.

7. **Delete is a hard delete.** There is no soft-delete or `deleted_at`.

8. **Enrichment is currently synchronous and user-triggered.**
   `POST /saved-items/:id/enrich` enriches one specific item. Nothing is
   automatically enqueued or processed in the background yet.

9. **The guarded HTTP client is the only outbound fetch path for user-supplied
   URLs.** Do not add another client, transport, or SSRF implementation.

10. **Outbound ports are restricted to 80/443.** Widening this allowlist requires
    an explicit decision.

11. **`internal/enrichment` is reusable extraction logic, not persistence.**
    It has no Fiber, SQLC, Asynq, or database responsibility.

12. **Enrichment must not modify `collection_id`.** Metadata enrichment and
    automatic collection organization are separate concerns.

13. **The worker has a separate configuration boundary.** It uses
    `WorkerConfig` / `LoadWorker` and does not depend on API-only secrets.

14. **Canonical URLs are not automatically applied.** The extractor may report a
    canonical URL, but replacing the Saved Item's original URL is a separate
    product decision.

15. **`Metadata.Author` and `Metadata.SiteName` are currently extraction-only
    fields.** Adding persistence for them requires an explicit product/schema
    decision.

---

## Next Step

The synchronous Saved Item enrichment flow is complete and manually verified.

The next phase is background processing.

Current order:

1. Decide how and when saving a URL should enqueue enrichment work.
2. Implement the Asynq task and consume side.
3. Decide retry behavior using the existing enrichment failure classification.
4. Integrate worker execution with the existing Saved Item enrichment
   persistence behavior.
5. Implement automatic collection organization as a separate worker concern.

Before implementing the worker, explicitly settle:

- whether `POST /saved-items` enqueues enrichment immediately;
- worker concurrency;
- retry policy;
- whether per-user rate limiting is needed;
- how the current API-side enrichment operation should be shared with the
  worker without duplicating persistence/enrichment behavior.

Do not solve those worker architecture questions by changing the current
synchronous endpoint unless the worker implementation actually requires it.

Search pagination, autocomplete, search suggestions, collection CRUD, and Saved
Item update/edit remain outside the current scope.

---

## Resume Instructions

1. Read `AGENTS.md`, `documentation/product.md`, and this file before coding.
2. Check `git status` and `git diff` to understand the actual working tree.
3. Inspect the relevant existing feature packages before changing code.
4. Run:
   `go build ./... && go vet ./... && go test ./...`
   before assuming the tree is healthy.
5. Avoid unrelated refactors. If something looks wrong outside the current task,
   report it instead of fixing it as a side effect.
6. Never edit generated files manually. Change their source and regenerate.
7. Never apply migrations or create commits unless explicitly instructed.
8. Do not assume background enrichment exists. The current implemented enrichment
   path is synchronous and explicitly triggered for one Saved Item.
