-- name: CreateSavedItem :one
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    platform,
    title,
    collection_id
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(platform),
    sqlc.arg(title),
    sqlc.arg(collection_id)
)
RETURNING
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    created_at,
    updated_at;

-- name: GetUnsortedCollectionByUser :one
-- Resolves the authenticated user's Unsorted collection. system_key is the
-- stable identity of a system collection, so the display name is never used to
-- find it.
SELECT
    id
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND type = 'system'
  AND system_key = 'unsorted'
LIMIT 1;

-- name: GetSavedItemByIDForUser :one
SELECT
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    created_at,
    updated_at
FROM saved_items
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: DeleteSavedItemByIDForUser :one
DELETE FROM saved_items
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING id;

-- name: ListSavedItems :many
SELECT
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    created_at,
    updated_at
FROM saved_items
WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountSavedItems :one
SELECT COUNT(*)
FROM saved_items
WHERE user_id = sqlc.arg(user_id);
