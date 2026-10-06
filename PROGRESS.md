# PROGRESS.md

Handoff notes for the next coding session. Read `AGENTS.md` first, then
`documentation/product.md`, then this file.

This is a handoff, not a diary. Keep it accurate and short.

## Current Status

### Saved Items — Phase A backend is complete

All four endpoints are implemented, wired into `main.go` via
`saved_item.RegisterModule`, and covered by tests.

Saved Items is implemented and committed; see the feature and decision sections
below for the current behavior and constraints.

| Endpoint                  | Status | Notes                                   |
| ------------------------- | ------ | --------------------------------------- |
| `POST /saved-items`       | done   | 201, requires Bearer                    |
| `GET /saved-items`        | done   | 200, paginated inbox, `created_at DESC` |
| `GET /saved-items/:id`    | done   | 200, owner-scoped                       |
| `DELETE /saved-items/:id` | done   | 200, hard delete, owner-scoped          |

**Not implemented:** update/edit (PATCH or PUT) — deliberately out of scope for
now. There is no update endpoint and none is scheduled.

Feature package: `internal/api/saved_item/`, following the `auth/session` vertical
slice (handler → service → repository, sqlc per feature).

### Authentication and ownership

- All four endpoints require a Bearer access token via `middleware.Auth`.
- `user_id` always comes from the token's `sub` claim, never from the request.
- Reads and deletes are owner-scoped in SQL: both `id` AND `user_id` are matched,
  so another user's row matches nothing.
- A missing item and another user's item both return `RESOURCE_NOT_FOUND` (404).
  Integration tests assert the two 404 response bodies are byte-identical, so the
  endpoints never disclose whether an ID exists.
- Invalid UUID in the path → 400 `BAD_REQUEST`.

### Domain derivation and normalization

`domain` is derived server-side from the submitted URL using Go's `net/url`. The
remote page is never contacted. Normalization is intentionally minimal:

1. Parse the URL, take `Hostname()`.
2. Lowercase the hostname.
3. Remove one leading `www.` if present.

No other subdomain normalization, no public suffix or registrable-domain
detection. `m.youtube.com` and `youtube.com` remain distinct by design.

### platform and title

Both are **NULL** for every item saved in Phase A, and they are still NULL for
every item today: the columns and the extraction layer exist, but nothing calls
the extractor. They will be populated by a background enrichment process that
does not exist yet. See the enrichment section below.

`platform` is the **content/source** platform (youtube, tiktok, instagram,
pinterest, ...). It is NOT the client platform. `X-Platform` is session-only.

### Unsorted user invariant

Every permanent user owns exactly one `Unsorted` system collection, and it is
the default destination for that user's saved items.

- A **pending registration** has no user and no Unsorted collection.
- A **finalized permanent user** receives Unsorted in the same transaction that
  creates the user, so a user can never exist without one.
- A **newly saved item** belongs to that user's Unsorted collection.

Both finalize paths do this. `FinalizeManualRegistration` and
`FinalizeSocialRegistration` each insert the collection between `CreateUser` and
the credential/identity plus continuation finalization, inside the existing
registration repository transaction. No new transaction was added, and no
Collections service was involved.

`POST /saved-items` resolves Unsorted by `user_id` plus `system_key = 'unsorted'`
— never by display name — and stores the resulting `collection_id`. The
`collections_system_key_unique` constraint prevents a second system collection
with the same key for one user.

### Collections — implemented

`internal/api/collection/` is a complete vertical slice (repository → service →
handler → routes), wired in `main.go` and `testutil/app.go` via
`collection.RegisterModule`.

| Endpoint                          | Status | Notes                              |
| --------------------------------- | ------ | ---------------------------------- |
| `PUT /saved-items/:id/collection` | done   | 200, owner-scoped, requires Bearer |

The endpoint takes `{"collection_name": "..."}` and files one saved item into one
user collection, creating that collection if it does not exist. There is **no
separate create-collection endpoint**, and no list, rename, or delete.

Behavior:

- **Create-or-get.** A user collection is created only as part of filing an item
  into it, so an empty user collection is never created on purpose.
- **Atomic.** `PutSavedItemIntoUserCollection` is a single repository method
  owning one transaction: lock and verify the item, create-or-get the collection,
  move the item, commit. No empty collection can survive a failed move.
- **Idempotent.** An item already in the target collection is a success with
  `already_in_collection: true` and no write — `updated_at` is left alone. Not a 409.
- **Owner-scoped.** The item must belong to the caller. Unknown and foreign IDs
  both return 404 `SAVED_ITEM_NOT_FOUND` with byte-identical bodies.
- **Unsorted stays permanent.** `system_key = 'unsorted'` resolves by key, never
  by display name, and may become empty.
- **Reserved names.** A name held by a system collection returns 409
  `COLLECTION_NAME_RESERVED` instead of silently filing the item into it.
- **Names** are trimmed and matched case-insensitively by
  `collections_user_name_unique`; the display name keeps its casing. No Go-side
  existence check.
- **Unrelated to enrichment.** The enrichment columns `enrichment_status`,
  `last_enriched_at`, `description` and `image_url` are neither read nor written.
  `enrichment_started_at` was dropped by migration 000024 and no longer exists.

Committed as `1e85c5d feat: add saved item collection flow`.

### sqlc generated-model cleanup — done

`omit_unused_structs: true` is enabled across all 13 sqlc targets, so each
target's `models.go` carries only the table structs its own queries reference
instead of a struct per table. 169 unused generated models were removed and no
generated query code changed: every `db.go` and `queries.sql.go` is byte
identical, and the diff is `models.go`-only removals.

Saved Item now owns its projected type, `saved_item.SavedItem`, in a new
`types.go`. It has the 8 columns the saved items queries project and replaces the
generated `saveditemdb.SavedItem` table model, which those queries never
referenced: migration 000022 gave `saved_items` columns that the queries do not
select, so sqlc emitted a per-query row type instead. Models with a real
cross-feature consumer are kept — `sessiondb.Session` is still generated because
login and registration both use it.

`AGENTS.md` documents the rule under Code generation.

### Search — implemented and committed

`internal/api/search/` is a complete vertical slice (repository → service →
handler → routes), wired in `main.go` and `testutil/app.go` via
`search.RegisterModule`. It owns its own read projections rather than borrowing
`saved_item.SavedItem` or the collection feature's type.

| Endpoint      | Status | Notes                              |
| ------------- | ------ | ---------------------------------- |
| `GET /search` | done   | 200, owner-scoped, requires Bearer |

Built in three reviewed phases: migration 000023, then the data/domain layer,
then the HTTP layer. The complete Search feature is committed.

Final API contract:

- `GET /search?q=<query>` — authenticated. `user_id` always comes from the token's
  `sub` claim.
- `q` is the only parameter. It must be 2–128 runes after trimming; blank, too
  short and too long all return 400 `INVALID_SEARCH_QUERY`. Length is measured in
  runes, and the query is trimmed but never lowercased.
- The response is `data.collections` and `data.saved_items`. Both are always
  present and always arrays — an empty result is a successful 200, never a 404.
- Saved items match on **title, domain and url**. Collections match on **name**.
- `platform`, IDs, timestamps and `collection_id` are not searchable.
  `collection_id` **is** returned on each saved item, because the client needs to
  show where a result lives.
- `Score` is on the internal projections, because the SQL needs it to rank, but it
  is **not** in the public response. Review removed it deliberately: the ordering
  is the contract, the number behind it is not, and its values depend on internal
  weights that are expected to change.

Matching and ranking:

- PostgreSQL native, `pg_trgm` based, user scoped. **No** Elasticsearch,
  OpenSearch, Meilisearch, Typesense or Algolia, and **no** PostgreSQL full-text
  search or `tsvector`. FTS is out of scope for v1.
- Substring matching is the normal path; `word_similarity()` is a **fallback
  inside the same query**, not a second round trip. There is one query per entity
  and no separate fuzzy endpoint.
- `word_similarity()`, not `similarity()`, because long titles and URLs score
  poorly against whole-string similarity. The fuzzy threshold is `> 0.3`.
- An exact-substring bonus of `10.0` outranks any fuzzy-only result, whose maximum
  weighted total is `2.4`. Recency is a deterministic tiebreaker only
  (`score DESC, created_at DESC, id DESC`), never a relevance component.
- System collections are searchable, so a search for "unsorted" finds Unsorted.

Limits are internal and not exposed as a parameter: saved items default to 20 and
cap at 50, collections cap at 5. **Cursor pagination is not implemented**, and
neither is autocomplete or search suggestions. No migration was added for either.

Worth knowing before touching the index strategy: the composite GIN indexes from
migration 000023 serve the `user_id` restriction, but a single GIN index cannot
satisfy an `OR` across `title`, `domain` and `url`, so the match clauses are
filtered rather than index-accelerated. Separately, `title` is NULL for every
saved item until enrichment runs, so the `title` trigram index currently indexes
an all-NULL column. Both were measured with `EXPLAIN`, not assumed, and neither
was redesigned. See decisions 14–17.

### Enrichment — security boundary and extraction implemented, not integrated

Two foundations are now implemented and verified. **Neither is wired into the
application**: there is still no enrichment service, repository method, handler,
route, or Asynq task, and no Saved Item has ever been fetched. Nothing a user
does can currently trigger enrichment.

#### Schema

Migration `000024_finalize_saved_item_enrichment` has been created and manually
applied. It finalizes enrichment on the single `saved_items` table. No separate
metadata table was introduced.

| Column              | Notes                                       |
| ------------------- | ------------------------------------------- |
| `description`       | optional, NULL until enrichment             |
| `image_url`         | optional, NULL until enrichment             |
| `enrichment_status` | `NOT NULL DEFAULT 'pending'`                |
| `last_enriched_at`  | optional, when metadata was last refreshed  |

`saved_items_enrichment_status_check` now allows exactly `pending`, `completed`,
`failed`. `enrichment_started_at` was dropped and `processing` was removed as an
allowed status: worker execution state is not product state, so an item stays
`pending` until enrichment completes or fails, and a worker that dies mid-job
needs no `processing` marker to reconcile afterwards.

Status semantics: `pending` means enrichment has not successfully completed yet,
`completed` means metadata was extracted and need **not** be complete, and
`failed` means the worker could not enrich that URL. The URL is the Saved Item's
real data and is independent of all three — a URL that cannot be fetched must
never invalidate or delete the Saved Item.

`internal/database/baseline/schema.sql` matches. The integration container is
built from the baseline, not from `migrations/`, so the two drift easily. No
migration has been created or modified since `000024`; none is needed for the
security or extraction work.

#### Outbound fetch security

`internal/security/outbound_fetch.go` is the SSRF boundary for any server-side
fetch of a user-supplied URL. No dependency was added.

- `NewGuardedHTTPClient(policy)` returns the only `*http.Client` this project
  should use to fetch a user-supplied URL. Its `net.Dialer.ControlContext`
  validates the **resolved** address at dial time, which closes the
  time-of-check/time-of-use gap that makes DNS rebinding work, and covers every
  redirect hop and pooled connection without redirect-specific code.
- Allowed schemes are `http`/`https` and allowed ports are 80/443 only. The port
  allowlist is the highest-leverage control in the file: it is what keeps a
  co-located Redis (6379), Cloud SQL (5432) and every other non-standard service
  out of reach without having to recognise them as private addresses.
- Blocked addresses: loopback, unspecified, RFC 1918, IPv6 unique-local,
  link-local (v4 and v6), multicast, zoned, plus 14 special-purpose prefixes
  that `net/netip` reports as ordinary global unicast. `::ffff:` forms are
  unmapped first, and NAT64 (`64:ff9b::/96`), 6to4 (`2002::/16`) and the GCP IPv6
  metadata address (`fd20:ce::254`) are blocked explicitly.
- Redirects capped at 3, `Timeout` 10s, `ResponseHeaderTimeout` 5s, dial and TLS
  handshakes 5s each, response body bounded at 2 MiB (applied to the
  *decompressed* stream, so a compression bomb is covered by the same limit).
- `Proxy` is nil rather than `http.ProxyFromEnvironment`, because a proxy
  connects from its own process and would put the dial-time policy out of reach.
- `ValidateOutboundURL` provides early rejection with clear errors: scheme,
  userinfo (refused outright — the classic `http://user@host` disguise), host
  names that can only mean something local (`localhost`, `*.local`, `*.internal`),
  unsafe literal addresses, and port.
- 100% statement coverage on every function in the file, including a real
  `httptest` server on loopback proving the guard is installed in the transport.

#### Enrichment extraction layer

`internal/enrichment/` is the reusable extraction core. It is deliberately
independent of Fiber, `internal/api`, sqlc, Asynq and persistence, so the future
endpoint and the future worker call the same code.

- `Enricher` interface with `NewEnricher(client *http.Client, policy security.OutboundFetchPolicy)`.
  The guarded client is injected; enrichment never constructs one, never uses
  `http.DefaultClient`, and holds **no** SSRF logic of its own. The client is the
  single boundary.
- `Metadata` carries `Title`, `Platform`, `Description`, `ImageURL`,
  `CanonicalURL`, `SiteName`, `Author`. Every field is `*string` and nil when
  nothing was found, so "no description" stays distinguishable from "an empty
  one". `SiteName` and `Author` have no `saved_items` column; no migration was
  added for them.
- Sources: `<title>`; `og:title/description/image/url/site_name/author`;
  `twitter:title/description/image/image:src`; `<meta name=description/author/application-name>`;
  `<link rel=canonical>`; JSON-LD `name`, `headline`, `description`, `image`
  (string, `ImageObject`, or list), `url`, `author.name`, `publisher.name`, plus
  `Organization`/`WebSite` nodes — across single objects, arrays, `@graph`,
  `{"@value":…}`, `@type` as string or list, and fully-qualified schema.org types.
- Precedence is declared as data, one slice per field, and all four sources write
  into one flat candidate map. JSON-LD candidate keys are namespaced `jsonld:`
  because the property names collide with the HTML vocabularies; without the
  prefix a meta description and a JSON-LD description were the same key and
  document order, not the declared order, decided the winner.
- `publisher.name` can never become the title. It is collected under its own key
  and JSON-LD descent suppresses generic fields inside reference subtrees.
- `platform` is derived only from metadata the page publishes about itself
  (`application-name`, then JSON-LD publisher/organization/website, then
  `og:site_name`). A recognised hostname with no metadata yields nil. There is no
  domain-to-platform mapping.
- Normalization is minimal: trim, collapse whitespace runs, fold non-breaking
  spaces. No lowercasing, truncation, or punctuation changes. Relative metadata
  URLs resolve against the **post-redirect** URL, non-http(s) results are dropped,
  and nothing is fetched or validated during extraction.
- `CanonicalURL` is reported and deliberately not acted on. Replacing a saved
  item's URL is a product decision and belongs to the caller.
- Failure semantics: every error is a `*Failure` with a `Kind` — `fetch`,
  `content`, or `parse` — so a caller can decide whether a job is worth retrying.
  Missing metadata is **not** a failure; a page with no Open Graph tags returns an
  empty `Metadata` and a nil error.
- 95% statement coverage across the package. No test makes a real network
  request.

`golang.org/x/net/html` is used for parsing. It was **already in the module graph
as an indirect dependency** at `v0.59.0`; `go mod tidy` only moved it to direct.
`go.sum` is unchanged — no module was added and no version changed.

#### Still not implemented

- An enrichment service or repository method on `saved_items`. No sqlc query
  reads or writes `title`, `platform`, `description`, `image_url`,
  `enrichment_status` or `last_enriched_at` yet.
- `POST /saved-items/:id/enrich`. No route, handler, response shape, or swagger
  annotation.
- Any Asynq task, `ServeMux` registration, or queue configuration.
- Any change to `cmd/worker`, `cmd/api`, or `internal/testutil/app.go`. Neither
  binary knows `internal/enrichment` exists.
- `updated_at` consequences of enrichment writes. The `saved_items_set_updated_at`
  trigger fires on any UPDATE, so the first enrichment will move `updated_at` on
  items whose metadata was null. Nothing orders by it, but `ListSavedItems`
  returns it.

#### Worker infrastructure

- `docker-compose.dev.yml` gained a `redis` service: `redis:8-alpine`,
  `stash-it-redis-dev`, `restart: unless-stopped`, port `6379`, no volume, and
  persistence explicitly disabled. It follows the existing `postgres` service
  conventions. The `postgres` service and the `volumes:` block are unchanged, and
  no second compose file was added.
- `github.com/redis/go-redis/v9` was added. No Redis client existed before.
- `internal/config/worker.go` adds `WorkerConfig` and `LoadWorker`, a separate
  configuration boundary. The worker validates only `AppEnv`, `DATABASE_URL` and
  `REDIS_URL`, and never requires `GOOGLE_CLIENT_ID`,
  `VERIFICATION_CODE_SECRET` or `ACCESS_TOKEN_SECRET`, so it can be deployed with
  only the variables it actually reads. `Config` and `Load` were not changed.
  `LoadWorker` tolerates a missing `.env.<APP_ENV>` where `Load` treats it as
  fatal, because godotenv never overrides variables that are already set and a
  deployed worker supplies real environment variables instead of a file.
- `cmd/worker/main.go` is a second binary. It imports nothing from
  `internal/api` and no Fiber. It loads its own configuration, reuses the
  existing `internal/logger` unchanged, builds a client with `redis.ParseURL`,
  pings, logs `worker started`, then blocks on `signal.NotifyContext` so the
  process stays alive until `SIGINT`/`SIGTERM`.
- Local `REDIS_URL=redis://localhost:6379/0` — one value for Docker, Cloud Run
  and a VPS, with no worker-side knowledge of the environment.
- Redis connectivity and worker startup were manually verified: the worker
  reached the ping, logged `worker started`, and stayed running until signalled.
  This has not been re-verified since.

#### Still not implemented (worker side)

- Redis queue or job processing of any kind (`LPUSH`, `BLPOP`, Streams, pub/sub),
  job payloads, retries or acknowledgements, a sqlc target for the worker,
  enrichment `queries.sql`, a generated worker package, URL fetching in the
  worker, HTML parsing in the worker, enrichment status writes, and actual
  background enrichment. `docker-compose.dev.yml`, `go.mod` and `cmd/worker/main.go`
  are the only things the worker infrastructure phase touched.

### Testing and verification status

As of the completed Search work:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test -count=1 ./...` — pass, all 10 packages ok
- Saved item unit and integration tests pass
- Collection unit and integration tests pass
- Registration unit and integration tests pass
- Search service unit tests, repository integration tests, and HTTP API
  integration tests pass
- `sqlc generate`, `mockgen`, `swag init -g cmd/api/main.go -parseInternal` all
  clean

Integration tests need Docker. They were run and passing. Re-run them before
relying on any claim.

After the security and extraction work, all three commands were re-run over the
whole tree and pass:

- `gofmt -l internal/enrichment internal/security` — clean
- `go build ./...` — pass
- `go vet ./...` — pass
- `go test ./... -count=1` — pass, 11 packages ok, including `internal/integration`
  (Postgres 18 testcontainer, Docker 29.7.2)
- `internal/security` — 100% statement coverage on `outbound_fetch.go`
- `internal/enrichment` — 95% statement coverage

`go.sum` is unchanged by the extraction work. `golang.org/x/net` moved from
indirect to direct in `go.mod` because `golang.org/x/net/html` is now imported;
the version did not change and no module was added.

`cmd/worker` itself was not exercised by these commands. Its only verification is
the manual Redis connectivity check recorded above.

## Important Decisions

Do not change these casually. Several were corrections of earlier mistakes.

1. **`saved_items.platform` is the content/source platform, not the client
   platform.** It means youtube/tiktok/instagram, not web/android/ios. An earlier
   implementation wrongly populated it from `X-Platform` and was corrected.
2. **`X-Platform` is session-only.** It populates `sessions.platform`. It must
   never be read in the `saved_item` feature. There is a regression test
   (`ignores a client supplied X-Platform header`) that fails if reintroduced.
3. **`platform` is NULL in Phase A.** Do not add detection, heuristics, or
   host-based inference.
4. **`title` is NULL in Phase A.** Not client-supplied. A later phase populates it.
5. **`domain` is derived and normalized server-side** (lowercase + strip one
   `www.`). Do not add PSL/registrable-domain logic without an explicit decision.
6. **Duplicate URLs are allowed.** No unique constraint, no dedup, no `url_hash`.
7. **No update/PATCH endpoint exists yet.** Do not add one unless asked.
8. **Delete is a hard delete.** No soft-delete or `deleted_at` column. Revisit
   only if enrichment later creates child rows that must be retained.
9. **No enrichment is wired into the application.** A worker binary exists and
   connects to Redis, and an extraction layer exists in `internal/enrichment`, but
   nothing is queued, nothing is consumed, and no Saved Item is ever fetched. No
   route, service, repository method or Asynq task calls the extractor, so the
   extraction code has never run against a real page. Superseded in part by
   decisions 21–26.
10. **The Phase B schema and lifecycle are in place.** Migration
    `000022_create_collections_and_saved_item_enrichment` added `collections`,
    `saved_items.collection_id` (`NOT NULL`, `ON DELETE RESTRICT`), and the
    `enrichment_status` / `enrichment_started_at` / `last_enriched_at` columns.
    It has been applied, and the Unsorted-per-user invariant described above now
    maintains it at runtime. The Collections API is now implemented on top of it
    (`1e85c5d`). Migration `000024_finalize_saved_item_enrichment` later
    finalized the enrichment columns: `description` and `image_url` added,
    `enrichment_started_at` dropped, `processing` removed. The columns exist and
    nothing writes them yet. `migrations/` is the production source of truth and
    must never be edited after creation. **Creating a migration and applying it
    are separate operations**, and applying one always requires explicit user
    instruction.
11. **`collections_system_key_check` subsumes `collections_type_check`.** Any row
    satisfying the system-key rule already has a valid `type`, so PostgreSQL
    reports the system-key constraint for an invalid `type` and the type
    constraint is never the one named in the error. Treat any check violation on
    `collections` as a validation failure rather than matching a constraint name.
12. **The schema does not enforce that a collection belongs to the saved item's
    owner.** `saved_items.collection_id` only guarantees the collection exists.
    Cross-user assignment is possible at the database level, so ownership checks
    belong in the service layer. Revisit only with an explicit decision.
13. **`omit_unused_structs: true` is enabled for every sqlc target.** Generated
    models are no longer shared across features by default. Each target's
    `models.go` now carries only the table structs its own queries reference.
    Models with a real cross-feature consumer are still generated and kept, such
    as `sessiondb.Session`, which login and registration both use. Saved Item owns
    its 8-field projected type (`saved_item.SavedItem`) instead of borrowing the
    generated `saveditemdb.SavedItem` table model, which its own queries never
    referenced because migration 000022 added columns they do not project. Two
    same-shaped structs across features are expected and are not a reason to
    introduce a shared domain type. Revisit only with an explicit decision.
14. **Search is PostgreSQL native and user scoped.** `pg_trgm` only, using
    `word_similarity()` for fuzzy matching. Do not add Elasticsearch, OpenSearch,
    Meilisearch, Typesense, Algolia, or any other external search engine, and do
    not add PostgreSQL full-text search or `tsvector` columns for v1. Neither was
    needed: the scale does not justify either.
15. **The relevance score is internal and is not part of the public contract.**
    `SearchSavedItem.Score` and `SearchCollection.Score` exist because the SQL
    ranks and orders on them, and they stay internal. `GET /search` returns the
    results already ordered and exposes no score. A substring match must always
    outrank a fuzzy-only match, and recency must only ever break ties. Revisit the
    weights or the threshold only with an explicit decision, and never let a
    weight change silently alter what a user sees first.
16. **Search limits are internal, and there is no pagination.** Saved items
    default to 20 and cap at 50, collections cap at 5. No `limit`, `page` or
    `cursor` parameter is exposed, so those caps are the only bound on one
    request. Cursor pagination, autocomplete and search suggestions are not
    implemented. Because results are capped and not paged, a user with more
    matches than the cap cannot currently reach the tail.
17. **The search indexes from migration 000023 are the v1 index strategy.** They
    serve the `user_id` restriction, but the trigram half cannot accelerate an
    `OR` across `title`, `domain` and `url`, and `title` is NULL for every saved
    item until enrichment exists. Adding a url trigram index, splitting the
    predicate, or introducing any search table, ranking column or denormalized
    document needs evidence from real query timings, not expectation.
18. **Worker execution state is not persisted.** `enrichment_status` is exactly
    `pending`, `completed`, `failed`, and there is deliberately no `processing`
    value and no `enrichment_started_at` column. A worker that dies mid-job
    leaves the item `pending`, which is the correct product state on its own.
    Do not reintroduce `processing` to make retries or observability easier;
    that needs an explicit decision. `last_enriched_at` is kept because "last
    refreshed" is product state and is independent of the status value.
19. **API and worker configuration are separate boundaries.** The worker uses
    `config.WorkerConfig` / `config.LoadWorker` and never reads `Config` or
    `Load`, so it never requires API-only secrets such as `GOOGLE_CLIENT_ID`,
    `VERIFICATION_CODE_SECRET` or `ACCESS_TOKEN_SECRET`. Sharing one environment
    and one `.env.<APP_ENV>` file is fine; sharing one struct or loader is not.
    Redis is configured as a single `REDIS_URL` value, not host/port/database
    fields, so the same variable works on Docker, Cloud Run and a VPS.
20. **Enrichment failure must never invalidate a Saved Item.** The URL is the
    Saved Item's real data and is independent of `enrichment_status`. A job that
    is lost, a page that times out, a site that blocks the request, or an
    extraction that finds nothing must all leave the item present and usable.
    Revisit only with an explicit decision.
21. **`security.NewGuardedHTTPClient` is the only outbound fetch path.**
    `internal/security` owns the SSRF boundary: the dial-time address check, the
    scheme and port allowlists, the redirect cap, the timeouts and the response
    size limit. Enrichment holds none of that logic and must not grow any. Do not
    add a second client, transport, or address list anywhere in the codebase. The
    guard is at dial time rather than before the request on purpose: validating
    the URL and then connecting leaves a gap that DNS rebinding exploits, and
    putting the check in the dialer also covers redirect hops for free.
22. **The port allowlist is 80/443 only.** This is a deliberate product
    constraint, not an oversight. It is the control that keeps a co-located Redis
    or Cloud SQL out of reach, and some legitimate sites on other ports will not
    enrich. Widening it needs an explicit decision.
23. **`internal/enrichment` is reusable extraction logic, not a service.** It
    takes a URL and returns metadata or a classified failure. It has no Fiber, no
    sqlc, no Asynq and no persistence, so the future endpoint and the future
    worker cannot diverge. Persistence, `enrichment_status` writes and the
    `pgtype.Text` conversion all belong to the caller. This also keeps
    `cmd/worker`'s "imports nothing from `internal/api`" invariant intact.
24. **`completed` means the process succeeded, not that fields were found.** An
    item whose page exposed only a title is `completed`; an item whose page
    exposed nothing usable may also be `completed`. `failed` means the process
    itself failed. Missing metadata is never a failure, and `enrichment.Kind`
    exists so the caller can separate a retryable fetch problem from a permanent
    content problem without matching error text.
25. **Enrichment must not touch `collection_id` or move items.** Metadata
    extraction has no opinion about organization. Automatically filing items into
    collections is a separate worker responsibility, and conflating the two would
    make a failed or partial enrichment look like a lost item. Revisit only with
    an explicit decision.
26. **`platform` stays metadata-derived.** The extractor takes it from
    `application-name`, JSON-LD publisher/organization/website, then
    `og:site_name`, and never from the URL hostname. This extends decision 3:
    adding a host-based rule or a domain-to-platform mapping now needs an explicit
    decision, because it would contradict both the product definition of the field
    and the extractor's tests.

Two deliberate omissions worth recording, both revisitable:

- `Metadata.Author` and `Metadata.SiteName` have no `saved_items` column. They
  are extracted because pages publish them and they are the raw material
  `Platform` comes from, but **no migration may be added for them** without an
  explicit decision.
- `Metadata.CanonicalURL` is reported and deliberately not acted on. Replacing a
  Saved Item's `url` with its canonical form is a product decision about what a
  Saved Item means, and it belongs to the caller.

## Next Step

**Phase B is complete and committed.** Per the roadmap in `documentation/product.md`, step 2 is
"Collections + Search" and both are done, so the current work is step 3,
**background URL metadata extraction**, which is now partly in place.

The roadmap calls "Saved item detail" and "Delete a saved item" Phase B work,
but both were implemented earlier as part of the core loop. They are done.

The schema and user lifecycle are in place: Collections, `saved_items.collection_id`,
the final enrichment columns, the Unsorted-per-user invariant, and the search
indexes. `internal/database/baseline/schema.sql` matches migration 000024.

For this step the following now exist: the enrichment columns, local Redis,
`go-redis/v9`, `config.LoadWorker`, a `cmd/worker` binary that starts and verifies
its Redis connection, the guarded outbound HTTP client in `internal/security`, and
the extraction layer in `internal/enrichment`.

What is still missing, in the order it needs doing:

1. **An enrichment sqlc target and queries** that read a Saved Item's URL for
   enrichment and write back `title`, `platform`, `description`, `image_url`,
   `enrichment_status` and `last_enriched_at`. No query touches those columns yet.
2. **A saved-item enrichment service and repository method** that owns
   `pgtype.Text` conversion and the status write, wrapping `enrichment.Enricher`.
   Persistence stays outside `internal/enrichment` (decision 23).
3. **`POST /saved-items/:id/enrich`** — exactly one item, owner-scoped, returning
   the updated item with its `enrichment_status`. Not a batch endpoint. It mounts
   under the existing `/saved-items` group the way `collection` does, and a
   synchronous fetch failure is reported in the payload as `enrichment_status`
   rather than as a 5xx, since the item itself is fine.
4. **Enqueueing on save**, then **the Asynq consume side**: task type, payload,
   retry classification driven by `enrichment.Kind`, and graceful shutdown.
   `cmd/worker` and `cmd/api` still know nothing about enrichment.

Decisions that must be settled before step 4 rather than during it: whether
`POST /saved-items` enqueues at all, what the worker concurrency is (asynq's
default is `NumCPU`, which is 1 on a default Cloud Run instance), and whether
per-user rate limiting exists. The API binary also has no signal handling yet, so
graceful shutdown is part of that work rather than something the worker can
assume.

Search pagination, autocomplete, search suggestions, the `UNION` predicate
optimization, and collection CRUD are not part of the current scope. Do not add
unrelated features without explicit direction.

## Resume Instructions

1. Read `AGENTS.md`, `documentation/product.md`, and this file before coding.
2. Check `git status` and `git diff` to understand the actual current working tree.
3. Inspect the relevant existing feature packages before changing code. Do not
   reimplement functionality that is already present.
4. Run `go build ./... && go vet ./... && go test ./...` before assuming the tree
   is healthy.
5. Avoid unrelated refactors. If something looks wrong outside the current task,
   report it instead of fixing it as a side effect.
6. Never edit generated files manually. Change the source (`queries.sql`, feature
   code, etc.) and regenerate generated output.
7. Do not apply migrations or create commits unless explicitly instructed.
8. Enrichment has never run. `internal/enrichment` and `internal/security` are
   verified by their own unit tests only, so do not assume behaviour that is not
   asserted there.
