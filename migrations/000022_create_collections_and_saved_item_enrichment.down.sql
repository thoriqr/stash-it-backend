-- Reverse only what 000022 introduced, in reverse dependency order. Objects
-- owned by earlier migrations, notably the saved_items updated_at trigger and
-- saved_items_user_created_at_idx, are left untouched.

DROP INDEX saved_items_collection_id_idx;

ALTER TABLE saved_items
DROP CONSTRAINT saved_items_enrichment_status_check;

ALTER TABLE saved_items
DROP CONSTRAINT saved_items_collection_id_fkey;

ALTER TABLE saved_items
DROP COLUMN last_enriched_at,
DROP COLUMN enrichment_started_at,
DROP COLUMN enrichment_status,
DROP COLUMN collection_id;

DROP TRIGGER collections_set_updated_at ON collections;

DROP INDEX collections_user_idx;

DROP INDEX collections_system_key_unique;

DROP INDEX collections_user_name_unique;

DROP TABLE collections;