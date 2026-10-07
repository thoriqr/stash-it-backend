-- name: LockSavedItemForOrganization :one
-- Loads the CURRENT state of the saved item an organization task is about, and
-- locks it for the duration of the operation.
--
-- Everything the task decides on is read here, at execution time, rather than
-- taken from the payload: enrichment_status, platform, and which collection the
-- item is in right now. A task queued before the user moved the item must see
-- the move, so the item was in Unsorted when enrichment finished is a fact about
-- the past and grants nothing. The FOR UPDATE is what makes that reading and the
-- move that follows one decision rather than two that can interleave with a user
-- action.
--
-- The item's collection is joined rather than carried as a bare id, so "is this
-- still Unsorted" is answered by the collection's system_key rather than by
-- comparing against an id read earlier. Unsorted is identified by system_key
-- because that is its stable identity; its display name is not. The join carries
-- user_id on both sides, which migration 000025's composite foreign key already
-- guarantees: an item's collection_id and user_id cannot disagree.
--
-- This reads by id alone, without the user_id the API scopes every read by, for
-- the same reason the enrichment worker's read does: a task names the row it acts
-- on, there is no caller to mislead and no id to disclose, and the job acts on the
-- row rather than on a user. user_id is projected so the caller can verify the
-- task's claim about ownership rather than taking it on trust.
--
-- No row here means the item is gone, which is an ordinary outcome for a task
-- processed later than it was created.
SELECT
    si.id,
    si.user_id,
    si.enrichment_status,
    si.platform,
    si.collection_id,
    c.system_key AS collection_system_key
FROM saved_items si
JOIN collections c
    ON c.id = si.collection_id
   AND c.user_id = si.user_id
WHERE si.id = sqlc.arg(id)
FOR UPDATE OF si;


-- name: GetCollectionByNameForOrganization :one
-- Resolves a collection of this user by display name, compared the same way
-- collections_user_name_unique compares it: case-insensitively, with surrounding
-- whitespace ignored. That index is the definition of "the same name" in this
-- project, and reusing it here is what keeps organization from inventing a second
-- notion of name equality that could disagree with the one the unique constraint
-- enforces.
--
-- No type filter, deliberately, and this is the difference between this statement
-- and the API's collection lookup. The API refuses to file an item into a system
-- collection a user happened to name, because the user was asking for a
-- collection of their own. Organization is not: an automatically created
-- platform collection is type 'system', and refusing to match one would mean the
-- second item saved from the same platform created a second collection.
--
-- A collection of either type is a legitimate target. Reusing one the user named
-- themselves is the point: their naming is theirs to choose, and filing into
-- "youtube" when the platform reads "YouTube" respects that rather than
-- duplicating or renaming it.
SELECT
    id,
    user_id,
    name,
    type,
    system_key
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND lower(btrim(name)) = lower(btrim(sqlc.arg(name)));


-- name: CreateSystemCollectionForOrganization :one
-- Creates the platform collection an item is filed into, as type 'system' with
-- the platform's identity as its system_key, as collections_system_key_check
-- requires.
--
-- 'system' describes where the collection came from, not who owns it. It still
-- belongs to this user through collections.user_id, and migration 000025's
-- composite foreign key is what makes an item's collection_id and user_id have
-- to agree, so a system collection is per-user exactly like a user collection.
--
-- collections_user_name_unique is an expression index over
-- (user_id, lower(btrim(name))), so the conflict target is written as an
-- expression. DO NOTHING returns no row when the name is already taken, which is
-- how the caller learns to fall back to a lookup. This is the constraint doing
-- the work: two organization tasks racing on the same platform cannot both win,
-- because the second blocks on the conflicting index entry and is then told the
-- row exists. Application-side "check then insert" would not be safe, since both
-- tasks would see nothing there.
--
-- Deliberately not ON CONFLICT DO UPDATE: collections has an updated_at trigger,
-- so an update would write to a pre-existing collection purely to read it.
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
ON CONFLICT (user_id, lower(btrim(name))) DO NOTHING
RETURNING
    id,
    user_id,
    name,
    type,
    system_key;


-- name: MoveSavedItemToCollectionForOrganization :one
-- Files the saved item into the already-resolved target collection.
--
-- Matching id AND user_id keeps ownership enforced on the write itself, and it
-- is redundant with the lock taken at the start of the operation by design: the
-- write is stating the same claim twice so that it cannot succeed if the lock's
-- claim were ever wrong.
--
-- collection_id is written here, deliberately, and it is the only column this
-- statement touches. enrichment_status and last_enriched_at are neither selected
-- nor written, so an organization failure or a success can never disturb what
-- enrichment recorded. A failed enrichment stays failed and a completed one stays
-- completed whatever happens here.
UPDATE saved_items
SET collection_id = sqlc.arg(collection_id)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
RETURNING id;