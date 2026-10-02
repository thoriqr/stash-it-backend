-- name: TruncateSavedItemData :exec
TRUNCATE TABLE
    saved_items,
    users
CASCADE;

-- name: CreateSavedItemUser :one
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

-- name: CreateTestSavedItem :one
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    platform,
    title,
    created_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(platform),
    sqlc.arg(title),
    sqlc.arg(created_at)
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

-- name: GetSavedItemState :one
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
WHERE id = sqlc.arg(id);

-- name: ListSavedItemsForUser :many
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
ORDER BY created_at DESC;

-- name: CountSavedItemsForUser :one
SELECT COUNT(*)
FROM saved_items
WHERE user_id = sqlc.arg(user_id);
