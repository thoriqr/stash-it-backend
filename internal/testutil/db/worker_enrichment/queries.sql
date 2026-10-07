-- name: TruncateWorkerEnrichmentData :exec
TRUNCATE TABLE
    saved_items,
    collections,
    users
CASCADE;

-- name: CreateWorkerEnrichmentUser :one
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

-- name: CreateUnsortedCollectionForWorkerEnrichment :one
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

-- name: CreateTestUserCollectionForWorkerEnrichment :one
-- A user collection, so a test can file an item somewhere other than Unsorted
-- and then prove background enrichment leaves it exactly where it was.
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
RETURNING id;

-- name: CreatePendingSavedItemForWorkerEnrichment :one
-- A saved item in its pre-enrichment state. The enrichment columns are left to
-- their column defaults, which is the state a real save produces and the state a
-- background enrichment task starts from.
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    collection_id
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(collection_id)
)
RETURNING id;

-- name: DeleteSavedItemForWorkerEnrichment :exec
-- Removes an item outright, standing in for a user deleting it between the save
-- and the task being processed.
DELETE FROM saved_items
WHERE id = sqlc.arg(id);

-- name: GetSavedItemWorkerEnrichmentState :one
-- The full enrichment-relevant state of a saved item, projected so a test can
-- assert every column after background enrichment rather than only the status.
SELECT
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    description,
    image_url,
    collection_id,
    enrichment_status,
    last_enriched_at,
    created_at,
    updated_at
FROM saved_items
WHERE id = sqlc.arg(id);
