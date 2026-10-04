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
-- Sets the full enrichment state on an existing saved item so a test can seed a
-- non-default state and then assert it is untouched by a move.
UPDATE saved_items
SET enrichment_status = sqlc.arg(enrichment_status),
    enrichment_started_at = sqlc.narg(enrichment_started_at),
    last_enriched_at = sqlc.narg(last_enriched_at)
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
    enrichment_started_at,
    last_enriched_at,
    updated_at
FROM saved_items
WHERE id = sqlc.arg(id);