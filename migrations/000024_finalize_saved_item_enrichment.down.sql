-- Reverses 000024 only, in reverse dependency order.
--
-- The CHECK is restored before enrichment_started_at so the table is never in a
-- state where the column exists but 'processing' is disallowed. Objects owned
-- by earlier migrations are left untouched.

ALTER TABLE saved_items
DROP CONSTRAINT saved_items_enrichment_status_check;

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

ALTER TABLE saved_items
ADD COLUMN enrichment_started_at TIMESTAMPTZ;

ALTER TABLE saved_items
DROP COLUMN description,
DROP COLUMN image_url;