-- Reverses 000025 only, in reverse dependency order.
--
-- The single-column foreign key is restored before the composite unique
-- constraint is dropped, because the composite foreign key depends on that
-- constraint and cannot outlive it. Objects owned by earlier migrations are left
-- untouched.
--
-- Restoring the previous relationship necessarily restores the weaker invariant
-- it enforced: after this migration, the database once again accepts a saved item
-- filed into a collection owned by a different user. Application code is what
-- keeps that from happening, as it did before 000025.

ALTER TABLE saved_items
    DROP CONSTRAINT saved_items_collection_id_fkey;

ALTER TABLE saved_items
    ADD CONSTRAINT saved_items_collection_id_fkey
        FOREIGN KEY (collection_id)
        REFERENCES collections (id)
        ON DELETE RESTRICT;

-- Removed only because 000025 introduced it solely as the composite foreign
-- key's target. collections.id is already the primary key, so dropping this does
-- not weaken any uniqueness guarantee about a collection's identity.
ALTER TABLE collections
    DROP CONSTRAINT collections_id_user_id_key;