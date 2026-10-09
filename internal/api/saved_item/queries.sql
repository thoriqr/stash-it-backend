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
-- Deletes one saved item of the authenticated user and reports the collection it
-- was in.
--
-- collection_id is returned because this statement is the only place that can
-- observe it for a row that no longer exists afterwards: a read before the delete
-- could already be stale, and a read after it finds nothing. RETURNING gives the
-- deleted row's own value, and the DELETE holds an exclusive lock on that row
-- until this statement ends, so a concurrent move of the same item either
-- committed before this value was taken or waits for this transaction to finish.
-- That is why no separate locking read is needed to pin the collection.
--
-- Matching id AND user_id means an item that does not exist and an item owned by
-- another user both return no row, so both surface as the same not found error
-- and the endpoint never discloses whether an ID exists.
DELETE FROM saved_items
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING id, collection_id;

-- name: CountSavedItemsInCollection :one
-- Counts what the delete left behind in the collection the saved item was in.
--
-- This runs AFTER the delete, never before it, so it cannot count the row that
-- was just removed and the collection is reported as empty exactly when the
-- deleted item was the last one it held. Counting before the delete would answer
-- a different question and would be wrong whenever anything else changed in
-- between.
--
-- collection_id AND user_id repeats the scoping the delete used. Migration
-- 000025's composite foreign key already guarantees that every saved item filed
-- in a collection belongs to that collection's owner, so this predicate cannot
-- change the count; it states the intent rather than leaving it to the
-- constraint.
--
-- saved_items_collection_id_idx is the index behind this count. Migration 000022
-- created it for the ON DELETE RESTRICT check, which is the same lookup.
SELECT COUNT(*)
FROM saved_items
WHERE collection_id = sqlc.arg(collection_id)
  AND user_id = sqlc.arg(user_id);

-- name: GetCollectionSystemKeyForUser :one
-- Reads the stable identity of a collection owned by the authenticated user.
--
-- system_key is what identifies Unsorted. Its display name is not its identity and
-- its type is not either: 'system' describes who created a collection, not what it
-- is, and a collection of either type is one the user may delete. Only the key
-- decides which single collection is protected.
--
-- A collection that does not exist, or belongs to somebody else, returns no row.
-- The caller reads that as "not Unsorted", which is correct: a collection that is
-- not there is not the one protected collection, and there is nothing left for the
-- caller to offer to delete.
SELECT system_key
FROM collections
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);
