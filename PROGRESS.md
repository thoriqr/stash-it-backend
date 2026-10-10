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
| `POST /saved-items`       | done   | 201, requires Bearer, minimal response |
| `GET /saved-items/:id`    | done   | 200, owner-scoped, complete item + collection |
| `DELETE /saved-items/:id` | done   | 200, hard delete, owner-scoped, reports collection state |

Update/edit (`PATCH` or `PUT`) is deliberately out of scope for now.

`GET /saved-items` was removed. Every saved item belongs to exactly one collection, so
the inbox is the Unsorted collection seen through `GET /collections/:id/saved-items`
rather than a second, independent list. Keeping both would mean two ways to ask the
same question, and the standalone one reported fewer fields: it carried no
`enrichment_status`, `last_enriched_at`, `description` or `image_url`.

Feature package: `internal/api/saved_item/`, following the project's
vertical-slice convention.

### Save response is minimal, detail response is complete

The two read paths answer different questions and deliberately report different
things.

`POST /saved-items` returns only what a save can meaningfully know:

```json
{ "data": { "saved_item": { "id": "…", "url": "https://example.com/a",
      "collection_id": "…", "enrichment_status": "pending" } },
  "message": "saved item created successfully" }
```

A save commits before enrichment has run, so `domain` is the only metadata column
carrying a value and it was derived locally from the submitted URL, not read off the
page. Reporting `title`, `platform`, `description`, `image_url` or `last_enriched_at`
there would be a set of nulls presented as though they had been looked up, which reads
as "this page has nothing" rather than "this page has not been read yet".

`enrichment_status` is included because the `INSERT` returns it. It costs no extra
query, and reporting the stored value beats reporting a constant the code assumes: it
is what the column's default actually wrote.

`GET /saved-items/:id` returns the complete item plus the collection it is filed in:

```json
{ "data": { "saved_item": { "id": "…", "url": "https://example.com/a",
      "domain": "example.com", "platform": null, "title": null,
      "description": null, "image_url": null, "collection_id": "…",
      "enrichment_status": "pending", "last_enriched_at": null,
      "created_at": "…", "updated_at": "…" },
    "collection": { "id": "…", "name": "Wishlist" } },
  "message": "saved item retrieved successfully" }
```

- **`data.saved_item` matches the listing shape** in `GET /collections/:id/saved-items`
  field for field, for the same reason: without `enrichment_status` a null title is
  ambiguous between "this page has no title" and "this page has not been read yet".
- **`data.collection` carries `id` and `name` only.** `type` and `system_key` belong
  to `GET /collections`: they describe how a collection came to be, which plays no
  part in showing one item. Unsorted arrives through the same object as anything else.
- **`saved_item.collection_id` always equals `collection.id`.** The redundancy is
  deliberate: it makes the item self-describing once it is carried out of the response.
- **No extra query for the collection.** `GetSavedItemByIDForUser` joins
  `collections` in the same statement, owner-scoped on both sides, so naming the
  collection costs nothing beyond what the read already did. The join is `INNER`
  because `collection_id` is `NOT NULL` and references an existing row.
- **A pending or failed item is returned in full.** `title`, `description`,
  `image_url`, `platform` and `last_enriched_at` are reported as JSON `null` with the
  key present, never omitted.
- **Neither DTO was weakened.** `CreatedSavedItemResponse` and
  `SavedItemDetailResponse` are separate types; the detail response did not have to
  give up fields so the create response could be short.

`SavedItem`, the feature's projection, is now the complete row including
`collection_id` and the enrichment columns, because `Get` has to report all of it.
Create and Get are its only readers, so a narrower projection would only mean a
second near-identical type. The collection slice keeps its own separate projection,
shaped for paging over a collection rather than describing one item.

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
- A saved item result reports `id`, `title`, `url`, `domain`, `image_url`,
  `enrichment_status`, a nested `collection` of `id` and `name`, and `created_at`.
  The collection is joined in by the search query itself, so naming it costs no
  extra query per result.
- A collection result reports `id` and `name` only; `type` and `system_key` are
  reported by `GET /collections` rather than by a search result.
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

- Rate limiting for endpoints other than the two PIN endpoints. Per-email and
  per-IP PIN limiting is implemented and verified — see "Registration rate
  limiting" below. Login, password reset, saved items, collections, search and
  enrichment are deliberately unlimited for now.
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

## Registration rate limiting — implemented

Both endpoints that send a message are limited:
`POST /auth/register/verification/:verification_id/pin` and
`.../resend`. Two independent budgets apply, and neither replaces the other.

| Budget     | Subject                        | Limit            | Namespace | Enforced by |
| ---------- | ------------------------------ | ---------------- | --------- | ----------- |
| per-email  | normalized email address       | 5 per 1 hour     | `pin-email` | `registration` service |
| per-IP     | resolved client IP address     | 20 per 10 minutes| `pin-ip`   | route middleware |

Both are Redis-backed fixed-window counters. Both are shared across the two
endpoints, so switching from `/pin` to `/resend` is not a way to double an
allowance, and a new `verification_id` for the same address is not a fresh
per-email budget.

### Ordering within a request

```
IP middleware  ->  handler parses the path  ->  service resolves the verification
  ->  cooldown  ->  email limiter  ->  issue  ->  send
```

- **The per-IP budget is enforced in front of the handler**, not in the service.
  A service never sees a request that failed to parse its UUID or named a
  verification that does not exist, so a limit enforced there would count only
  well-formed requests. Repeated invalid requests are counted, because the
  cheapest request an attacker can make is one that cannot succeed.
- **The cooldown is checked before the email budget**, so a caller already being
  told to wait is not also charged for asking.
- **A request the IP budget refuses never reaches the email budget.** Verified
  directly: an exhausted IP budget leaves the per-email counter untouched.

The two registration endpoints that do not send a message
(`POST /auth/register/manual`, `.../verify`, `.../finalize/*`) are deliberately
not client-limited.

### Responses

| Situation                | Status | Code                              | `Retry-After` |
| ------------------------ | ------ | --------------------------------- | ------------- |
| email budget spent       | 429    | `PIN_RATE_LIMIT_EXCEEDED`         | yes           |
| client budget spent      | 429    | `IP_RATE_LIMIT_EXCEEDED`          | yes           |
| Redis unreachable        | 503    | `PIN_RATE_LIMIT_UNAVAILABLE` / `IP_RATE_LIMIT_UNAVAILABLE` | **no** |
| client address unresolvable | 503 | `CLIENT_IP_UNAVAILABLE`           | **no**        |

**A limiter that cannot answer is a refusal, not permission.** Allowing on failure
would let anyone remove the ceiling by causing one. A 503 names no recovery time
because there is none to name; fabricating a `Retry-After` there would tell a
client to come back at a moment nobody chose.

`Retry-After` is written by `httpx.NewErrorHandler`, the single place a response
is written, gated on status 429. A value carried on an error object and never
emitted would leave every client guessing when its own window reopens.

### `Retry-After` semantics

- **Read from the counter's remaining TTL, not from the configured window.**
  Reporting the configured window would tell a client arriving late in a window
  to wait out time that had already passed.
- **Returned by the same Lua script that increments the counter.** Reading the
  count and the TTL in two round trips would let the window expire between them,
  so a caller could be told "over budget" from a count that was real and a TTL
  that no longer was.
- **Rounded up** to whole seconds, because the header's contract is "not before"
  and rounding down names a moment the window is still closed.
- **Never negative.** Redis reports a negative PTTL for a key with no expiry and
  for a key that has gone; neither is a length of time to hand to a client.
- **A refused request does not extend the window.** The TTL is set only when the
  counter is created, so a client hammering a closed window cannot hold it open.
  Verified over three consecutive refusals.

### Client address resolution

The deployment platform is **not decided**, so nothing is assumed about it. The
resolution source is declared explicitly rather than inferred.

| `CLIENT_IP_SOURCE` | Meaning | Forwarded headers |
| ------------------ | ------- | ----------------- |
| `peer` (default)   | Clients connect directly; the socket address is the client's | never read, whatever the request contains |
| `proxy`            | A verified proxy fronts the application | read only from `TRUSTED_PROXIES`, in `TRUSTED_PROXY_HEADER` |

Configuration is refused at startup when it could not be trusted:

- `proxy` with no `TRUSTED_PROXIES` — a forwarded address believed from any
  source, which is the same as believing it from the client;
- `TRUSTED_PROXIES` or `TRUSTED_PROXY_HEADER` set while the source is `peer` —
  a contradiction whose intended half is a guess;
- an entry that is not an IP or CIDR;
- an entry that trusts everything: `0.0.0.0/0`, `::/0`, any range broader than
  `/8`, or the unspecified address;
- **a bare address that is not in canonical form** — long-form or uppercase IPv6,
  or IPv4 written in its IPv6-mapped form.

The last one exists because of how the framework matches. It keeps each
configured address as the text it was given and compares it to the incoming peer
rendered canonically, so `2001:0db8::1`, `2001:DB8::1` and `::ffff:10.0.0.1`
would configure an allowlist that silently matches nothing. The peer is then never
recognised as a proxy, the forwarded address is never read, and **every client
through it resolves to the proxy's own address and shares one counter** — the
exact failure this configuration exists to prevent, with no error anywhere.
Ranges are exempt: they are matched numerically, not by text.

All four framework settings are **derived** from that one declaration by
`config.Config.ProxySettings()`. There is no combination in which a header is
read without a verified allowlist beside it.

`EnableIPValidation` is turned on whenever a header is read, and this is not
optional. Fiber v3.5.0's `extractIPFromHeader` returns the header's **raw bytes**
when validation is off, so a chain of `203.0.113.7, 198.51.100.4` would become
the client's identity — a different counter for every chain the client chose to
send. With validation on, Fiber walks the chain right-to-left skipping trusted
proxies, so a client that prepends a forged entry does not move its own address.

Addresses are canonicalized through `ratelimit.NormalizeIP` before being used as
a subject: ports and IPv6 zones are stripped, and IPv4-mapped IPv6 collapses to
IPv4. Without that, one client behind one NAT gets a separate budget for each
spelling of its address.

An address that cannot be resolved is refused with `CLIENT_IP_UNAVAILABLE`
rather than given an invented subject, because every unresolved request would
otherwise share one counter.

### Verification

`internal/ratelimit` — IP canonicalization and its equivalence classes; policy
validation; `Retry-After` rounding including a sweep proving it is never
negative; fail-closed on an unreachable Redis; key derivation carrying neither
the subject nor its namespace collisions.

`internal/middleware` — 429 with `Retry-After`; 503 with no `Retry-After`;
`Retry-After` rounding across whole, fractional, zero and negative waits;
six different forwarding headers (`X-Forwarded-For`, `X-Real-IP`, `Forwarded`,
`Client-IP`, `CF-Connecting-IP`, `True-Client-IP`) all ignored with no proxy
configured; a configured proxy believed; distinct clients given distinct
identities; a client prepending to the chain not moving its own address; the
configured policy applied; an unparseable forwarded value refused rather than
used.

`internal/config` — the default peer source; proxy mode requiring an allowlist;
every rejection above, including non-canonical bare addresses; canonical bare
addresses and ranges of any spelling still accepted; a copied allowlist that
cannot be mutated through the returned settings; `Load` refusing to start on an
untrustworthy configuration.

`internal/integration` — against real Postgres and real Redis: TTL on first
increment; the TTL not extending on later calls; window expiry restoring the
budget; concurrent requests admitting exactly `Max` (both namespaces, and
repeated across rounds); concurrent HTTP requests against one address admitting
exactly 20; one address exhausted not affecting a neighbour; a verification not
locked by another address's budget; the email and IP budgets refusing
independently; `Retry-After` present, integral, within the window and
non-increasing across refusals; a forced TTL-less counter repaired rather than
reported as a negative wait; Redis failure producing 503 with nothing issued and
no header; spoofed headers landing on the peer's address; registration creation
consulting no limiter at all.

**The concurrency tests were proven to detect the race they claim to.** With the
Lua script replaced by a naive `GET`/`SET` read-modify-write, all three
concurrency tests fail — including the new per-IP one — and pass again with the
script restored.

Nothing here was weakened to make a test pass.

---

## Login rate limiting and enumeration resistance — implemented

`POST /auth/login` is limited, and the Google routes are not. Two independent
budgets apply to manual login, and neither replaces the other.

| Budget     | Subject                    | Limit             | Namespace     | Enforced by        |
| ---------- | -------------------------- | ----------------- | ------------- | ------------------ |
| per-IP     | resolved client IP address | 30 per 10 minutes | `login-ip`    | route middleware   |
| per-email  | normalized email address   | 10 per 15 minutes | `login-email` | `login` service    |

Both are Redis-backed fixed-window counters from the same `internal/ratelimit`
package the PIN budgets use, sharing one Redis, one Lua script and one connection
pool. An address spread across a thousand source IPs is never near its per-IP
ceiling, which is exactly the shape of a credential-stuffing list; the per-email
budget is what still holds in that case.

The per-IP ceiling sits deliberately above the equivalent PIN ceiling. People
share addresses — a household, an office, a carrier's CGNAT — and do so most
heavily at predictable moments, when everyone signs in at once. A login false
positive is worse than a PIN false positive: the user cannot authenticate at
all, and there is no second way in.

### Ordering within a request

```
IP middleware  ->  normalize the address  ->  charge the address budget
  ->  look the account up  ->  verify the password  ->  create the session
```

- **The address budget is charged before the lookup, not counted after the
  failure.** A check-then-count arrangement would let a burst of concurrent
  requests all read "within budget", all start an Argon2id derivation, and only
  be refused afterwards: the limit would bound how many requests are *ultimately
  refused* while bounding none of the work that costs anything. Charging first
  bounds the work.
- **A charge is handed back when the work it guarded succeeded.** The release
  happens before the session is created. Without it, every successful sign-in
  would spend the failure budget of the address it authenticated, and a
  legitimate user who signed in a few times could not sign in again.
- **A request the IP budget refuses never reaches the address budget.** A flood
  must cost a locked-out account nothing: what was spent was a network budget,
  not the account's.
- **A release that fails does not fail the login.** The budget was enforced when
  it was charged, which is the step that bounds the work and the step that fails
  closed. An unreleased charge makes the address marginally stricter for the rest
  of its window and then expires with it. Refusing the login instead would tell a
  real user their password was wrong while a session row was on its way into the
  database, and they would retry and create more.

### Responses

| Situation                   | Status | Code                                         | `Retry-After` |
| --------------------------- | ------ | -------------------------------------------- | ------------- |
| per-IP budget spent         | 429    | `IP_RATE_LIMIT_EXCEEDED`                     | yes           |
| per-email budget spent      | 429    | `LOGIN_RATE_LIMIT_EXCEEDED`                  | yes           |
| Redis unreachable           | 503    | `IP_RATE_LIMIT_UNAVAILABLE` / `LOGIN_RATE_LIMIT_UNAVAILABLE` | **no** |
| client address unresolvable | 503    | `CLIENT_IP_UNAVAILABLE`                      | **no** |

`LOGIN_RATE_LIMIT_EXCEEDED` deliberately does not say which budget refused. The
path already names the endpoint, and naming the dimension would tell a caller
whether their guess ran into a shared address or into a specific account — more
than the response needs to carry.

`Retry-After` follows the same rules as the PIN budgets: read from the counter's
remaining TTL rather than the configured window, returned by the same atomic
script that incremented the counter, rounded up, never negative, and not extended
by a request that is being refused.

**A limiter that cannot answer is a refusal, not permission.** Allowing on
failure would let anyone remove the ceiling by causing an outage. A 503 names no
recovery time because there is none to name, and the credentials were never
tried: an outage must not become a way to have a password checked.

### Enumeration resistance

Three mechanisms, none of which alone is sufficient:

1. **The lookup reports one thing for two cases.** `GetUserForLogin` joins the
   credential table, so an address with no account and an account with no
   password credential (Google-only) both come back as `INVALID_CREDENTIALS`.
   Neither releases the charge, or an attacker could spend someone's budget at
   leisure by probing addresses that do not exist.
2. **A dummy verification runs when the lookup finds nothing.** Without it, an
   unknown address returns before any password work while a wrong password pays
   for a full Argon2id derivation — a gap large enough to tell an observer which
   addresses exist, and one that also makes enumerating them cheap.
   `PasswordHasher.DummyVerify` performs a real derivation against a fixed hash
   built from the same constants `Hash` uses and computed once through
   `sync.OnceValue`, so it cannot fall behind the active parameters and is not
   re-derived per request. It equalizes the dominant term and nothing else: a
   successful login is still slower, because it adds a session write.
3. **The address is normalized once**, by `registration.NormalizeEmail`, and that
   single value is used for both the budget and the lookup, so the two can never
   disagree about which account is being limited. A budget keyed on the raw
   submission would hand an attacker `Alice@Example.com` and
   `alice@example.com` as two independent allowances for one account.

The endpoint has always returned the same code and message for a wrong password
and for an unknown address. What changed is the cost, not the response.

### Argon2id parameter bounds

`internal/security` bounds what a stored hash may ask the server to do: `m ≤
128 MiB` and `t ≤ 8`, against active parameters of `m = 64 MiB`, `t = 3`, `p =
4`. Both are enforced in `parseArgon2Params`, **before** argon2 is reached —
`argon2.IDKey` allocates its memory up front, so an unbounded value is an
unbounded allocation and the process does not survive it. The ceilings are
absolute rather than multiples of the active constants so that they cannot
quietly become meaningless, and `TestActiveParametersFitWithinBounds` fails if
the active constants are raised past them, turning "the verifier stopped
accepting new hashes" into a build failure rather than a production incident.

**There are deliberately no lower bounds.** A hash weaker than the active
parameters must still verify, or a credential written before the parameters were
strengthened would lock its user out permanently:
`PasswordVerificationResult.NeedsRehash` exists but nothing acts on it yet, so
there would be no way back in short of a reset. A floor is a product decision
about those users, not a parser decision.

The ceiling has to stay survivable, because the per-client login limiter bounds
how many verifications a caller may *start*, not how expensive each one is. A
verification at the ceiling costs about 128 MiB across eight passes — roughly
five times a legitimate one.

### Verification

`internal/security` — memory, iteration and parallelism ceilings enforced before
the derivation; the ceilings themselves accepted inclusively; a
weaker-than-active hash still verifying and reporting `NeedsRehash`; the active
parameters fitting within the ceilings.

`internal/api/auth/login` (service) — the budget charged on the canonical address
and released only on success; a wrong password and an unauthenticable account
both keeping their charge; a spent budget refusing before the lookup (no
`GetUserForLogin` expectation is registered, so the test fails if the lookup is
reached at all); an unusable limiter failing closed with no `Retry-After`; a
release failure not failing the login; and the enumeration floor, measured as the
fastest of five known-account and five unknown-address logins.

`internal/api/auth/login` (routes) — the declared policy applied in front of the
handler, the subject canonicalized, a spent budget stopping the request, an
unusable one producing 503, and no Google route spending a budget.

`internal/integration` — against real Postgres and real Redis: the per-email
budget exhausting on the eleventh attempt with nothing issued; `Retry-After`
present, integral, within the window and non-increasing across refusals; one
address's guesses not spending another's; one address spelled five ways drawing
on a single budget; a successful login restoring it; both 503 paths carrying no
header and issuing nothing; the per-IP budget refusing without spending the
per-email one; one address exhausted not affecting a neighbour; concurrent
requests from one address admitting exactly the budget; spoofed headers landing
on the peer's address; and Google login spending no budget at all.

**The two tests that measure rather than assert were proven to detect what they
claim to.** With the Lua script replaced by a client-side `GET`/`SET`
read-modify-write, the concurrency test fails; with `DummyVerify` removed, the
enumeration floor fails. Both pass again once restored.

Nothing here was weakened to make a test pass.

---

## Bounded password-work capacity — implemented

Rate limiting and password work are different controls, and only the first of
them is a rate limit.

A rate limit bounds how often one subject may submit requests. It does not bound
how much expensive work is running right now, because a limit per subject
composes: a caller holding a thousand addresses holds a thousand budgets, and
every one of them can be spent at the same moment. On a constrained instance the
callers causing the memory pressure are frequently not the ones being limited.

### The mechanism

`internal/security.PasswordWorkLimiter` bounds how many derivations run at once
across every subject and every feature. It keeps no queue, no per-caller state
and no goroutine of its own.

| Setting                     | Env                        | Default | Range  |
| --------------------------- | -------------------------- | ------- | ------ |
| `PasswordWorkConcurrency`   | `PASSWORD_WORK_CONCURRENCY` | `1`    | 1–64   |
| `PasswordWorkWait`          | `PASSWORD_WORK_WAIT_MS`    | `1000`  | 0–60000 |

Both are range-checked at startup and a supplied-but-unusable value stops the
process rather than being corrected — the same rule `WORKER_CONCURRENCY` follows.
The ceilings are typo guards, not tuning values: at the active parameters each
derivation costs 64 MiB, so even the ceiling is more than a small instance could
hold.

`Acquire` returns the release belonging to that acquisition rather than the
limiter exposing an unowned `Release()`. That pairing is the ownership guarantee:
a release that runs again for an acquisition that already completed would
otherwise hand back a slot a different, live operation was relying on, and the
limiter would admit one more derivation than its limit. The three outcomes are
kept separable — success, `ErrPasswordWorkCapacityTimeout`, and `ctx.Err()` —
because a caller that went away must not be told it was merely busy.

### Where it is enforced

Inside the shared `PasswordHasher`, not at the call sites. `Hash`, `Verify` and
`DummyVerify` all take a slot and give it back, so a call site cannot reach
Argon2id without passing through the bound. One limiter is shared by login and
registration; two would each allow their own number and the total would be the
sum.

The dummy verification is bounded too, and it has to be: it costs exactly what a
real verification costs, so a mitigation exempt from the bound would be the
cheapest way to hold more derivations at once than the instance was built for.

`Verify` decodes the stored hash before acquiring, so a row nobody can parse
cannot hold a slot against every real request.

### Responses

| Situation                    | Status | Code                        | `Retry-After` |
| ---------------------------- | ------ | --------------------------- | ------------- |
| no password-work capacity    | 503    | `PASSWORD_WORK_UNAVAILABLE` | yes, the configured wait |

It is neither `INVALID_CREDENTIALS` nor a rate-limit code. The password was never
checked, so reporting a bad password would be a lie the caller acts on, and
reporting a rate limit would name a budget that was not spent and a window that
does not exist. A caller that went away keeps its own context error rather than
being converted to this.

Password reset's `Hash` is bounded too — it goes through the same hasher — but
its refusal is not yet translated into this response; it is currently reported as
an internal fault. That is the next task.

### Verification

`internal/security` — every operation refusing when the slot is held from
outside, a refused operation returning nothing usable, cancellation and an
unparseable hash each reported as themselves rather than as exhaustion, capacity
returned on success, refusal and error paths, `DummyVerify` not re-entering the
limiter (which at concurrency one would be a deadlock rather than a slow request),
the reported wait, and a hasher refusing to be built without a limiter.

`internal/api/auth/login` and `internal/api/auth/registration` — capacity
exhaustion producing 503 with a bounded `Retry-After` on both the known-account
and unknown-account paths, not being mistaken for invalid credentials, a rate
limit, an input error or a fault, the public message describing none of the
mechanism, and the same request succeeding once capacity returns with the email
budget still accounting exactly one failure for the refused attempt and one
release for the successful one.

Every one of these holds the single slot from the test rather than waiting for a
real derivation, so none depends on how long Argon2id takes on the machine.

---

## Outstanding deployment work — not implemented

These are unresolved because the hosting platform and the email provider have
**not been selected**. They are deployment tasks, not application gaps, and the
distinction matters: the mechanisms below exist and are verified, and what is
missing is the information only a chosen environment can supply.

### 1. Client IP and deployment proxy configuration

- Select the actual hosting platform.
- Determine whether the application is behind a reverse proxy or load balancer.
- **Verify the actual forwarding-header behaviour** by sending a forged
  `X-Forwarded-For` to a deployed instance and observing what the application
  resolves. Whether a platform appends to or replaces the header determines
  whether a trusted-proxy configuration is correct at all.
- Configure `CLIENT_IP_SOURCE` and `TRUSTED_PROXIES` from verified information
  only.
- Test real client-address resolution in the chosen environment.
- Reassess the initial per-IP threshold against real traffic and against
  shared-address false positives. There are now **two** per-IP policies, not
  one: `20/10min` for PIN issuance and `30/10min` for manual login. They are
  separate counters and one being spent says nothing about the other, so both
  need a traffic-informed threshold.

**The specific risk that cannot be closed in code.** With the default `peer`
source, a deployment that is actually behind a proxy resolves every client to the
proxy's own address, putting them all in one counter. That is a misconfiguration
with a visible symptom — unrelated users being refused — rather than a silent
one, and the startup log records the resolved mode so a deployment's actual
configuration is in the record. It is not eliminated, only made visible.

Two things make it harder to leave unnoticed and are worth knowing:

- `app.Test` (Fiber v3.5.0) serves every request from an in-memory connection
  whose `RemoteAddr()` is hard-coded to `0.0.0.0`. Per-address behaviour cannot
  be exercised through it without a configured trusted proxy, which is why the
  integration tests drive client addresses through a proxy-configured app.
- Fiber's `isValidProxyIP` classifies a dotted-quad-in-IPv6 form such as
  `::ffff:198.51.100.5` as neither a valid IPv4 nor a valid IPv6 address, so the
  walk skips it and falls back to the peer address. Such a client is grouped with
  the proxy rather than separated. This errs toward over-sharing, never toward
  evasion, and `NormalizeIP` handles the form correctly when it reaches it.

**Login makes the consequences of that misconfiguration worse than PIN did.** A
collapsed counter on `POST /auth/login` means one unlucky address exhausts a
30-request budget that everyone behind the proxy shares, and the next real user
cannot sign in at all. The per-IP budget there is deliberately set higher than
the PIN one for exactly this reason, but it is mitigation, not a fix.

### 2. Email provider selection and production configuration

- Select the email delivery provider. None has been chosen; non-development
  builds currently use `email.NewUnconfiguredSender()`, which errors on send.
- Document its required environment variables and secret management.
- Complete sender/domain verification and any provider-required IP
  verification or allowlisting.
- Document provider-specific errors, delivery failures, quotas and retry
  behaviour.
- Configure provider-level sending limits, usage monitoring and budget
  safeguards where available.
- Verify that the application's rate limits complement rather than replace
  provider-level safeguards.

**Consequence of the second one, recorded here so it is not lost:** until a
provider exists, the per-email budget is the *only* ceiling on what verification
mail this application can trigger. It is a real control and it is verified, but
it is an application-level one, not a provider-level one.

### 3. Production readiness

- Verify Redis availability, TLS and connection configuration, and failure
  behaviour for the selected hosting platform. The limiter fails closed, so a
  Redis outage currently stops PIN issuance **and manual login** entirely rather
  than letting either through unthrottled — deliberate, and worth confirming is
  the intended availability trade-off for the chosen environment. Manual login is
  the sharper half of that trade: an outage means nobody can sign in, and unlike
  PIN issuance there is no second way in for the user to fall back on.
- Reassess both the per-email and per-IP policies after observing realistic usage.
  For login specifically, `LoginEmailFailureLimit`/`LoginEmailFailureWindow` in
  `internal/api/auth/login/constants.go` is the single value to revisit if
  account lockouts are reported — an attacker who knows an address can
  deliberately exhaust that budget and lock the account out for up to the window.
  The window is kept short precisely so recovery is quick.
- Confirm the Argon2id active parameters (`m = 64 MiB`, `t = 3`, `p = 4`) are
  right for the chosen instance size, and that `argon2MaxMemory` (128 MiB) still
  leaves a verification survivable when several run at once. The ceilings exist
  so a stored hash cannot ask for unbounded work; the active parameters are what
  a legitimate verification actually costs.
- Confirm `PASSWORD_WORK_CONCURRENCY` against the chosen instance. The default
  of `1` suits a constrained instance, where a single derivation is already a
  large share of the memory available. At one vCPU a measurement on the target
  showed two concurrent derivations taking twice as long as one — fully
  serialized — while holding twice the memory, so on that shape the limit costs
  nothing to keep at one. A deployment with memory to spare should raise it and
  measure rather than take the default on trust.
- Ensure deployment documentation does not imply that the email provider or the
  hosting platform has already been selected. Neither has.

---

## Testing and verification status

The current tree has been verified with:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test ./... -count=1` — pass
- `go test -race ./... -count=1` — pass, 20 packages, no data race reported
- `sqlc generate` — pass
- `mockgen` — pass
- `swag init -g cmd/api/main.go -parseInternal` — pass
- `gofmt -l` on every file changed — clean

The generators were re-run after the change and produced byte-identical output,
so no generated file is stale.

Integration tests require Docker and use the project's PostgreSQL test container.
The queue-facing integration tests additionally start a Redis testcontainer,
lazily and only when one is needed. The rate-limit integration tests use that
same lazily-started Redis rather than a second one.

The **PIN** rate-limit and concurrency tests were additionally re-run
repeatedly (`-count=6` for the integration ones, `-count=8` for the unit ones)
and showed no flakes. That repeated running was done when the PIN budgets were
added and has not been repeated since.

**The login rate-limit tests have not been run repeatedly.** They are recorded
here as single `-count=1` runs only, so no flake-resistance claim is made for
them. The one login test that is timing-sensitive — the enumeration floor in
`internal/api/auth/login` — compares the fastest of five runs of each path, which
is chosen so that a slow or loaded machine makes it pass more readily rather than
less, but that is a design argument and not a measurement.

**The race detector has been run, locally and in full.** `-race` requires cgo, so
it needs a C compiler; GCC 16.2.0 (MSYS2 UCRT64) is now installed on the Windows
development machine and the whole suite runs under it:

- `go test -race ./internal/security/... -count=1` — pass
- `go test -race ./internal/api/auth/login/... -count=1` — pass
- `go test -race ./internal/api/auth/registration/... -count=1` — pass
- `go test -race ./... -count=1` — pass, all 20 packages, no `DATA RACE` reports

The limiter and hasher concurrency tests were additionally re-run five times
each under the detector, since a single run is weak evidence where the code is
contended by design.

**CI still does not run it.** `.github/workflows/test.yml` runs
`go test ./... -v` on `ubuntu-latest` without `-race`, so the verification above
is local to a machine with a C compiler and is not reproduced on every change.
Adding `-race` to that step needs no further configuration on `ubuntu-latest` and
is outstanding.

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

20. **A forward header is believed only behind a verified allowlist, and the
    declaration of where a client's address comes from is one setting rather than
    two.** `CLIENT_IP_SOURCE` is `peer` or `proxy`, and everything else — the
    framework's proxy trust, its allowlist, which header is read, and whether the
    forwarded chain is parsed and validated — is derived from it. There is no
    combination in which a header is read without a verified allowlist beside it,
    which is what makes "the header was present" and "the header was believed"
    the same thing. Do not read `X-Forwarded-For`, `X-Real-IP` or any similar
    header outside `middleware.IPRateLimit`'s use of `c.IP()`, and never as an
    identity, a rate-limit subject, or a logging key on its own.

21. **`EnableIPValidation` is mandatory whenever a header is read.** Fiber v3.5.0's
    `extractIPFromHeader` returns the header's raw bytes when it is off, so a
    chain such as `203.0.113.7, 198.51.100.4` would become the client's identity
    — a separate counter for every chain a client chose to send. Turning it off
    next to proxy trust is a vulnerability, not a performance setting.

22. **A limiter that cannot answer refuses; it never allows.** A Redis outage, an
    unusable policy, or a client address that cannot be resolved all produce 503
    rather than a pass. Allowing on failure would let anyone remove the ceiling by
    causing one. Do not add a fallback that admits the request when the counter
    is unreachable.

23. **The two PIN budgets are separate dimensions and neither replaces the other.**
    The per-email budget stops one address being mailed repeatedly, which an
    attacker with many addresses defeats; the per-IP budget stops one address
    reaching the endpoints repeatedly, which an attacker with many proxies
    defeats. Merging them would leave a way in. `pin_issued_count` remains
    issuance history and is not a counter for either.

24. **The IP budget runs in front of the handler, not in the service.** A service
    never sees a request that failed to parse its UUID or named a verification
    that does not exist, so a limit enforced there would count only well-formed
    requests and would be free to burn with the cheapest kind of request there
    is. Moving it into the service would silently weaken it.

25. **`Retry-After` is the counter's remaining window, never the configured one.**
    It is read by the same atomic script that increments the counter, rounded up,
    and never negative. A refused request must not extend the window, and a 503
    must carry no `Retry-After` at all — there is no known recovery time and a
    fabricated one would be a guess presented as a fact.

---

## Next Step

Enrichment and automatic organization are both implemented and verified: the
synchronous per-item endpoint, the background enrichment worker, and the
background organization worker it hands off to. Their architecture is settled —
event-driven, no sweep, no persisted organization state — and is recorded in
decisions 8, 9, 13, 13a and 13b rather than reopened here.

Current order:

1. Deployment work that cannot start until the hosting platform is chosen:
   verify real forwarding-header behaviour, configure trusted proxies from
   verified information, and confirm client-address resolution in that
   environment. See "Outstanding deployment work" above.
2. Email provider selection and production configuration, which the per-email
   budget currently stands in for. See "Outstanding deployment work" above.
3. Decide whether to run a one-off backfill of items enriched before automatic
   organization existed. It is a product decision, not a mechanism gap, and it
   would be a separate deliberate operation rather than anything the worker does
   on its own.
4. Rate limiting for endpoints other than the two PIN endpoints, once there is
   real traffic to size the limits against.
5. Cloud Run deployment specifics for the worker, when a target is chosen.

Rate limiting for the PIN endpoints is implemented and verified: per-email and
per-IP budgets, Redis-backed atomic counters, `Retry-After`, and fail-closed
behaviour on Redis failure. What remains for it is deployment configuration, not
application code.

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