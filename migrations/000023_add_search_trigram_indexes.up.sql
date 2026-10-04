-- Phase B: basic search support.
--
-- Enables the two contrib extensions the search strategy needs and adds the
-- trigram indexes that back the user-scoped saved item and collection search
-- predicates. This migration only prepares the schema: the search repository,
-- service, handlers, routes and sqlc target are a later phase and are not
-- implemented here.
--
-- No search tables, history, suggestions, autocomplete structures, tsvector
-- columns, ranking columns or denormalized documents are added. Search queries
-- the existing saved_items and collections rows directly.
--
-- Both indexes are composite GIN (user_id, <text> gin_trgm_ops) so that a single
-- user-scoped trigram lookup serves both the WHERE and the ORDER BY. The uuid
-- GIN operator class required by that shape comes from btree_gin, not from
-- pg_trgm.

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- Backs the saved item search predicate over title. domain and url are
-- intentionally NOT indexed in this first version: they still take part in the
-- predicate, so they can still produce a match, they are simply resolved by a
-- sequential scan on the user-scoped subset rather than by an index.
CREATE INDEX saved_items_search_idx
    ON saved_items USING gin (user_id, title gin_trgm_ops);

-- Backs the collection search predicate over name. lower(btrim(name)) is not
-- indexed: name is compared and matched case-insensitively by the predicate, and
-- gin_trgm_ops is already case-insensitive, so no expression index is required.
CREATE INDEX collections_search_idx
    ON collections USING gin (user_id, name gin_trgm_ops);