-- name: TruncateWorkerOrganizationData :exec
TRUNCATE TABLE
    saved_items,
    collections,
    users
CASCADE;

-- name: CreateWorkerOrganizationUser :one
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

-- name: CreateUnsortedCollectionForWorkerOrganization :one
-- The Unsorted system collection every user has, and the only collection a new
-- saved item is ever created into by these tests.
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

-- name: CreateUserNamedCollectionForWorkerOrganization :one
-- A collection the user named themselves, so a test can prove an automatically
-- organized item is filed into one rather than having a second collection
-- created beside it.
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

-- name: CreateWorkerOrganizationSavedItem :one
-- A saved item in the state a background enrichment task finds after a successful
-- enrichment: completed, carrying a platform, and filed in Unsorted.
--
-- The enrichment columns are set explicitly rather than left to defaults because
-- the item's platform is the input to the decision under test, not an incidental
-- starting value.
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    platform,
    enrichment_status,
    last_enriched_at,
    collection_id
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.arg(domain),
    sqlc.arg(platform),
    sqlc.arg(enrichment_status),
    NOW(),
    sqlc.arg(collection_id)
)
RETURNING id;

-- name: DeleteSavedItemForWorkerOrganization :exec
-- Removes an item outright, standing in for a user deleting it between the
-- enrichment that scheduled its organization task and that task being consumed.
DELETE FROM saved_items
WHERE id = sqlc.arg(id);

-- name: MoveSavedItemForWorkerOrganization :exec
-- Files an item into a collection the way the collection API does, standing in
-- for a user action that happens while an organization task is waiting.
UPDATE saved_items
SET collection_id = sqlc.arg(collection_id)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: GetWorkerOrganizationItemState :one
-- The full state an organization decision is made on, projected so a test can
-- assert every relevant column afterwards.
SELECT
    id,
    user_id,
    url,
    platform,
    enrichment_status,
    last_enriched_at,
    collection_id,
    updated_at
FROM saved_items
WHERE id = sqlc.arg(id);

-- name: GetWorkerOrganizationCollection :one
SELECT
    id,
    user_id,
    name,
    type,
    system_key
FROM collections
WHERE id = sqlc.arg(id);

-- name: ListWorkerOrganizationCollectionsForUser :many
-- Every collection the user owns, so a test can assert how many collections exist
-- and not only which one the item ended up in. A duplicate created by a race
-- would be visible here and nowhere else.
SELECT
    id,
    user_id,
    name,
    type,
    system_key
FROM collections
WHERE user_id = sqlc.arg(user_id)
ORDER BY name;

-- name: ListWorkerOrganizationSavedItemCollections :many
-- The names of the collections holding this user's saved items, so a test can
-- assert that no second collection was created holding the same item.
SELECT
    c.id,
    c.name,
    c.type,
    c.system_key
FROM saved_items si
JOIN collections c ON c.id = si.collection_id
WHERE si.user_id = sqlc.arg(user_id)
ORDER BY c.name;