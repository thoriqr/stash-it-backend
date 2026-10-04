-- Test-only queries for the search integration tests.
--
-- These seed and inspect rows. They deliberately write columns the runtime
-- never writes: search must be proven to match on title, which is NULL for every
-- item saved in production until background enrichment exists. So title is
-- settable here, and so is domain, and so is created_at, because the
-- determinism test needs two rows that tie on score and on recency.

-- name: TruncateSearchData :exec
TRUNCATE TABLE
    saved_items,
    collections,
    users
CASCADE;

-- name: CreateSearchUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
) VALUES (
    sqlc.arg(email),
    sqlc.arg(display_name),
    NOW()
)
RETURNING id;

-- name: CreateSearchCollection :one
-- type and system_key are both supplied so a test can create a system collection
-- with its key, which is what the production Unsorted collection looks like.
INSERT INTO collections (
    user_id,
    name,
    type,
    system_key
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    sqlc.arg(type),
    sqlc.narg(system_key)
)
RETURNING id;

-- name: CreateSearchSavedItem :one
-- domain, title and platform are nullable and independently settable so each can
-- be the only field carrying a value. platform is settable purely so a test can
-- prove it is NOT searched: nothing in production populates it yet, so without
-- this column there would be no way to show a populated platform is ignored.
--
-- created_at is settable and defaulted to NOW(), so the determinism test can
-- create rows that tie on both score and recency and leave id to break the tie.
INSERT INTO saved_items (
    user_id,
    url,
    domain,
    title,
    platform,
    collection_id,
    created_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(url),
    sqlc.narg(domain),
    sqlc.narg(title),
    sqlc.narg(platform),
    sqlc.arg(collection_id),
    COALESCE(sqlc.narg(created_at), NOW())
)
RETURNING id;

-- name: CountSavedItemsForSearchUser :one
SELECT COUNT(*)
FROM saved_items
WHERE user_id = sqlc.arg(user_id);

-- name: CountCollectionsForSearchUser :one
SELECT COUNT(*)
FROM collections
WHERE user_id = sqlc.arg(user_id);