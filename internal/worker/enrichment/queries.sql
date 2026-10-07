-- name: GetSavedItemForBackgroundEnrichment :one
-- Loads the one saved item a background enrichment task is about.
--
-- This is the only statement in the project that reads a saved item by id alone.
-- The API scopes every read by id AND user_id because an endpoint must not act on
-- somebody else's item, and it must not disclose that an id exists. Neither
-- concern applies here: there is no caller to mislead and no id to disclose, the
-- id came from a committed save, and the job acts on the row rather than on a
-- user. user_id is returned only so a log line can name the owner.
--
-- No rows means the item was deleted between the save and the task being
-- processed, which is an ordinary outcome rather than a failure, and the
-- repository reports it as such.
SELECT
    id,
    user_id,
    url
FROM saved_items
WHERE id = sqlc.arg(id);


-- name: CompleteSavedItemEnrichment :one
-- Persists the metadata the extractor actually returned and marks the item
-- completed.
--
-- The statement is deliberately the same write as the one the synchronous
-- endpoint makes, statement for statement, so the two paths cannot produce
-- different rows for the same page. Every metadata column is written from the
-- result, including as NULL, so a later enrichment that finds nothing clears a
-- previous value instead of leaving stale data behind.
--
-- last_enriched_at is written here and only here: a successful attempt is the
-- only moment at which metadata was actually refreshed.
--
-- collection_id is deliberately absent. domain and collection_id stay exactly as
-- they are, because enrichment reads metadata and organizing is a separate
-- concern this feature does not have.
--
-- id and user_id are returned so the caller can schedule organization from this
-- statement's own result rather than re-reading the row it has just written. The
-- write is a single statement in autocommit, so a row returned here is committed
-- by the time this returns, which is what makes it safe to enqueue the follow-up
-- work on the strength of it.
UPDATE saved_items
SET
    title = sqlc.arg(title),
    platform = sqlc.arg(platform),
    description = sqlc.arg(description),
    image_url = sqlc.arg(image_url),
    enrichment_status = 'completed',
    last_enriched_at = NOW()
WHERE id = sqlc.arg(id)
RETURNING id, user_id;


-- name: FailSavedItemEnrichment :one
-- Records that the enrichment process itself failed.
--
-- No metadata column is written and last_enriched_at is left untouched, so a
-- failed attempt refreshes nothing: whatever metadata the item already carries
-- stays as it is, and a page that stopped answering does not blank out a title
-- that was already there.
--
-- No reason is stored, because the schema has no column for one. It is logged by
-- the caller instead.
UPDATE saved_items
SET
    enrichment_status = 'failed'
WHERE id = sqlc.arg(id)
RETURNING id;
