-- name: TruncateSavedItemData :exec
TRUNCATE TABLE
    saved_items,
    collections,
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
-- A collection the user created. type = 'user' and system_key = NULL, which
-- collections_system_key_check requires. This is the shape that a NULL-safe
-- system_key guard has to handle, since no user collection carries a key.
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

-- name: CreateTestSystemCollection :one
-- A collection created by automatic organization: type = 'system' with a system
-- key that is not Unsorted. Seeded directly so tests can prove that a system
-- collection is one the user may delete exactly like one of their own.
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

-- name: CreateTestSavedItem :one
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    platform,
    title,
    collection_id,
    created_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(platform),
    sqlc.arg(title),
    sqlc.arg(collection_id),
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
-- Reads one saved item's stored state.
--
-- collection_id and enrichment_status are selected so a test can assert that an
-- endpoint changed neither. Every endpoint that reads a saved item has to leave the
-- row alone, and without those two columns the only way to check that was to infer
-- it from the response, which is the thing under test.
SELECT
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    collection_id,
    enrichment_status,
    last_enriched_at,
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
