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

-- name: ListCollectionsNewestFirst :many
-- First page of the user's collections, newest first, with Unsorted pinned ahead of
-- everything else.
--
-- The pinned row is a rank rather than a filter. system_key = 'unsorted' gives that
-- collection 0 and every other collection 1, so it sorts first under whichever
-- ordering the caller asked for. It is identified by system_key only: its display
-- name is free text and its type says who created a collection, not what it is, so
-- neither may decide this.
--
-- There is deliberately no type filter. A collection automatic organization created
-- belongs to one user through collections.user_id and belongs in this list exactly
-- like one they named themselves, which is the same reasoning SearchCollections
-- documents.
--
-- created_at DESC, id DESC is a total order. The tie-break is not optional:
-- created_at defaults to NOW(), which is transaction_timestamp() and therefore
-- constant within a transaction, so one INSERT can produce many rows sharing a
-- timestamp. migrations/000022 inserts every user's Unsorted collection in a single
-- statement, so ties are guaranteed rather than hypothetical, and without the id a
-- page boundary could skip or repeat a row. collections.id is uuidv7() and no
-- trigger overrides it, so it is unique and monotonic in creation order.
--
-- Callers request limit + 1 rows so has_more is exact rather than inferred.
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
ORDER BY
    (CASE WHEN system_key = 'unsorted' THEN 0 ELSE 1 END) ASC,
    created_at DESC,
    id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsNewestAfterUnsorted :many
-- Newest-first collections with Unsorted excluded, from the top of the ordering.
--
-- This is the page that follows one holding only Unsorted, so it takes no position
-- parameter at all. There is deliberately nothing for a caller to pass: a zero
-- timestamp would build a predicate against '-0001-01-01' and return the wrong rows,
-- so keeping this a separate statement makes that mistake impossible rather than
-- something to guard at runtime.
--
-- Unsorted is excluded with IS DISTINCT FROM, which is load-bearing twice. It
-- removes the pinned row so it is never repeated, and it is NULL-safe, so user
-- collections whose system_key is NULL are not dropped along with it.
-- collections_system_key_check gives every type = 'user' collection a NULL key, so a
-- plain <> would evaluate to NULL for all of them and this query would return system
-- collections only.
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
  AND system_key IS DISTINCT FROM 'unsorted'
ORDER BY
    created_at DESC,
    id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsNewestAfterRow :many
-- Newest-first collections after the position a cursor carries.
--
-- The row comparison describes the same total order as the ORDER BY, which is what
-- makes this keyset pagination rather than an offset in disguise: DESC ordering
-- resumes with <. Row comparison short-circuits like the expanded form, so it needs
-- only the leading index column and resolves ties without an OR chain.
--
-- The stored position comes from the cursor, never from a lookup of the row it came
-- from. Nothing here reads collections by id, so a cursor keeps working after its
-- anchor row is deleted: it resumes from the position it recorded. That is the
-- property offset pagination cannot offer, since deleting a row shifts every later
-- row down a slot and makes a client receive a duplicate.
--
-- cursor_value is the row's created_at and cursor_id its id, both as read from the
-- database rather than re-derived.
--
-- The casts are load-bearing, not decoration. Without them sqlc infers the type of
-- every row-comparison parameter from the row's leftmost element, which would give
-- cursor_id a timestamptz type and let a caller pass a timestamp where a UUID
-- belongs. Naming each type explicitly is what keeps the generated parameters honest.
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
  AND system_key IS DISTINCT FROM 'unsorted'
  AND (created_at, id) < (sqlc.arg(cursor_value)::timestamptz, sqlc.arg(cursor_id)::uuid)
ORDER BY
    created_at DESC,
    id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsOldestFirst :many
-- First page of the user's collections, oldest first, with Unsorted pinned ahead of
-- everything else.
--
-- The pinned rank and the absence of a type filter are the same as in the newest
-- variant, and so is the reasoning: system_key identifies Unsorted, and a
-- collection's type never restricts what the user may see or delete.
--
-- created_at ASC, id ASC keeps the ordering total for the same reason DESC does.
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
ORDER BY
    (CASE WHEN system_key = 'unsorted' THEN 0 ELSE 1 END) ASC,
    created_at ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsOldestAfterUnsorted :many
-- Oldest-first collections with Unsorted excluded, from the top of the ordering.
--
-- See ListCollectionsNewestAfterUnsorted: IS DISTINCT FROM both removes the pinned
-- row and keeps the NULL system_key of user collections, and there is no position
-- parameter because this page always starts at the top of the ordering.
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
  AND system_key IS DISTINCT FROM 'unsorted'
ORDER BY
    created_at ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsOldestAfterRow :many
-- Oldest-first collections after the position a cursor carries.
--
-- ASC ordering resumes with >, which is the same ordering the ORDER BY describes.
-- The anchor row is not read, so the position survives that row being deleted.
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
  AND system_key IS DISTINCT FROM 'unsorted'
  AND (created_at, id) > (sqlc.arg(cursor_value)::timestamptz, sqlc.arg(cursor_id)::uuid)
ORDER BY
    created_at ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsByNameFirst :many
-- First page of the user's collections, alphabetically ascending, with Unsorted
-- pinned ahead of everything else.
--
-- lower(btrim(name)) is the same expression collections_user_name_unique is built
-- on, so sort order and name uniqueness can never disagree about what it means for
-- two names to be the same. Sorting on the raw display name instead would let the
-- ordering and the constraint each hold a different opinion about a name, which is
-- the disagreement collection/service.go explicitly avoids when storing one.
--
-- The tie-break on id is belt and braces: the unique index already means two
-- collections in one user cannot share a normalized name. It is kept so the ordering
-- is total regardless of collation, since lower(btrim(name)) is not locale-stable
-- for equal keys.
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
ORDER BY
    (CASE WHEN system_key = 'unsorted' THEN 0 ELSE 1 END) ASC,
    lower(btrim(name)) ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsByNameAfterUnsorted :many
-- Alphabetically ascending collections with Unsorted excluded, from the top.
--
-- See the other AfterUnsorted variants for why the exclusion is IS DISTINCT FROM
-- and why this page takes no position parameter.
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
  AND system_key IS DISTINCT FROM 'unsorted'
ORDER BY
    lower(btrim(name)) ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCollectionsByNameAfterRow :many
-- Alphabetically ascending collections after the position a cursor carries.
--
-- cursor_value must already be the normalized name, lower(btrim(name)), exactly as
-- the ORDER BY and the unique index compute it. Passing the raw display name would
-- compare against a value the index never produced and quietly return the wrong
-- rows, so the service normalizes before it encodes.
--
-- The casts are load-bearing: without them sqlc would type cursor_id from the row's
-- leftmost element. See ListCollectionsNewestAfterRow.
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
  AND system_key IS DISTINCT FROM 'unsorted'
  AND (lower(btrim(name)), id) > (sqlc.arg(cursor_value)::text, sqlc.arg(cursor_id)::uuid)
ORDER BY
    lower(btrim(name)) ASC,
    id ASC
LIMIT sqlc.arg(page_limit);

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