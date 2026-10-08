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

-- name: GetCollectionByIDForUser :one
-- Reads one collection of the authenticated user.
--
-- Matching id AND user_id means a collection that does not exist and one owned by
-- another user both return no row, so both surface as the same not found error and
-- the delete endpoint never discloses whether an id exists for somebody else.
--
-- No row is taken FOR UPDATE. That is deliberate and it is what keeps this
-- operation deadlock-free against the move endpoint: PutSavedItemIntoUserCollection
-- locks a saved item first and then touches a collection, so taking the
-- collection's lock here, before the child rows, would invert that order and two
-- requests could wait on each other. Emptiness is not decided by this read anyway,
-- so nothing here needs to be held. The foreign key's own row locks serialise the
-- part that does.
SELECT
    id,
    user_id,
    name,
    type,
    system_key,
    created_at,
    updated_at
FROM collections
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: DeleteCollectionByIDForUser :one
-- Removes one collection of the authenticated user.
--
-- This is the last step of the delete and it is still protected by
-- saved_items_collection_id_fkey, which is ON DELETE RESTRICT. By this point the
-- operation has either deleted or moved every saved item out of this collection,
-- so the constraint has nothing left to block. If something was filed into the
-- collection after that and before this statement, the constraint refuses and the
-- whole transaction rolls back, leaving the collection and its items as they were.
-- The database is the final authority here rather than an application check, so
-- there is no window for a race to slip through.
--
-- collection_id alone would not establish ownership, so user_id is matched too.
DELETE FROM collections
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING id;

-- name: DeleteSavedItemsInCollection :exec
-- Deletes every saved item of the authenticated user that is filed in this
-- collection. This is the 'delete' disposition of a collection deletion.
--
-- Matched by collection_id AND user_id so no item belonging to anybody else can be
-- reached. Migration 000025's composite foreign key already guarantees that items
-- filed in this collection belong to this collection's owner, so the user_id
-- predicate cannot change the outcome; it states the intent.
--
-- saved_items_collection_id_idx is the index behind this, the same one that backs
-- the ON DELETE RESTRICT check.
DELETE FROM saved_items
WHERE collection_id = sqlc.arg(collection_id)
  AND user_id = sqlc.arg(user_id);

-- name: MoveSavedItemsToCollection :exec
-- Reassigns every saved item of the authenticated user that is filed in the source
-- collection to an already-validated target collection. This is the 'move'
-- disposition of a collection deletion, and it exists only to give the items
-- somewhere to go while their collection is removed. It is not a general batch
-- move: there is no endpoint that calls it on its own, and no source and target
-- reach it without a collection deletion having already decided to remove the
-- source.
--
-- Matched by collection_id AND user_id so the write cannot touch another user's
-- items. The target is not re-read here: it was resolved and verified in this same
-- transaction, and re-resolving it per row would be a second opinion that could
-- disagree with the one the decision was made on.
--
-- Only collection_id is written. enrichment_status, last_enriched_at, description
-- and image_url are untouched, so removing a collection never disturbs what
-- enrichment recorded. The saved_items.updated_at trigger fires for each row,
-- which is intended: a move is a real change to that item.
UPDATE saved_items
SET collection_id = sqlc.arg(target_collection_id)
WHERE collection_id = sqlc.arg(collection_id)
  AND user_id = sqlc.arg(user_id);

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