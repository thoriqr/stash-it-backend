-- name: LockSavedItemForUser :one
-- Locks the saved item and proves ownership in one statement. The row is
-- projected in full, plus collection_id, so the caller can compare the item's
-- current collection against the target without a second read.
--
-- Matching id AND user_id means an item that does not exist and an item owned
-- by another user both return no row, so both surface as the same not found
-- error and the operation never discloses whether an id exists.
SELECT
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    created_at,
    updated_at,
    collection_id
FROM saved_items
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: CreateUserCollection :one
-- Creates a user collection: type = 'user' and system_key = NULL, as required by
-- collections_system_key_check. Collections of type 'user' only ever come into
-- existence through this query, as part of putting a saved item into them.
--
-- collections_user_name_unique is an expression index over
-- (user_id, lower(btrim(name))), so the conflict target is written as an
-- expression. DO NOTHING returns no row when the name is already taken for this
-- user, which is how the caller learns it must fall back to a lookup. It is
-- deliberately not ON CONFLICT DO UPDATE: collections has an updated_at trigger,
-- so an update would write to a pre-existing collection just to read it.
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
ON CONFLICT (user_id, lower(btrim(name))) DO NOTHING
RETURNING
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at;

-- name: GetUserCollectionByNameForUser :one
-- Resolves an existing user collection by name, compared the same way the unique
-- index compares it: case-insensitively and with surrounding whitespace
-- ignored. This runs as a separate statement after CreateUserCollection returned
-- no row, so it takes a fresh snapshot under READ COMMITTED and therefore sees a
-- collection that a concurrent transaction committed in the meantime.
--
-- type = 'user' is deliberate. collections_user_name_unique also covers system
-- collections, and its display names are reserved precisely so a user cannot
-- shadow them. Returning no row here therefore means the name is held by a system
-- collection, which the caller reports as a reserved name rather than silently
-- filing the saved item into a system collection the user only named by accident.
SELECT
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND type = 'user'
  AND lower(btrim(name)) = lower(btrim(sqlc.arg(name)));

-- name: MoveSavedItemToCollection :one
-- Reassigns the saved item to an already-resolved collection. Matching id AND
-- user_id keeps ownership enforced on the write itself, not only on the read that
-- preceded it.
--
-- enrichment_status, last_enriched_at, description and image_url are neither
-- selected nor written. Moving an item has no effect on enrichment, whatever
-- state enrichment is in.
--
-- The saved_items.updated_at trigger fires here, which is intended: a move is a
-- real change to the item. When the item is already in the target collection the
-- caller skips this query entirely, so a no-op move does not touch updated_at.
UPDATE saved_items
SET collection_id = sqlc.arg(collection_id)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING
    id,
    user_id,
    url,
    domain,
    platform,
    title,
    created_at,
    updated_at,
    collection_id;