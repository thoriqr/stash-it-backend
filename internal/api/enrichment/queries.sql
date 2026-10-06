-- name: GetSavedItemURLForEnrichment :one
-- Resolves one saved item inside the authenticated user's ownership scope and
-- returns only what enrichment needs: the URL to fetch.
--
-- A missing item and an item owned by another user both match nothing here, so
-- the repository reports the same not found error for both and the endpoint
-- never discloses whether an id exists.
SELECT
    url
FROM saved_items
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);


-- name: CompleteSavedItemEnrichment :one
-- Persists the metadata the extractor actually returned and marks the item
-- completed.
--
-- Every metadata column is written from the result, including as NULL, so a
-- later enrichment that finds nothing clears a previous value instead of leaving
-- stale data behind. A field the extractor did not provide is never invented:
-- it is stored as NULL, which is what the column already held.
--
-- last_enriched_at is written here and only here. It records when metadata was
-- last refreshed, and this is the only path that refreshes it.
--
-- domain and collection_id are deliberately absent. domain is derived from the
-- URL at save time and enrichment never touches it, and collection_id is the
-- user's own organization decision, which enrichment must not make.
UPDATE saved_items
SET
    title = sqlc.arg(title),
    platform = sqlc.arg(platform),
    description = sqlc.arg(description),
    image_url = sqlc.arg(image_url),
    enrichment_status = 'completed',
    last_enriched_at = NOW()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
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


-- name: FailSavedItemEnrichment :one
-- Records that the enrichment process itself failed.
--
-- No metadata column is written and last_enriched_at is left untouched, because
-- a failed attempt refreshes nothing: it is not a moment at which metadata was
-- last refreshed. Whatever metadata the item already carries stays as it is.
--
-- The failure is recorded as status only. No reason is stored, because the
-- schema has no column for one and inventing one is not this feature's call.
UPDATE saved_items
SET
    enrichment_status = 'failed'
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
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