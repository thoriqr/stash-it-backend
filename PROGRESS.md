# PROGRESS.md

Handoff notes for the next coding session. Read `AGENTS.md` first, then
`documentation/product.md`, then this file.

This is a handoff, not a diary. Keep it accurate and short.

## Current Status

### Saved Items — Phase A backend is complete

All four endpoints are implemented, wired into `main.go` via
`saved_item.RegisterModule`, and covered by tests.

Committed work so far:

- `0fd199f feat: complete saved items core flow`
- `fix: restore saved item creation after collections schema`
- `7f5637b feat: initialize unsorted collection on registration`
- `1e85c5d feat: add saved item collection flow`

| Endpoint          | Status  | Notes                                        |
| ----------------- | ------- | -------------------------------------------- |
| `POST /saved-items`     | done | 201, requires Bearer                        |
| `GET /saved-items`      | done | 200, paginated inbox, `created_at DESC`       |
| `GET /saved-items/:id`  | done | 200, owner-scoped                            |
| `DELETE /saved-items/:id`| done | 200, hard delete, owner-scoped               |

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

Both are **NULL** for every item saved in Phase A. They are populated by a later
background enrichment process that does not exist yet.

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

Committed as `feat: initialize unsorted collection on registration`.

### Collections — implemented

`internal/api/collection/` is a complete vertical slice (repository → service →
handler → routes), wired in `main.go` and `testutil/app.go` via
`collection.RegisterModule`.

| Endpoint | Status | Notes |
| -------- | ------ | ----- |
| `PUT /saved-items/:id/collection` | done | 200, owner-scoped, requires Bearer |

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
  `already_in_collection: true` and no write — `updated_at` is left alone. Not a
  409.
- **Owner-scoped.** The item must belong to the caller. Unknown and foreign IDs
  both return 404 `SAVED_ITEM_NOT_FOUND` with byte-identical bodies.
- **Unsorted stays permanent.** `system_key = 'unsorted'` resolves by key, never
  by display name, and may become empty.
- **Reserved names.** A name held by a system collection returns 409
  `COLLECTION_NAME_RESERVED` instead of silently filing the item into it.
- **Names** are trimmed and matched case-insensitively by
  `collections_user_name_unique`; the display name keeps its casing. No Go-side
  existence check.
- **Unrelated to enrichment.** `enrichment_status`, `enrichment_started_at` and
  `last_enriched_at` are neither read nor written.

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

### Search — implemented

`internal/api/search/` is a complete vertical slice (repository → service →
handler → routes), wired in `main.go` and `testutil/app.go` via
`search.RegisterModule`. It owns its own read projections rather than borrowing
`saved_item.SavedItem` or the collection feature's type.

| Endpoint | Status | Notes |
| -------- | ------ | ----- |
| `GET /search` | done | 200, owner-scoped, requires Bearer |

Built in three reviewed phases, **none of it committed yet**: migration 000023,
then the data/domain layer, then the HTTP layer. The working tree is the whole of
basic search.

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
9. **No metadata/enrichment worker exists.** No scraping, `og:title`, `og:image`,
   JSON-LD, or remote fetching anywhere in the codebase.
10. **The Phase B schema and lifecycle are in place.** Migration
    `000022_create_collections_and_saved_item_enrichment` added `collections`,
    `saved_items.collection_id` (`NOT NULL`, `ON DELETE RESTRICT`), and the
    `enrichment_status` / `enrichment_started_at` / `last_enriched_at` columns.
    It has been applied, and the Unsorted-per-user invariant described above now
    maintains it at runtime. The Collections API is now implemented on top of it
    (`1e85c5d`), but there is still **no enrichment worker**.
    `migrations/` is the production source of truth and must never be edited
    after creation. **Creating a migration and applying it are separate
    operations**, and applying one always requires explicit user instruction.
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

## Next Step

**Phase B is complete.** Per the roadmap in `documentation/product.md`, step 2 is
"Collections + Search" and both are done, so the next work is step 3,
**background URL metadata extraction**.

A note on phase terminology, since it is easy to misread:

- The roadmap lists "Saved item detail" and "Delete a saved item" under Phase B,
  but both were built during Phase A because the core loop needed them. They are
  done, not pending.
- **Collections is done** — repository, service, and the
  `PUT /saved-items/:id/collection` endpoint, committed as `1e85c5d`.
- **Basic search is now done too** — migration 000023, the search queries and sqlc
  target, repository, service, projections, and the `GET /search` endpoint, with
  unit, repository integration and HTTP integration tests. See the Search section
  above.
- `collection_id` was deliberately omitted from the Phase A migration for this
  reason, and was added later by migration 000022, which has been applied.

The schema and the user lifecycle are both in place: the `collections` table,
the `saved_items.collection_id` relationship, the `enrichment_*` columns, the
Unsorted-per-user invariant, and the two search indexes.
`internal/database/baseline/schema.sql` now matches migration 000023.
Registration, Saved Item, Collections, and Search work is finished — do not redo
it.

Basic search is **uncommitted**. Run `git status` before starting: the working
tree contains migration 000023, the whole search feature, the baseline and
`sqlc.yaml` changes, the generated Swagger, and the wiring in `main.go` and
`testutil/app.go`. Committing was explicitly out of scope for that work, so
whether to commit it is an open decision for the user.

What is **not** done, and should not be started without asking: metadata
extraction itself, cursor pagination, autocomplete, search suggestions, the
`UNION` predicate optimization, and collection CRUD. Confirm scope with the user
before writing code, and do not invent additional features such as reminders,
price tracking, comparison, or AI.

## Resume Instructions

1. Read `AGENTS.md` — it is the authoritative operating guide.
2. Read `documentation/product.md` for scope and direction.
3. Read this file for current state and decisions.
4. Run `git status` and `git diff` before changing anything. Saved Items,
   registration, the Unsorted invariant, Collections and the sqlc cleanup are
   **committed** (`0fd199f`, `fix: restore saved item creation after collections
   schema`, `7f5637b feat: initialize unsorted collection on registration`,
   `1e85c5d feat: add saved item collection flow`, then `88e24d8 refactor(sqlc):
   own the saved item projection and enable omit_unused_structs`).
   **Basic search is not committed** — it is the whole current working tree. Do
   not discard it, and do not commit it without being asked.
5. Inspect `internal/api/saved_item/`, `internal/api/collection/`,
   `internal/api/search/`, and `internal/api/auth/registration/` before writing
   code. Do not reimplement existing endpoints, the Unsorted creation, the
   collection flow, or search.
6. Re-verify the tree compiles before assuming a clean start:
   `go build ./... && go vet ./... && go test ./...`
7. Avoid unrelated refactors. If something looks wrong, report it rather than
   fixing it as a side effect.
8. Never edit files in `migrations/` or `internal/api/**/generated/`. Change
   `queries.sql` and regenerate, or change the feature code.
9. Do not apply migrations or commit unless explicitly instructed.
