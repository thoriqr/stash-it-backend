-- name: CreateSavedItem :one
-- Writes one saved item and reports the row as persisted.
--
-- collection_id and enrichment_status are returned even though the create response
-- reports only a minimal representation. They come back from this same INSERT, so
-- reading them costs no additional statement: the alternative would be a second
-- query per save purely to learn what the database already wrote. enrichment_status
-- is the column's DEFAULT, so reporting it here reports what was actually stored
-- rather than a constant this code assumes.
--
-- domain is derived locally from the submitted URL and platform and title are left
-- NULL for enrichment to fill in, so none of the metadata columns carry anything
-- meaningful yet. That is why the create response does not report them.
--
-- description and image_url are returned even though this INSERT never sets them.
-- They are always NULL here, and returning them keeps the projection genuinely
-- complete: the shared SavedItem type is documented as the whole row, and filling
-- those two fields from a literal rather than from the row would have the mapper
-- asserting something this statement never read.
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
    description,
    image_url,
    collection_id,
    enrichment_status,
    last_enriched_at,
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
-- Reads one saved item of the authenticated user, with the collection it is filed in.
--
-- The detail response reports the full saved item plus the collection's id and name,
-- and both come from this one statement. Fetching the collection separately would be
-- a second round trip to read a row this join already had in hand.
--
-- The join is owner-scoped on the collection as well as on the saved item. Migration
-- 000025's composite foreign key over (collection_id, user_id) already guarantees an
-- item's collection belongs to the item's owner, so c.user_id = si.user_id cannot
-- change the result; it states the intent rather than leaving it to the constraint,
-- and it means no other user's collection is reachable even if that constraint were
-- ever relaxed. An INNER JOIN is therefore correct and not lossy: every saved item has
-- a NOT NULL collection_id that references an existing row.
--
-- Matching si.id AND si.user_id means an item that does not exist and an item owned by
-- another user both return no row, so both surface as the same not found error and the
-- endpoint never discloses whether an ID exists.
SELECT
    si.id,
    si.user_id,
    si.url,
    si.domain,
    si.platform,
    si.title,
    si.description,
    si.image_url,
    si.collection_id,
    si.enrichment_status,
    si.last_enriched_at,
    si.created_at,
    si.updated_at,
    c.name AS collection_name
FROM saved_items si
JOIN collections c
  ON c.id = si.collection_id
 AND c.user_id = si.user_id
WHERE si.id = sqlc.arg(id)
  AND si.user_id = sqlc.arg(user_id);

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

-- name: ListSavedItemsFirst :many
-- First page of the authenticated user's recent saved items, newest first.
--
-- This is the recent history: every saved item the user owns, across every
-- collection they own, including Unsorted and collections automatic organization
-- created. It is deliberately NOT a per-collection view. The inbox is a
-- chronological record of what was saved, so an item stays in it after it has been
-- filed into a collection of the user's own, and it says which collection that is.
--
-- No filter on enrichment_status or on any organization outcome. A pending item, an
-- item whose page could not be read and an item that has been organized are all
-- ordinary states of a saved item and all appear here; the item exists and belongs
-- to the caller, so "its metadata has not arrived" is a fact about the item rather
-- than a reason to withhold it. The item's enrichment_status is reported, so a
-- client can tell the three apart without anything being hidden.
--
-- collections is joined for the collection name, exactly as GetSavedItemByIDForUser
-- joins it for one item: the listing spans collections, so a client cannot learn
-- from the request which collection any given row is in, and reading the names
-- separately would be a second round trip per page for a row the join already had in
-- hand. The join is owner-scoped on both sides and is INNER because every saved item
-- has a NOT NULL collection_id referencing an existing row.
--
-- created_at DESC, id DESC is a total order, and the tie-break is not optional.
-- created_at defaults to NOW(), which is transaction_timestamp() and therefore
-- constant within a transaction, so rows inserted together genuinely share a
-- timestamp and a page boundary could otherwise skip or repeat an item.
-- saved_items.id is uuidv7() with no overriding trigger, so it is unique and
-- monotonic in creation order.
--
-- There is no sort parameter. A history is read newest first or not at all; offering
-- orders would answer a question this resource does not pose.
--
-- This is a read. It writes nothing and triggers nothing: enrichment is scheduled by
-- saving a URL and by POST /saved-items/{id}/enrich, and looking at the history is
-- not a reason to fetch anything.
--
-- saved_items_user_created_at_idx ON (user_id, created_at DESC) is the index behind
-- this ordering, so the leading prefix is walked rather than sorted.
--
-- Callers request limit + 1 rows so has_more is exact rather than inferred.
SELECT
    si.id,
    si.user_id,
    si.url,
    si.domain,
    si.platform,
    si.title,
    si.description,
    si.image_url,
    si.collection_id,
    si.enrichment_status,
    si.last_enriched_at,
    si.created_at,
    si.updated_at,
    c.name AS collection_name
FROM saved_items si
JOIN collections c
    ON c.id = si.collection_id
   AND c.user_id = si.user_id
WHERE si.user_id = sqlc.arg(user_id)
ORDER BY
    si.created_at DESC,
    si.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListSavedItemsAfterRow :many
-- Recent saved items, newest first, after the position a cursor carries.
--
-- The row comparison describes the same total order as the ORDER BY above it, which
-- is what makes this keyset pagination rather than an offset in disguise: DESC
-- ordering resumes with <. Row comparison short-circuits like the expanded form, so
-- it needs only the leading index column and resolves ties without an OR chain.
--
-- The casts are load-bearing. Without them sqlc types every row-comparison parameter
-- from the row's leftmost element, which would give cursor_id a timestamptz type and
-- let a timestamp be passed where a UUID belongs.
--
-- The stored position comes from the cursor, never from a lookup of the row it came
-- from. Nothing here reads saved_items by id, so a cursor keeps working after its
-- anchor item is deleted: it resumes from the position it recorded. That is the
-- property offset pagination cannot offer, since deleting an item shifts everything
-- after it and makes a client receive a duplicate.
SELECT
    si.id,
    si.user_id,
    si.url,
    si.domain,
    si.platform,
    si.title,
    si.description,
    si.image_url,
    si.collection_id,
    si.enrichment_status,
    si.last_enriched_at,
    si.created_at,
    si.updated_at,
    c.name AS collection_name
FROM saved_items si
JOIN collections c
    ON c.id = si.collection_id
   AND c.user_id = si.user_id
WHERE si.user_id = sqlc.arg(user_id)
  AND (si.created_at, si.id) < (
      sqlc.arg(cursor_value)::timestamptz,
      sqlc.arg(cursor_id)::uuid
  )
ORDER BY
    si.created_at DESC,
    si.id DESC
LIMIT sqlc.arg(page_limit);

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
