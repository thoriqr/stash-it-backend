-- Enforces that a saved item can only reference a collection owned by the same
-- user.
--
-- saved_items.user_id and collections.user_id were each independently correct,
-- but nothing tied them together: the foreign key referenced collections(id)
-- alone, so the database would have accepted a row whose saved_items.user_id
-- differed from the user_id of the collection it was filed into. Only application
-- code kept that from happening, and only in the queries that were written with
-- the invariant in mind. Automatic organization adds a second writer that has no
-- user at all in scope, which is exactly the situation where an unenforced
-- invariant stops being a matter of care.
--
-- The invariant now lives in the schema. A composite foreign key carries it, and
-- the database rejects a cross-user filing regardless of which code attempts it.
--
-- No ownership model changes. collections.user_id is still the owner of a
-- collection, saved_items.user_id is still the owner of a saved item, system and
-- user collections keep their existing semantics, collection name uniqueness is
-- untouched, and saved_items.collection_id keeps its NOT NULL definition and its
-- column type. No organization column, status or table is added here; this
-- migration is only about the relationship between the two existing tables.

-- Preflight: report any row that would violate the invariant, and refuse to
-- proceed if one exists.
--
-- This checks rather than repairs. Repairing would mean deciding who owns a
-- saved item that is filed into somebody else's collection, and there is no
-- correct answer to that question available in the database: only a human knows
-- whether the item or the collection reference is the wrong one. Moving the item
-- silently would file a user's content into another user's collection and hide
-- the mistake, and deleting the row would destroy their data to satisfy a
-- constraint. Failing loudly leaves the decision where it belongs.
--
-- RAISE EXCEPTION with a formatted message rather than a CHECK-style message so
-- the offending count reaches the operator, and detail so the rows themselves can
-- be found. The whole check is one statement, so it sees a single consistent
-- snapshot and reports the same rows it would have blocked on.
DO $$
DECLARE
    mismatched_count BIGINT;
BEGIN
    SELECT COUNT(*)
    INTO mismatched_count
    FROM saved_items si
    JOIN collections c ON c.id = si.collection_id
    WHERE si.user_id <> c.user_id;

    IF mismatched_count > 0 THEN
        RAISE EXCEPTION
            'saved_items.collection_id references a collection owned by a different user: % row(s) violate the ownership invariant, so the composite foreign key cannot be added',
            mismatched_count
            USING HINT = 'Inspect with: SELECT si.id, si.user_id AS item_user, c.id AS collection_id, c.user_id AS collection_user FROM saved_items si JOIN collections c ON c.id = si.collection_id WHERE si.user_id <> c.user_id. Resolve each row deliberately before re-running this migration.';
    END IF;
END
$$;

-- The composite foreign key requires a unique target on (id, user_id).
--
-- This is a constraint rather than a bare unique index because the database needs
-- it to guarantee referential integrity, and a UNIQUE constraint is what that
-- guarantee is expressed with. It is also satisfiable by every existing row:
-- collections.id is already the primary key, so (id, user_id) is unique by
-- construction. The composite unique index this creates is redundant with the
-- primary key for lookups by id alone, and is deliberately kept anyway because a
-- foreign key's target must be exactly this unique shape. Dropping it later would
-- break the foreign key, not just slow a query down.
ALTER TABLE collections
    ADD CONSTRAINT collections_id_user_id_key UNIQUE (id, user_id);

-- Replace the single-column foreign key with the composite one.
--
-- Both halves happen in one statement so the table is never left in a state where
-- collection_id references collections at all. Dropping and re-adding
-- separately would create a window with no referential integrity on the column,
-- and a crash between the two would leave it that way.
--
-- ON DELETE RESTRICT is carried over unchanged: a collection that still holds
-- saved items cannot be removed, which is what stops a deletion from stranding an
-- item with no collection.
ALTER TABLE saved_items
    DROP CONSTRAINT saved_items_collection_id_fkey;

ALTER TABLE saved_items
    ADD CONSTRAINT saved_items_collection_id_fkey
        FOREIGN KEY (collection_id, user_id)
        REFERENCES collections (id, user_id)
        ON DELETE RESTRICT;