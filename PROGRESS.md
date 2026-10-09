# PROGRESS.md

Handoff notes for the next coding session. Read `AGENTS.md` first, then
`documentation/product.md`, then this file.

This is a handoff, not a diary. Keep it accurate and short.

## Current Status

### Saved Item lifecycle — end to end

| Stage                                    | Status |
| ---------------------------------------- | ------ |
| Save (`POST /saved-items`)                | done   |
| Background enrichment after save          | done   |
| Background automatic organization         | done   |
| On-demand enrichment (`POST /saved-items/:id/enrich`) | done |
| Collection filing (`PUT /saved-items/:id/collection`)  | done |
| Saved item ownership constraint (migration 000025) | applied |

Migration 000025 is applied to the development database, and
`internal/database/baseline/schema.sql` reflects it, so integration tests exercise
the same ownership constraint production does.

The full path — save, background enrichment, and the automatic organization the
enrichment hands off to — was also verified end to end against the running stack
with a manual request walkthrough. Automated tests cover the same flow.

### Saved Items — Phase A backend complete

All four core Saved Item endpoints are implemented, wired into `main.go`
through `saved_item.RegisterModule`, and covered by tests.

| Endpoint                  | Status | Notes                             |
| ------------------------- | ------ | --------------------------------- |
| `POST /saved-items`       | done   | 201, requires Bearer              |
| `GET /saved-items/:id`    | done   | 200, owner-scoped                 |
| `DELETE /saved-items/:id` | done   | 200, hard delete, owner-scoped, reports collection state |

Update/edit (`PATCH` or `PUT`) is deliberately out of scope for now.

`GET /saved-items` was removed. Every saved item belongs to exactly one collection, so
the inbox is the Unsorted collection seen through `GET /collections/:id/saved-items`
rather than a second, independent list. Keeping both would mean two ways to ask the
same question, and the standalone one reported fewer fields: it carried no
`enrichment_status`, `last_enriched_at`, `description` or `image_url`.

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

| Endpoint                              | Status | Notes                                                     |
| ------------------------------------- | ------ | --------------------------------------------------------- |
| `PUT /saved-items/:id/collection`     | done   | 200, owner-scoped, requires Bearer                        |
| `GET /collections`                    | done   | 200, owner-scoped, cursor-paginated, requires Bearer      |
| `GET /collections/:id/saved-items`    | done   | 200, owner-scoped, cursor-paginated, requires Bearer      |
| `DELETE /collections/:id`             | done   | 200, owner-scoped, requires Bearer                        |

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

### Collection deletion

`DELETE /collections/:id` is implemented. It is a separate explicit operation from
deleting a saved item, and the two never invoke each other.

Request body, a stable shape in both cases:

```json
{ "saved_items_action": "delete", "target_collection_id": null }
{ "saved_items_action": "move",   "target_collection_id": "<uuid>" }
```

Behavior:

- `saved_items_action` is required and is `delete` or `move`. Nothing is defaulted,
  because the two actions have opposite consequences and one of them destroys saved
  items.
- `target_collection_id` must be null for `delete` and a collection id for `move`.
  Both fields always exist and never change meaning.
- `delete` removes the collection's saved items along with the collection. `move`
  files them into the target collection first, then removes the collection.
- The target must exist, belong to the authenticated user, and differ from the
  source. A target that does not exist is an error; there is no fallback to any
  collection, including Unsorted.
- **Unsorted is a valid move target and is named by its id** like any other
  collection. There is no `move_to_unsorted` flag and no special mode for it.
- Any collection may be deleted regardless of `type`. Unsorted is the only exception,
  identified by `system_key = 'unsorted'` and never by display name or type.
- The whole operation is one repository-owned transaction: load the source, refuse
  Unsorted, resolve the target when moving, dispose of the children, delete the
  collection, commit. Anything failing rolls all of it back.
- Nothing is taken `FOR UPDATE`, which keeps the lock order compatible with the move
  endpoint and lets the foreign key decide emptiness.
- The response is a plain `{"data": null, "message": "collection deleted
  successfully"}`. The request already states the disposition, so the response adds
  nothing by listing what was done.

Error contract: `COLLECTION_NOT_FOUND`, `UNSORTED_COLLECTION_PROTECTED`,
`INVALID_COLLECTION_DELETE_ACTION`, `INVALID_COLLECTION_DELETE_TARGET`,
`COLLECTION_DELETE_TARGET_NOT_FOUND`, `COLLECTION_NOT_EMPTY`.

### Collection listing — `GET /collections`

The user's collections, one page at a time, with Unsorted pinned to the front.

Request:

```http
GET /collections?sort=newest&limit=20&cursor=<opaque>
```

| Param | Values | Default | Notes |
| --- | --- | --- | --- |
| `sort` | `newest`, `oldest`, `name` | `newest` | Unknown value is a 400, never guessed |
| `limit` | 1–50 | 20 | Above the maximum is `VALIDATION_ERROR`; `limit=0` is read as absent |
| `cursor` | opaque token | none | Resume where the previous page ended; must match the requested `sort` |

Behavior:

- **Unsorted is always first, under every sort**, and appears on no later page. It
  is pinned by a rank over `system_key = 'unsorted'` in SQL, never by display name
  and never by `type`. Pinning it by position rather than filtering means all three
  sorts inherit it from one rule.
- **Collections automatic organization created are listed**, unfiltered. A
  collection belongs to one user either way.
- **`name` sorts by `lower(btrim(name))`** — the same expression
  `collections_user_name_unique` is built on, so the ordering and the uniqueness
  constraint cannot disagree about what two names being the same means.
- **Every order is broken by `id`.** `created_at` defaults to `NOW()`, which is
  constant within a transaction, so equal timestamps are real: migration 000022
  inserts every user's Unsorted collection in one statement. Without a unique
  tie-break a page boundary can skip or repeat a row.
- **Six named queries**, one per (sort × resume point). sqlc derives parameter types
  per statement, so a runtime-selected `ORDER BY` would either collapse the types or
  force a query framework this feature has no use for.
- **The resumed queries exclude Unsorted with `IS DISTINCT FROM`.** That is
  load-bearing twice: it removes the pinned row, and it is NULL-safe, so user
  collections whose `system_key` is NULL are not dropped with it. A plain `<>` would
  return system collections only.
- **Row comparison in the keyset predicate, with explicit casts.** The casts are
  load-bearing too: without them sqlc types every row-comparison parameter from the
  row's leftmost element, which would give the `id` parameter a `timestamptz` type.

Response:

```json
{ "data": { "collections": [ { "id": "…", "name": "Unsorted", "type": "system",
    "system_key": "unsorted", "created_at": "…", "updated_at": "…" } ] },
  "message": "collections retrieved successfully",
  "meta": { "cursor": { "next_cursor": null, "has_more": false } } }
```

- `system_key` is included so a client recognizes Unsorted by identity rather than by
  display name, and can know a collection may not be deleted before offering to
  delete it.
- **`next_cursor` has no `omitempty`**, so it serializes as an explicit `null` on the
  final page. A nil pointer tagged `omitempty` would drop the key entirely, and a
  missing key and a null are two different things to a client decoding it.
- **No `total` or `total_pages`.** `has_more` comes from one extra fetched row rather
  than a count, which is the work keyset pagination exists to avoid.
- An empty page serializes `collections` as `[]`, never `null`.
- **`meta` is declared on the API response type as a documentation-only mirror.**
  `httpx.OKWithMeta` writes the real envelope, so a struct that omits `meta` returned a
  three-key body while the generated schema described two — and the `@Description`
  above, which talks about `meta.cursor.next_cursor`, then contradicted the schema
  under it. Still valid OpenAPI, so no linter caught it. Fixed here and on
  `GET /collections/:id/saved-items`; both now share one `CursorPageMeta`. See the
  "Documented response shape" test below.

### A collection's saved items — `GET /collections/:id/saved-items`

Where the saved items of one collection are seen. It replaces the removed standalone
listing, because every saved item belongs to exactly one collection.

Request:

```http
GET /collections/:id/saved-items?limit=20&cursor=<opaque>
```

Response:

```json
{
  "data": {
    "collection": { "id": "01a0f359-093b-737a-963a-80f7ca6768ed", "name": "YouTube" },
    "saved_items": [
      {
        "id": "01a0f359-093b-737a-963a-80f7ca6768ed",
        "url": "https://example.com/video",
        "domain": "example.com",
        "platform": null,
        "title": null,
        "description": null,
        "image_url": null,
        "collection_id": "01a0f359-093b-737a-963a-80f7ca6768ed",
        "enrichment_status": "pending",
        "last_enriched_at": null,
        "created_at": "2026-10-09T00:00:00Z",
        "updated_at": "2026-10-09T00:00:00Z"
      }
    ]
  },
  "message": "saved items retrieved successfully",
  "meta": {
    "cursor": { "next_cursor": null, "has_more": false }
  }
}
```

Behavior:

- **The collection is resolved and its ownership proved before any item is read.** A
  collection that does not exist and one owned by somebody else produce the same
  `COLLECTION_NOT_FOUND`, so the endpoint never discloses whether an id exists.
- **`data.collection` carries `id` and `name`, and nothing else.** The caller arrived
  knowing the id; the name is the one thing the path cannot tell it. It is reused from
  the ownership lookup rather than read again — no second query was added — and it is
  reported on every response, **including an empty page**, because an empty collection
  is still a named collection and omitting the field would make "nothing in it"
  indistinguishable from "no match". `type` and `system_key` are deliberately absent:
  they answer questions for `GET /collections`, not this one.
- **Unsorted is an ordinary collection here.** Nothing is special-cased for it: the
  inbox is this endpoint pointed at the Unsorted id, and it arrives through the same
  `collection` object as anything else.
- **Each item is reported in full**: `id`, `url`, `domain`, `platform`, `title`,
  `description`, `image_url`, `collection_id`, `enrichment_status`,
  `last_enriched_at`, `created_at`, `updated_at`. The previous standalone listing
  reported seven of these, so a caller could not tell a missing title from metadata
  that had not arrived.
- **`collection_id` stays on every item** and always equals `data.collection.id`. The
  redundancy is deliberate: it makes an item self-describing once it is carried out of
  this response.
- **A null title, a null description, a null image url and a `failed` enrichment are
  all ordinary states and all listed.** `enrichment_status` is `NOT NULL` with a
  default; the metadata columns are nullable and stay so after a page exposes none.
- **Ordering is fixed at `created_at DESC, id DESC`.** No sort parameter. A collection
  is something a person curated, so the order they chose when filing is the one they
  see.
- **`limit` defaults to 20 and is capped at 50.** A limit of 51+ or below 1 is
  `VALIDATION_ERROR`; a non-numeric limit is `BAD_REQUEST`; `limit=0` is treated as
  absent. The `max=50` tag fires before the service's own clamp, so an out-of-range
  limit is rejected rather than silently resized.
- **Listing never triggers enrichment and never writes.** Enrichment is scheduled by
  saving and by `POST /saved-items/:id/enrich`; browsing is not a reason to fetch.
- **Cursor-paginated**, with the same `meta.cursor` shape as `GET /collections` and an
  explicit `next_cursor: null` on the final page. A cursor is a position, so it
  survives its anchor item being deleted; a cursor past the end returns an empty page.
- **Two SQL statements, not six.** One ordering and no pinned row means one
  first-page statement and one after-position statement.
- **Its cursor payload has no `Sort` and no `Group`.** Both exist on the collection
  payload only because that listing pins Unsorted and offers three orders; fields that
  can never vary here would only add unreachable validation branches. There is a test
  asserting the payload carries neither.
- **The timestamp is parsed and validated in the service**, so a token that decodes but
  holds an unusable value is `INVALID_CURSOR` rather than a server fault, and no query
  runs to discover it.

Errors: `400 BAD_REQUEST` / `400 VALIDATION_ERROR` / `400 INVALID_CURSOR`,
`401 INVALID_AUTHORIZATION_HEADER` / `INVALID_ACCESS_TOKEN` / `ACCESS_TOKEN_EXPIRED`,
`404 COLLECTION_NOT_FOUND`, `500 INTERNAL_SERVER_ERROR`.

### Saved item deletion reports collection state

`DELETE /saved-items/:id` deletes the saved item and **never** touches its
collection. The response reports what the delete left behind:

```json
{
  "data": {
    "collection_id": "01a0f359-093b-737a-963a-80f7ca6768ed",
    "collection_empty": true,
    "collection_deletable": true
  },
  "message": "saved item deleted successfully"
}
```

- `collection_id` comes from the DELETE's own `RETURNING` clause, so it is the
  deleted row's value and a concurrent move cannot make it disagree. No locking
  read is needed to pin it.
- `collection_empty` is counted after the delete, scoped by `collection_id AND
  user_id`.
- `collection_deletable` is `collection_empty` and the collection not being
  Unsorted. It exists because Unsorted is where every save lands, so an empty inbox
  is the most common "yes" here and a caller acting on `collection_empty` alone
  would be turned away on almost every inbox deletion.
- All three are advisory. The collection is untouched, and deleting it is a separate
  request that validates the current state again.
- There is deliberately no transaction around the delete and its two reads: nothing
  is acted on, and under READ COMMITTED each statement snapshots separately anyway.
- The previously `null` data body is replaced, which is a deliberate API change.

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

## Enrichment — foundation and both application paths complete

The enrichment foundation and both application integrations are complete. The
background worker and automatic organization have their own section below.

The current system has these relevant layers:

1. `internal/security` — guarded outbound HTTP and SSRF protection.
2. `internal/enrichment` — reusable metadata fetching and extraction, shared by
   every caller.
3. `internal/api/enrichment` — synchronous Saved Item application integration.
4. `internal/worker/enrichment` — background enrichment worker, driven by an Asynq
   task.
5. `internal/worker/organization` — background automatic organization, driven by an
   Asynq task the enrichment worker produces.

Enrichment runs either on request for one saved item or in the background after a
save. The two paths write the same rows.

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
- 95.4% statement coverage in `internal/enrichment`;
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

## Background enrichment worker — implemented

Background Asynq enrichment is implemented and verified. The worker enriches a
Saved Item after it has been committed, and the synchronous endpoint remains the
per-item on-demand path.

### What exists

Infrastructure:

- local Redis service in `docker-compose.dev.yml`;
- `go-redis/v9` and `hibiken/asynq`;
- separate `WorkerConfig` / `LoadWorker` — the worker reads only `DATABASE_URL`,
  `REDIS_URL`, `APP_ENV`, and the optional `WORKER_CONCURRENCY`, which is range
  checked when it is set;
- `cmd/worker` binary with a Redis connectivity check and graceful signal
  blocking;
- `REDIS_URL` is now required by the API as well as the worker, since a task can
  only be processed by a worker pointed at the same Redis.

Packages:

- `internal/worker/queue` — the queue contract. Holds `TaskTypeEnrichSavedItem`,
  `QueueEnrichment`, the task timeout and retry budget, the payload type, the
  retry delay function, and the `Producer` the API enqueues through. The package
  imports no Fiber, nothing from `internal/api`, and no persistence.
- `internal/worker/enrichment` — the worker's own handler, service, repository and
  narrow projections, plus its own sqlc target.
- `internal/api/saved_item/enqueuer.go` — the one-method interface the save flow
  depends on. It may be nil, which is how "this process schedules nothing" is
  expressed.

`cmd/worker` imports nothing from `internal/api`.

### Save → enqueue flow

1. The row is written. `domain` is derived locally and nothing contacts the
   remote source.
2. Only once that write has committed is enrichment queued, with the saved item's
   id as the whole payload.

Enqueueing before the write returned could put a task in Redis for an id the
database never accepted. Enqueueing after it makes the worst case the opposite
and smaller: a committed row whose task was never queued, which is a valid Saved
Item that still says `pending` and can be enriched on demand.

A failure to enqueue is logged and swallowed. The save succeeded, and reporting
it as a failed request would report a failure for something that happened.

### Task and consumer

The payload carries the saved item's id and nothing else. The worker reads the
URL from the row, so a queued copy can never disagree with what was stored.

The worker reuses `internal/enrichment` and `internal/security`, constructing the
same guarded outbound client the API uses. There is no second SSRF
implementation and no second outbound client.

### Queue and task naming

Both areas name their task type `<area>:<thing>` after their queue, so a task and
the queue it belongs to can be read off one string:

| Queue         | Task type                |
| ------------- | ------------------------ |
| `enrichment`  | `enrichment:saved_item`  |
| `organization`| `organization:saved_item`|

Both are dedicated queues rather than Asynq's `default`, and both are declared
explicitly in the worker's `Queues` map. Organization is not on the enrichment
queue because its work is pure database: a burst of saves would otherwise delay
filing behind its own page fetches.

### Persistence semantics

Identical to the synchronous endpoint, statement for statement:

| Outcome                | `enrichment_status` | Metadata | `last_enriched_at` |
| ---------------------- | ------------------- | -------- | ------------------ |
| extraction succeeded   | `completed`         | written  | updated            |
| extraction failed      | `failed`            | preserved| unchanged          |
| page exposed nothing   | `completed`         | all NULL | updated            |

`collection_id` and `domain` are never written by enrichment. There is no
`processing` or `retrying` value and no intermediate state: how many more attempts
the queue will make is execution mechanics, not the state of the Saved Item.

A task for a deleted item is discarded rather than retried, and no outbound
request is made for a row that is gone.

### Retry classification

The classification is read from `enrichment.Kind`, so the worker does not define a
second opinion about what a page error means. It maps that classification onto one
queue decision.

Retryable:

- DNS, connection, TLS, timeouts, too many redirects, policy refusals;
- a body that could not be read;
- `429` and `5xx`, which describe the origin at that moment;
- any database error, which arrives unclassified and defaults to retryable.

Permanent, and skipped immediately:

- a terminal client status such as `401`, `403`, `404`, `410`;
- a non-HTML content type;
- an HTML document that could not be parsed.

Retries are bounded by the queue's own budget: one attempt plus
`EnrichmentMaxRetry` retries, with an exponentially increasing, jittered, capped
delay. Once the budget is exhausted Asynq archives the task and the item already
carries the durable `failed` record.

### Concurrency and shutdown

`WORKER_CONCURRENCY` remains the runtime configuration source, with an explicit
range enforced by the loader:

| Setting                  | Value                              |
| ------------------------ | ---------------------------------- |
| `DefaultWorkerConcurrency` | `5`                              |
| `MinWorkerConcurrency`     | `1`                              |
| `MaxWorkerConcurrency`     | `20`                             |

An unset value resolves to the default. A value that is present but outside the
range — zero, negative, above the maximum, or not a number — fails configuration
and stops the worker from starting, rather than being silently replaced. A
deployment that believes it asked for something it did not get is worse than one
that refuses to start.

Enrichment is network-bound, so a small number of concurrent attempts overlaps
mostly waiting, while a large number only multiplies concurrent outbound requests
and database connections against origins that are already slow.

On SIGTERM the worker stops taking new tasks and waits for in-flight ones, bounded
by `shutdownTimeout`. Anything still queued stays in Redis for whichever worker
runs next.

The startup log records the Redis address and database, never the full URL, which
may carry credentials.

### Automatic organization

Background organization is implemented. It is a separate task type on its own
queue, produced by the enrichment worker and consumed by the worker binary.

Flow, with no scan or sweep anywhere in it:

```
save -> enrichment task -> enrichment worker
     -> completed enrichment with a platform
     -> one organization task (saved_item_id + user_id)
     -> organization worker
     -> re-read the item, locked
     -> file it only if it is STILL in Unsorted
```

A failed enrichment records `failed` and queues nothing. A completed enrichment
with no platform queues nothing. There is no persisted organization state, so a
task that was never created is never created later: an item left in `Unsorted`
stays there until a user asks for it to be enriched again.

Package `internal/worker/organization` owns its own handler, service, repository,
projection and sqlc target, mirroring the enrichment worker. It imports nothing
from `internal/api` and nothing from the enrichment worker.

The worker decides at execution time, against a locked row:

| Condition                              | Outcome                            |
| -------------------------------------- | ---------------------------------- |
| item does not exist                     | discarded, like the enrichment worker |
| item belongs to another user            | refused and archived                |
| `enrichment_status` is not `completed`  | no-op                              |
| `platform` is NULL                      | no-op                              |
| item is no longer in `Unsorted`         | no-op, never moved back            |
| a collection already matches the platform | reused, never renamed             |
| no collection matches                    | created as `type = 'system'`       |

The "still in Unsorted" rule is the one that matters: an item the user filed into
their own collection while the task waited is left exactly where they put it.

Ownership is enforced by migration 000025's composite foreign key, and the worker's
statements are scoped to agree with it. A created collection belongs to that one
user; `system` says who created it, not who owns it.

`system_key` is the platform's own normalized identity — `lower(btrim(platform))` —
so it agrees with the `collections_user_name_unique` comparison and the two indexes
can never disagree. It is a derived identity, not a curated registry, which is the
honest consequence of `platform` being free text published by the page.

### Still not implemented

- Endpoint rate limiting.
- A possible one-off backfill of items enriched before automatic organization
  existed, which are still in `Unsorted` with a platform. This is an open product
  decision rather than a gap in the mechanism, and it would be a separate
  operation — see decision 13a for why it is not part of the normal worker flow.
- Cloud Run deployment specifics. The worker is a standalone binary with no HTTP
  surface, and is intended to be deployable as-is.

What is deliberately **not** implemented, and should not be read as missing:

- **No organization reconciliation, scanner or sweep.** Organization is event-driven
  from a successful enrichment that produced a platform, and there is no
  alternative producer. With no persisted organization status there is nothing to
  sweep *for*, and a query for "still in `Unsorted` with a platform" would also
  match exactly the state a user may have chosen on purpose. If an event is never
  produced, the system does not later discover it — deliberately, not by accident.
- **No reconciliation for items left `pending` by a failed enqueue.** Also a
  deliberate trade-off, recorded as decision 16.

### Verification

The worker has:

- `internal/config` — the concurrency policy: the default, both range boundaries,
  accepted values inside the range, and rejection of zero, negative,
  above-maximum and non-numeric values, all through `LoadWorker`;
- `internal/worker/enrichment` — the enrichment -> organization handoff: a completed
  enrichment with a platform schedules one task, and a failed extraction, a
  complete extraction with no platform, a failed write and an unreachable queue do
  not;
- `internal/worker/organization` — the eligibility guards, the system key derived
  from a platform, and that organization writes no enrichment state;
- `internal/worker/queue` — payload round trip, payload contents, backoff bounds,
  and the relationship between the task timeout and the outbound fetch ceiling;
- `internal/worker/enrichment` — handler classification for retryable, permanent,
  unclassified and malformed-payload tasks, and service behavior for completion,
  empty results, failures, missing items and write errors;
- `internal/integration` — the whole path against real Postgres and real Redis:
  a save enqueues, the worker consumes, the row is written; a deleted item is
  discarded; a permanent failure is archived on the first attempt; a transient
  failure is retried and then completes; an exhausted budget is archived; the
  producer's task type, queue, timeout and retry budget round-trip for both task
  types; the enrichment -> organization handoff runs end to end; and organization
  creates, reuses, is skipped for, and refuses to undo a user's filing for, with
  concurrent tasks producing exactly one collection per platform per user.

Deletion is verified against real Postgres, asserting database state rather than
status codes alone:

- `internal/api/saved_item` — the delete's reported collection, count scoping, the
  empty and not-empty cases, Unsorted against a system collection and against a user
  collection with no key, not-found leaving the collection unread, and error
  propagation from all three repository calls.
- `internal/integration` — reported collection state for user, system and Unsorted
  collections; the reported id being the deleted item's own after a real move;
  concurrent deletes of the last two items and of the same item, asserting invariants
  rather than an interleaving.
- `internal/api/collection` — the request rules, both dispositions, Unsorted as an
  ordinary target by id, and error pass-through.
- `internal/integration` — both dispositions against real rows; Unsorted refused
  while another system collection of the same user is deleted; the foreign key
  proven to be the guard, asserted by `ConstraintName`; ownership isolation and
  byte-identical not-found responses; and concurrency asserting that no saved item
  ever references a missing collection.

Queue-facing tests read Asynq's own accounting through an `Inspector` rather than
by counting Redis keys, so they cannot pass by reading nothing.

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
The queue-facing integration tests additionally start a Redis testcontainer,
lazily and only when one is needed.

The enrichment foundation was also separately verified with:

- `internal/security` — 100% statement coverage
- `internal/enrichment` — 95.4% statement coverage

The worker's unit-level packages are verified by unit tests plus the integration
suite above, which exercises their real behavior against Postgres and Redis.
Statement coverage figures for `internal/worker/*` are not recorded here, since
their meaningful behavior is in the integration path rather than in isolated
calls.

The worker binary's Redis connectivity and startup have been verified, and the
full background path is covered by the integration tests above. Those tests replace
only the extraction layer, for the same reason the synchronous enrichment tests do
(the guarded outbound client correctly refuses loopback); the real extractor has
been verified against live external origins by manual walkthrough instead.

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

8. **`POST /saved-items` enqueues enrichment, and the synchronous endpoint
   remains available.** A save commits its row first and then queues the item's
   id. `POST /saved-items/:id/enrich` still enriches one specific item on
   demand, and enrichment is repeatable from either path.

   Both paths produce the same row for the same page, and neither changes
   `collection_id` or `domain`.

9. **Enrichment has exactly three persisted states: `pending`, `completed`,
   `failed`.** There is deliberately no `processing` or `retrying` value. How
   many more attempts the queue will make is execution mechanics, not the state
   of the Saved Item, so it is never written. Adding a status value requires an
   explicit decision and a migration.

10. **The guarded HTTP client is the only outbound fetch path for user-supplied
    URLs.** Do not add another client, transport, or SSRF implementation. The
    worker constructs the same one and shares no other outbound path.

11. **Outbound ports are restricted to 80/443.** Widening this allowlist requires
    an explicit decision.

12. **`internal/enrichment` is reusable extraction logic, not persistence.**
    It has no Fiber, SQLC, Asynq, or database responsibility. It is shared by
    the synchronous endpoint and the worker, which is why neither path can
    diverge on what a page error means.

13. **Enrichment must not modify `collection_id`.** Metadata enrichment and
    automatic collection organization are separate concerns. Organization writes
    `collection_id` and nothing else.

13a. **Automatic organization is event-driven and never overrides a user.** A task
    exists only because a successful enrichment produced a platform, and it may only
    move an item that is still in `Unsorted`. Do not add a scan, a sweep, a periodic
    query, or a persisted organization status to find items the event missed.

13b. **`collections.type = 'system'` describes who created a collection, not who
    owns it.** A system collection belongs to exactly one user through
    `collections.user_id`, and migration 000025's composite foreign key is what makes
    an item's collection and its owner have to agree.

14. **The worker has a separate configuration boundary.** It uses
    `WorkerConfig` / `LoadWorker` and does not depend on API-only secrets. It also
    imports nothing from `internal/api`.

15. **`FailureKind` separates a page that cannot be used from an origin that is
    not answering.** Terminal client statuses, non-HTML content and unparseable
    documents are permanent; network failures, unreadable bodies, `429` and `5xx`
    are retryable within the bounded budget. Changing which group a failure falls
    into changes retry behavior, so treat it as a product decision.

16. **A save succeeds even when enqueueing fails.** A failed enqueue leaves the
    item `pending`, and there is no reconciliation pass for it either. This is a
    deliberate trade-off: the Saved Item is valid product data regardless of its
    metadata, and the user can always ask on demand.

17. **Canonical URLs are not automatically applied.** The extractor may report a
    canonical URL, but replacing the Saved Item's original URL is a separate
    product decision.

18. **`Metadata.Author` and `Metadata.SiteName` are currently extraction-only
    fields.** Adding persistence for them requires an explicit product/schema
    decision.

19. **Deleting a saved item never deletes its collection.** The delete reports the
    collection, whether it is now empty, and whether the user may delete it, and
    those are advisory values for the caller. Removing a collection is a separate
    endpoint the caller invokes deliberately. An empty collection is a valid state,
    so removing one as a side effect would take an action nobody asked for.

19a. **Deleting a collection is an explicit destructive operation whose caller
    states the disposition of its saved items.** `saved_items_action` is `delete` or
    `move` with no default, because the two have opposite consequences and one
    destroys content. Nothing is inferred on the caller's behalf, and a missing
    target is an error rather than a substitution.

19b. **Unsorted is identified by `system_key` and is the only protected
    collection.** Collection type is not the criterion: `type = 'system'` says who
    created a collection, not what it is, so an automatically created collection is
    deletable like one the user named. Unsorted as a *move target* is ordinary and is
    named by its id; there is no `move_to_unsorted` mode and no fallback to it.

19c. **`saved_items_collection_id_fkey` remains the final guard on collection
    deletion.** The children are disposed of first because `collection_id` is
    `NOT NULL`, and the FK decides emptiness at the final DELETE rather than an
    application pre-check. A violation rolls the transaction back and is reported as
    `COLLECTION_NOT_EMPTY`. Never change it to `ON DELETE CASCADE`.

19d. **The move-all-items behavior exists only as part of collection deletion.**
    `MoveSavedItemsToCollection` has no endpoint of its own. A general batch-move
    feature is a separate product decision and is deliberately not implemented here.

19e. **A cursor is a position, not a row.** It records where a page ended, and the
    next query resumes from there without ever reading the row it came from. So a
    cursor keeps working after that row is deleted or renamed, and a cursor pointing
    past the end returns an empty page rather than an error. This is the property that
    separates this from offset pagination, where a deleted row shifts everything after
    it and the client silently receives a duplicate.

19f. **`GET /collections` uses cursor pagination; the existing offset list endpoints
    are unchanged.** `GET /collections/:id/saved-items` is cursor-paginated too.
    `GET /auth/sessions` keeps `page`/`limit` and `meta.pagination`. `httpx.Meta`
    carries both, with `cursor` omitted entirely where unset, so no existing response
    changes. Converting the session endpoint is a separate piece of work and needs its
    own decision.

19g. **There is no standalone saved-item listing.** Every saved item belongs to
    exactly one collection, so an inbox is the Unsorted collection seen through
    `GET /collections/:id/saved-items`. Two ways to ask the same question would be two
    answers to keep in step, and the standalone one was also the poorer answer: it
    carried no `enrichment_status`, `last_enriched_at`, `description` or `image_url`,
    so a caller could not tell a page with no title from one that had not been read.

19h. **A collection listing names its collection.** `GET /collections/:id/saved-items`
    reports `data.collection` with `id` and `name`, reused from the ownership lookup
    that call already had to make — no extra query. It is reported on every response
    including an empty page, because "nothing in it" and "no such collection" are
    different things and the name is what tells them apart. `type` and `system_key` are
    left out: they answer questions for `GET /collections`, and a client that reached
    this endpoint has already named the collection it wants. Unsorted goes through the
    same type as everything else.

19i. **A response schema that omits `meta` is a documentation bug, and it is guarded.**
    Both cursor endpoints described `meta.cursor` in prose while their generated 200
    schema showed a two-key envelope, because `httpx.OKWithMeta` builds `meta` and the
    API response structs did not declare it. Nothing at runtime could notice, and the
    result is still valid OpenAPI, so no linter did either. Both now declare a
    documentation-only mirror sharing one `CursorPageMeta`, and
    `TestCollectionAPI_ListSavedItems_DocumentedShape` walks `docs/swagger.json` and a
    real body side by side at every level, failing if either gains or loses a key.

---

## Next Step

Enrichment and automatic organization are both implemented and verified: the
synchronous per-item endpoint, the background enrichment worker, and the
background organization worker it hands off to. Their architecture is settled —
event-driven, no sweep, no persisted organization state — and is recorded in
decisions 8, 9, 13, 13a and 13b rather than reopened here.

Current order:

1. Endpoint rate limiting.
2. Decide whether to run a one-off backfill of items enriched before automatic
   organization existed. It is a product decision, not a mechanism gap, and it
   would be a separate deliberate operation rather than anything the worker does
   on its own.
3. Cloud Run deployment specifics for the worker, when a target is chosen.

Search pagination, autocomplete, search suggestions, and Saved Item update/edit
remain outside the current scope. Collection *deletion* now exists, but only as the
explicit `DELETE /collections/:id`; collection listing and creation as standalone
operations, a general batch move of saved items, and collection rename remain
outside the current scope.

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
8. Both enrichment paths exist and both work. A save commits its row and queues
   background enrichment, and a completed enrichment that produced a platform
   hands off to background automatic organization. On-demand enrichment of one
   Saved Item still exists as well, and both write the same rows. Read the
   background worker and automatic organization sections above before changing
   either; neither is an open question.
