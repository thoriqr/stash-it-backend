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
- `feat: initialize unsorted collection on registration`

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

**There is still no Collections feature.** No Collections repository, service,
handler, CRUD API, move-item behavior, basic search, or enrichment worker
exists. The only collection-related production SQL is the registration-scoped
`CreateUnsortedCollection`, which lives in registration because it must
participate in the existing finalize transaction.

### Testing and verification status

As of the completed Unsorted registration work:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test -count=1 ./...` — pass, all 8 packages ok
- Saved item unit and integration tests pass
- Registration unit and integration tests pass
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
    maintains it at runtime. It is still **schema plus lifecycle only**: there is
    no Collections API and no enrichment worker. `migrations/` is the production
    source of truth and must never be edited after creation. **Creating a
    migration and applying it are separate operations**, and applying one always
    requires explicit user instruction.
11. **`collections_system_key_check` subsumes `collections_type_check`.** Any row
    satisfying the system-key rule already has a valid `type`, so PostgreSQL
    reports the system-key constraint for an invalid `type` and the type
    constraint is never the one named in the error. Treat any check violation on
    `collections` as a validation failure rather than matching a constraint name.
12. **The schema does not enforce that a collection belongs to the saved item's
    owner.** `saved_items.collection_id` only guarantees the collection exists.
    Cross-user assignment is possible at the database level, so ownership checks
    belong in the service layer. Revisit only with an explicit decision.
13. **`sqlc` generated model sharing across packages is known, pre-existing
    behavior and is not being changed.** Every target uses
    `schema: "migrations"`, so each generated `models.go` contains a struct for
    every table, not just the ones its queries use. This predates the Saved Items
    work (the committed files already showed it for 13 tables). An
    `omit_unused_structs` option exists in sqlc v1.31.1 and was verified to work,
    but enabling it would touch all existing generated files and is a repo-wide
    convention change. Deliberately left alone.

## Next Step

Per the roadmap in `documentation/product.md`, the next work is **step 2:
Collections + basic search** — Phase B.

A note on phase terminology, since it is easy to misread:

- The roadmap lists "Saved item detail" and "Delete a saved item" under Phase B,
  but both were built during Phase A because the core loop needed them. They are
  done, not pending.
- `product.md` has been updated to reflect this, so the genuinely remaining Phase
  B work is **Collections** and **basic search**.
- `collection_id` was deliberately omitted from the Phase A migration for this
  reason, and was added later by migration 000022, which has been applied.

The schema and the user lifecycle are both in place: the `collections` table,
the `saved_items.collection_id` relationship, the `enrichment_*` columns, and
the Unsorted-per-user invariant. `internal/database/baseline/schema.sql` matches
migration 000022. Registration and Saved Item compatibility work is finished —
do not redo it.

The next implementation slice is the **Collections repository, service, and
API** built on the existing schema. Basic search follows it.

Before starting: confirm scope with the user. Nothing about the Collections API
is pre-approved — do not invent additional features such as reminders, price
tracking, comparison, or AI.

## Resume Instructions

1. Read `AGENTS.md` — it is the authoritative operating guide.
2. Read `documentation/product.md` for scope and direction.
3. Read this file for current state and decisions.
4. Run `git status` and `git diff` before changing anything. Saved Items,
   registration, and the Unsorted invariant are all **committed**
   (`0fd199f`, then `fix: restore saved item creation after collections schema`,
   then `feat: initialize unsorted collection on registration`), so the working
   tree starts clean.
5. Inspect `internal/api/saved_item/` and `internal/api/auth/registration/`
   before writing code. Do not reimplement existing endpoints or the Unsorted
   creation.
6. Re-verify the tree compiles before assuming a clean start:
   `go build ./... && go vet ./... && go test ./...`
7. Avoid unrelated refactors. If something looks wrong, report it rather than
   fixing it as a side effect.
8. Never edit files in `migrations/` or `internal/api/**/generated/`. Change
   `queries.sql` and regenerate, or change the feature code.
9. Do not apply migrations or commit unless explicitly instructed.
