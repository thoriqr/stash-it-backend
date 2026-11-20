-- name: CountUsers :one
-- How many accounts exist, for the summary strip.
--
-- It counts every account rather than only those with a session, because the question the
-- operator is asking is "how many people have signed up", not "how many are online". The
-- second is not answerable from users at all: activity lives in sessions, which this
-- number deliberately does not join.
SELECT COUNT(*)
FROM users;

-- name: ListReleaseSummary :many
-- The published policy for every platform that has one.
--
-- The minimum release is joined rather than stored as a copied version, which is how the
-- schema keeps it: min_supported_release_id points at a real artifact, so the page reports
-- the same build a client's check would act on.
--
-- The latest release carries its artifact details because they are what an operator wants
-- next to a version: the git tag says which commit shipped, and the URL and digest identify
-- the exact file. The minimum carries only its version and build, because nothing downloads
-- it — it is a floor, not an offer.
--
-- No counts, no gaps, no derived status. Those were tried and they answered a question
-- nobody was asking; what matters is which build is offered and which build is the floor.
SELECT
    channels.platform,
    latest.version AS latest_version,
    latest.build AS latest_build,
    latest.published_at,
    latest.git_tag,
    latest.download_url,
    latest.file_size_bytes,
    minimum.version AS min_version,
    minimum.build AS min_build
FROM client_release_channels AS channels
JOIN client_releases AS latest
  ON latest.id = channels.latest_release_id
JOIN client_releases AS minimum
  ON minimum.id = channels.min_supported_release_id
ORDER BY channels.platform;

-- name: ListFeedback :many
-- One page of reports, newest first, optionally filtered to one status.
--
-- The owner is joined rather than read per row, so the page can name who filed each
-- report without a second query per entry. Both values come from the row that exists
-- now, so a renamed or re-addressed account is shown under its current identity
-- rather than a copy taken when the report was written.
--
-- id DESC is the tie-breaker and not decoration: created_at can repeat, and under
-- LIMIT/OFFSET a tied group split across a page boundary shows one row twice and
-- skips another.
SELECT
    f.id,
    f.type,
    f.message,
    f.status,
    f.platform,
    f.app_version,
    f.app_build,
    f.created_at,
    f.updated_at,
    u.display_name,
    u.email
FROM feedback AS f
JOIN users AS u ON u.id = f.user_id
WHERE (
    sqlc.narg(status)::text IS NULL
    OR f.status = sqlc.narg(status)::text
)
ORDER BY f.created_at DESC, f.id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountFeedback :one
-- How many reports match the same filter the listing used.
--
-- It takes the same nullable argument so the number and the page cannot disagree
-- about which rows were being counted. It is a count of the whole filter, not of one
-- page: a total that changed as the operator scrolled would not be a total.
SELECT COUNT(*)
FROM feedback AS f
WHERE (
    sqlc.narg(status)::text IS NULL
    OR f.status = sqlc.narg(status)::text
);

-- name: CountFeedbackByStatus :many
-- How many reports hold each status, for the filter chips.
--
-- One statement rather than three counts, because the three numbers are always shown
-- together and three separate reads could disagree with each other.
SELECT status, COUNT(*) AS count
FROM feedback
GROUP BY status;

-- name: SetFeedbackStatus :one
-- Moves one report to another status and returns the stored row.
--
-- There is no ownership scope, and that is deliberate: the only caller is the
-- operator page, which is reachable only over loopback. This statement is not
-- mounted on any route the public app serves.
UPDATE feedback
SET status = sqlc.arg(status)
WHERE id = sqlc.arg(id)
RETURNING id, status, updated_at;

-- name: DeleteFeedback :execrows
-- Removes one report permanently.
--
-- A hard delete, because what it removes is a report the operator decided carries no
-- value: spam, a test message, an accident. Nothing reads it afterwards, so there is
-- nothing to preserve and no soft-delete column to filter on forever.
--
-- The count of removed rows is what lets the page report a report that was already
-- gone rather than redirecting as though it had deleted something.
DELETE FROM feedback
WHERE id = sqlc.arg(id);
