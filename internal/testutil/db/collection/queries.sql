-- name: TruncateCollectionData :exec
TRUNCATE TABLE
    saved_items,
    collections,
    users
CASCADE;

-- name: CreateCollectionUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
) VALUES (
    sqlc.arg(email),
    sqlc.arg(display_name),
    NOW()
)
RETURNING id;

-- name: CreateUnsortedCollection :one
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key
) VALUES (
    sqlc.arg(user_id),
    'Unsorted',
    'system',
    'unsorted'
)
RETURNING id;

-- name: CreateTestUserCollection :one
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    'user',
    NULL
)
RETURNING
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at;

-- name: CreateTestSavedItemInCollection :one
-- enrichment_status is settable so tests can prove that putting a saved item
-- into a collection is independent of enrichment state. It is left to the
-- column default when not supplied.
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    platform,
    title,
    collection_id,
    enrichment_status
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(platform),
    sqlc.arg(title),
    sqlc.arg(collection_id),
    COALESCE(sqlc.narg(enrichment_status), 'pending')
)
RETURNING id;

-- name: SetTestSavedItemEnrichmentState :exec
-- Sets the enrichment state on an existing saved item so a test can seed a
-- non-default state and then assert it is untouched by a move.
UPDATE saved_items
SET enrichment_status = sqlc.arg(enrichment_status),
    last_enriched_at = sqlc.narg(last_enriched_at)
WHERE id = sqlc.arg(id);

-- name: SetTestSavedItemEnrichmentStateAt :exec
-- Sets the full enrichment state on an existing saved item, including created_at.
--
-- A listing needs items in every enrichment state at once, and it orders by
-- created_at, so a test has to control both. SetTestSavedItemEnrichmentState covers
-- the enrichment columns but leaves created_at alone, which cannot place a row at a
-- chosen position in the ordering or create rows that genuinely tie.
UPDATE saved_items
SET enrichment_status = sqlc.arg(enrichment_status),
    last_enriched_at = sqlc.narg(last_enriched_at),
    title = sqlc.narg(title),
    description = sqlc.narg(description),
    image_url = sqlc.narg(image_url),
    created_at = sqlc.arg(created_at)
WHERE id = sqlc.arg(id);

-- name: GetCollectionState :one
SELECT
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at
FROM collections
WHERE id = sqlc.arg(id);

-- name: CreateTestCollectionAt :one
-- Creates one collection with an explicit created_at.
--
-- Collections default to NOW(), which is transaction_timestamp() and therefore
-- constant inside one transaction, so several rows inserted together share a
-- timestamp. migrations/000022 inserts every user's Unsorted collection in exactly
-- that shape, so equal timestamps are a real state rather than a contrived one, and
-- the listing has to order them without skipping or repeating a row across a page
-- boundary. Setting the value explicitly is what lets a test place rows at chosen
-- positions in an ordering, and what lets a test create rows that genuinely tie.
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key,
    created_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    CASE WHEN sqlc.narg(system_key)::text IS NULL THEN 'user' ELSE 'system' END,
    sqlc.narg(system_key)::text,
    sqlc.arg(created_at)
)
RETURNING
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at;

-- name: CreateTestSystemCollection :one
-- A collection as automatic organization creates it: type = 'system' with a key
-- that is not Unsorted. Seeded directly so tests can prove that a collection's
-- type does not restrict whether the user may delete it.
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    'system',
    sqlc.arg(system_key)
)
RETURNING id;

-- name: CountSavedItemsInCollection :one
-- Counts what is filed in a collection, so a test can tell "still there" from
-- "emptied" from "removed" without inferring any of it from the operation's own
-- response.
SELECT COUNT(*)
FROM saved_items
WHERE collection_id = sqlc.arg(collection_id);

-- name: CountSavedItemsInCollectionForUser :one
-- The same count scoped by user. Used where a test must prove one user's items
-- were not reachable through another user's collection.
SELECT COUNT(*)
FROM saved_items
WHERE collection_id = sqlc.arg(collection_id)
  AND user_id = sqlc.arg(user_id);

-- name: ListSavedItemCollectionsInCollection :many
-- Every distinct collection a set of saved items is filed in. Used to assert that
-- a batch move put all of a collection's items somewhere, rather than asserting
-- only that the source is empty.
SELECT DISTINCT collection_id
FROM saved_items
WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CountCollectionsNamedForUser :one
-- Counts by the same expression the unique index uses, so a test can prove that
-- normalization collisions produced exactly one row.
SELECT COUNT(*)
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND lower(btrim(name)) = lower(btrim(sqlc.arg(name)));

-- name: CountSystemCollectionsByKey :one
SELECT COUNT(*)
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND system_key = sqlc.arg(system_key);

-- name: GetSavedItemCollectionState :one
-- The full enrichment state is projected on purpose: tests assert it is
-- byte-identical before and after a move.
SELECT
    id,
    user_id,
    collection_id,
    enrichment_status,
    last_enriched_at,
    updated_at
FROM saved_items
WHERE id = sqlc.arg(id);