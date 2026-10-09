-- name: SearchSavedItems :many
-- Searches the user's saved items by substring and by fuzzy word similarity.
--
-- Global search is PostgreSQL native, pg_trgm based and user scoped. No full text
-- search and no tsvector: v1 matches on substrings with a fuzzy fallback for
-- typos.
--
-- Both search queries carry the substring predicate and the fuzzy predicate in one
-- statement. Fuzzy matching is a fallback semantic, not a separate operation, so
-- there is no second round trip and no second query shape: the ranking
-- expression below is what makes a substring match outrank a fuzzy-only match.
--
-- Matching covers title, domain and url. All three take part in the predicate,
-- so any one of them can bring a row into the result set, and all three are
-- weighted in the score. url is NOT NULL in the schema, so it always takes part.
-- title and domain are nullable, hence the coalesce() on the fuzzy side only:
-- coalesce(title, '') is not used in the ILIKE clauses because a bare
-- `x ILIKE '%q%'` is already NULL safe in a WHERE clause, and leaving the
-- column unwrapped keeps the clause as close to the plain column as possible.
--
-- Ranking is a two part score:
--
--   10.0 when any of the three fields contains the query as a substring, else 0
-- + 1.0 * word_similarity(query, title)
-- + 0.8 * word_similarity(query, domain)
-- + 0.6 * word_similarity(query, url)
--
-- The 10.0 substring bonus is deliberately larger than the largest possible
-- fuzzy-only score, which is 1.0 + 0.8 + 0.6 = 2.4. No combination of fuzzy
-- weights can therefore outrank a substring match, which is what makes fuzzy
-- matching a fallback rather than a competing relevance signal.
--
-- word_similarity() is used rather than similarity() because it finds the best
-- matching word extent instead of comparing whole strings. A long title or URL
-- scores poorly against similarity() even when it contains the query, while
-- word_similarity() scores the matching word itself.
--
-- Recency is not part of the score. It appears only as a deterministic
-- tiebreaker after created_at and then id, which together form a total order
-- because id is the primary key.
--
-- The projection carries what a result is rendered from, and nothing more.
-- updated_at is absent because no result reports it and nothing orders by it,
-- and platform is absent because it is not a searchable field and a result has
-- no use for it. title, domain and image_url are read rather than derived: a
-- result that has never been enriched reports NULLs, and no column is ever
-- substituted for one of them.
--
-- The collection is joined in, rather than read per result, because a result
-- names the collection it lives in and that name only exists in the
-- collections table. The join is owner-scoped on both sides: migration 000025's
-- composite foreign key over (collection_id, user_id) already guarantees an
-- item's collection belongs to the item's owner, so c.user_id = si.user_id
-- cannot change the result, it states the intent and it keeps another user's
-- collection unreachable even if that constraint were ever relaxed. An INNER
-- JOIN is therefore not lossy: every saved item has a NOT NULL collection_id
-- that references an existing row.
SELECT
    saved_items.id,
    saved_items.title,
    saved_items.url,
    saved_items.domain,
    saved_items.image_url,
    saved_items.enrichment_status,
    saved_items.collection_id,
    collections.name AS collection_name,
    saved_items.created_at,
    (
        CASE
            WHEN saved_items.title  ILIKE '%' || sqlc.arg(query) || '%'
              OR saved_items.domain ILIKE '%' || sqlc.arg(query) || '%'
              OR saved_items.url    ILIKE '%' || sqlc.arg(query) || '%'
            THEN 10.0
            ELSE 0.0
        END
        + 1.0 * word_similarity(sqlc.arg(query), coalesce(saved_items.title,  ''))
        + 0.8 * word_similarity(sqlc.arg(query), coalesce(saved_items.domain, ''))
        + 0.6 * word_similarity(sqlc.arg(query), saved_items.url)
    )::float8 AS score
FROM saved_items
JOIN collections
  ON collections.id = saved_items.collection_id
 AND collections.user_id = saved_items.user_id
WHERE saved_items.user_id = sqlc.arg(user_id)
  AND (
           saved_items.title  ILIKE '%' || sqlc.arg(query) || '%'
        OR saved_items.domain ILIKE '%' || sqlc.arg(query) || '%'
        OR saved_items.url    ILIKE '%' || sqlc.arg(query) || '%'
        OR word_similarity(
            sqlc.arg(query),
            coalesce(saved_items.title,  '')
        ) > 0.3
        OR word_similarity(
            sqlc.arg(query),
            coalesce(saved_items.domain, '')
        ) > 0.3
        OR word_similarity(
            sqlc.arg(query),
            saved_items.url
        ) > 0.3
      )
ORDER BY score DESC, saved_items.created_at DESC, saved_items.id DESC
-- Named result_limit rather than limit, which is a reserved word. The saved
-- items queries already spell this parameter page_limit for the same reason.
LIMIT sqlc.arg(result_limit);

-- name: SearchCollections :many
-- Searches the user's collections by substring and by fuzzy word similarity.
--
-- System collections are included on purpose: a search for "unsorted" should
-- find the Unsorted collection, and the collections table is the only place its
-- display name lives. There is deliberately no type = 'user' filter.
--
-- collection_id is not a searchable field. It is neither matched nor scored,
-- because a collection does not know its own saved items without joining, and
-- searching for an identifier is not a user-facing intent.
--
-- The projection is the whole of what a collection result reports. type and
-- system_key are not selected even though the query can match a system
-- collection: a result is a collection to navigate to, and GET /collections
-- remains where how a collection came to be is reported. created_at and
-- updated_at are selected by nothing either, since created_at orders the rows
-- here without being projected. The score stays, because the ORDER BY refers to
-- it by name.
SELECT
    collections.id,
    collections.name,
    (
        CASE
            WHEN collections.name ILIKE '%' || sqlc.arg(query) || '%'
            THEN 10.0
            ELSE 0.0
        END
        + 0.95 * word_similarity(sqlc.arg(query), collections.name)
    )::float8 AS score
FROM collections
WHERE collections.user_id = sqlc.arg(user_id)
  AND (
           collections.name ILIKE '%' || sqlc.arg(query) || '%'
        OR word_similarity(
            sqlc.arg(query),
            collections.name
        ) > 0.3
      )
ORDER BY score DESC, collections.created_at DESC, collections.id DESC
LIMIT sqlc.arg(result_limit);