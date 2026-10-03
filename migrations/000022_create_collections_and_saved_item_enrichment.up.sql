-- Phase B: collections and saved item enrichment state.
--
-- Adds the collections table, the saved_items -> collections relationship, and
-- the enrichment bookkeeping columns on saved_items. This migration only
-- prepares the schema: the collection APIs, the assignment behavior and the
-- background enrichment worker are later phases and are not implemented here.
--
-- saved_items.collection_id is added nullable, backfilled to each owner's
-- Unsorted collection, and only then promoted to NOT NULL, so the migration is
-- safe against databases that already contain saved items.

CREATE TABLE collections (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    name TEXT NOT NULL,

    type TEXT NOT NULL,

    system_key TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- type is plain TEXT plus a CHECK, never a PostgreSQL ENUM, so adding a
    -- collection type later is an ordinary constraint change.
    CONSTRAINT collections_type_check
        CHECK (type IN ('system', 'user')),

    -- A system collection is identified by system_key, never by its display
    -- name: system collections must carry a key, user collections must not.
    CONSTRAINT collections_system_key_check
        CHECK (
            (type = 'system' AND system_key IS NOT NULL)
            OR (type = 'user' AND system_key IS NULL)
        )
);

-- Collection names are unique per user, compared case-insensitively and with
-- surrounding whitespace trimmed, so "Wishlist", "wishlist", "WISHLIST" and
-- " Wishlist " all collide. This reserves the display name for system
-- collections too, so a user cannot shadow the "YouTube" system collection.
-- A unique index is used rather than a column DEFAULT because the uniqueness
-- is over an expression.
CREATE UNIQUE INDEX collections_user_name_unique
    ON collections (user_id, lower(btrim(name)));

-- At most one system collection per system_key per user. Partial, so the NULL
-- system_key of user collections never conflicts.
CREATE UNIQUE INDEX collections_system_key_unique
    ON collections (user_id, system_key)
    WHERE system_key IS NOT NULL;

CREATE INDEX collections_user_idx
    ON collections (user_id);

CREATE TRIGGER collections_set_updated_at
BEFORE UPDATE ON collections
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

-- Every user owns exactly one permanent Unsorted system collection. It is
-- created for every existing user, not only for users who already have saved
-- items, because the invariant is per user and new saved items land here.
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key
)
SELECT
    u.id,
    'Unsorted',
    'system',
    'unsorted'
FROM users u;

ALTER TABLE saved_items
ADD COLUMN collection_id UUID;

-- Named explicitly: repository code will later translate this known constraint
-- violation into an application error, and ON DELETE RESTRICT is what prevents
-- a collection from being removed while it still holds saved items.
ALTER TABLE saved_items
ADD CONSTRAINT saved_items_collection_id_fkey
    FOREIGN KEY (collection_id)
    REFERENCES collections(id)
    ON DELETE RESTRICT;

-- Saved items saved before collections existed belong to their owner's Unsorted
-- collection. This is a real row change, so the pre-existing saved_items
-- updated_at trigger fires and advances updated_at for these rows.
UPDATE saved_items si
SET collection_id = c.id
FROM collections c
WHERE c.user_id = si.user_id
  AND c.system_key = 'unsorted';

-- Only now that every row is backfilled is NOT NULL safe to enforce.
ALTER TABLE saved_items
ALTER COLUMN collection_id SET NOT NULL;

ALTER TABLE saved_items
ADD COLUMN enrichment_status TEXT NOT NULL DEFAULT 'pending',
ADD COLUMN enrichment_started_at TIMESTAMPTZ,
ADD COLUMN last_enriched_at TIMESTAMPTZ;

-- enrichment_status is TEXT plus a CHECK, never a PostgreSQL ENUM.
ALTER TABLE saved_items
ADD CONSTRAINT saved_items_enrichment_status_check
    CHECK (
        enrichment_status IN (
            'pending',
            'processing',
            'completed',
            'failed'
        )
    );

-- Backs the ON DELETE RESTRICT check and per-collection listing.
CREATE INDEX saved_items_collection_id_idx
    ON saved_items (collection_id);