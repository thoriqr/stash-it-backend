-- Finalizes the saved item enrichment schema.
--
-- saved_items stays the single table holding Saved Item metadata. No separate
-- metadata table is introduced.
--
-- Adds the two remaining metadata columns, description and image_url, both
-- optional because most pages expose neither and enrichment is best effort.
-- Both are written only by the background enrichment worker, which is not
-- implemented yet.
--
-- Drops enrichment_started_at and removes 'processing' from the allowed
-- enrichment statuses. Worker execution state is not product state, so it is
-- not persisted: an item is pending until enrichment completes or fails, and a
-- worker that dies mid-job simply leaves it pending. Nothing writes or reads
-- 'processing' today, so no row is expected to hold it.
--
-- last_enriched_at is kept. It records when metadata was last refreshed, which
-- is product state and is independent of the status value.

ALTER TABLE saved_items
ADD COLUMN description TEXT;

ALTER TABLE saved_items
ADD COLUMN image_url TEXT;

ALTER TABLE saved_items
DROP COLUMN enrichment_started_at;

-- enrichment_status is TEXT plus a CHECK, never a PostgreSQL ENUM.
ALTER TABLE saved_items
DROP CONSTRAINT saved_items_enrichment_status_check;

ALTER TABLE saved_items
ADD CONSTRAINT saved_items_enrichment_status_check
    CHECK (
        enrichment_status IN (
            'pending',
            'completed',
            'failed'
        )
    );