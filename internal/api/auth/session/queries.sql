-- name: CreateSession :one
INSERT INTO sessions (
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    absolute_expires_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(platform),
    sqlc.arg(installation_id),
    sqlc.arg(device_name),
    sqlc.arg(user_agent),
    sqlc.arg(absolute_expires_at)
)
RETURNING
    id,
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    created_at,
    last_activity_at,
    absolute_expires_at,
    revoked_at;

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (
    session_id,
    token_hash
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(token_hash)
)
RETURNING
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by;

-- name: GetRefreshTokenWithSessionForUpdate :one
SELECT
    rt.id,
    rt.session_id,
    rt.token_hash,
    rt.issued_at,
    rt.replaced_by,
    s.user_id,
    s.platform,
    s.installation_id,
    s.device_name,
    s.user_agent,
    s.created_at,
    s.last_activity_at,
    s.absolute_expires_at,
    s.revoked_at
FROM refresh_tokens rt
JOIN sessions s
    ON s.id = rt.session_id
WHERE rt.token_hash = sqlc.arg(token_hash)
FOR UPDATE OF rt, s;

-- name: ReplaceRefreshToken :exec
UPDATE refresh_tokens
SET replaced_by = sqlc.arg(replaced_by)
WHERE id = sqlc.arg(id);

-- name: UpdateSessionActivity :exec
UPDATE sessions
SET last_activity_at = NOW()
WHERE id = sqlc.arg(id);

-- name: RevokeSession :exec
-- Revokes one session, scoped to the user it belongs to.
--
-- user_id is matched rather than trusted from the caller. The session id alone
-- does not establish ownership, so matching only on it would let a revocation
-- that names the wrong session id reach another user's session. Every caller
-- already has both values: logout has them from the verified access token, and
-- the refresh path has them from the token row's own session.
--
-- The predicate is not a permission check the caller could bypass by passing the
-- matching user id, because a caller that does not hold the session has no way to
-- present its owner. It is the last guard, in the same spirit as
-- saved_items_collection_id_fkey.
--
-- revoked_at IS NULL keeps this idempotent: revoking twice is not an error, and
-- a session that is already revoked needs no write. Nothing is returned, so a
-- session that is missing, already revoked or another user's is indistinguishable
-- here. That is deliberate: logout must not disclose whether an id exists for
-- somebody else.
UPDATE sessions
SET revoked_at = NOW()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: RevokeSessionForUser :one
UPDATE sessions
SET revoked_at = NOW()
WHERE id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
RETURNING id;


-- name: ListSessions :many
-- Ordered by last_activity_at DESC with id DESC as the tie-breaker.
--
-- last_activity_at is not unique. It defaults to NOW(), which is constant within
-- a transaction, and UpdateSessionActivity writes NOW() too, so sessions created
-- or refreshed together can carry the same value. Under LIMIT/OFFSET a tied group
-- can be split across a page boundary in an arbitrary order, which is how a
-- client ends up shown one session twice and another not at all.
--
-- sessions.id is uuidv7() with no overriding trigger, so it is unique and
-- monotonic in creation order: the tie-breaker is both total and the right way
-- round, putting the most recently created session first among equals.
SELECT
    id,
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    created_at,
    last_activity_at,
    absolute_expires_at,
    revoked_at
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
ORDER BY last_activity_at DESC, id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountSessions :one
SELECT COUNT(*)
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: DeleteCleanupEligibleSessions :execrows
-- Removes a bounded batch of sessions that can no longer be used, letting the
-- refresh_tokens_session_id_fkey cascade remove each one's token chain with it.
--
-- The three conditions are the refresh path's own, read from the row rather than
-- decided twice. GetRefreshTokenWithSessionForUpdate takes a revoked session, a
-- session past absolute_expires_at, and a session past the idle window, so a row
-- matching here is a row refresh already refuses. Cleanup therefore cannot
-- shorten any session that is still usable.
--
-- The comparison is deliberately exact about which side is inclusive. The refresh
-- path asks time.Now().After(absolute_expires_at), which is absolute_expires_at
-- <= now, and asks whether the idle gap exceeds the window, which is
-- last_activity_at < now - window. Using <= on the idle side instead would delete
-- a session sitting exactly on the boundary that refresh would still accept.
--
-- idle_window is SessionIdleLifetime, supplied by the caller. The duration is
-- never written here: this file must not become a second place the idle timeout
-- is defined, or the two could disagree about when a session ends.
--
-- NOW() rather than a bound cutoff, so one statement evaluates every condition
-- against a single database timestamp instead of mixing the database's clock with
-- the API host's inside one comparison.
--
-- Only sessions are deleted. No statement anywhere removes an individual refresh
-- token while its session survives, because replaced_by is ON DELETE SET NULL:
-- deleting a successor would NULL its predecessor's replaced_by, which is
-- indistinguishable from a token that was never rotated and would silently
-- disable reuse detection for that chain. Removing the parent removes the whole
-- chain together and leaves no predecessor pointing at nothing.
--
-- ORDER BY id makes a bounded run consume the oldest eligible sessions rather
-- than an arbitrary subset. sessions.id is uuidv7() with no overriding trigger,
-- so it is monotonic in creation order, and the oldest sessions are also the
-- most likely to be the expired ones.
--
-- FOR UPDATE SKIP LOCKED is what lets two invocations overlap. It is not needed
-- for correctness — a row another transaction deleted is simply dropped from the
-- result — but without it the second run queues behind the first on the same
-- rows instead of taking its own batch alongside it.
--
-- The LIMIT is what keeps one statement bounded. PostgreSQL may still scan many
-- rows to find eligible candidates: the three conditions are an OR, and an OR
-- cannot be served from any single-column index, so the planner walks
-- sessions_pkey applying the filter. That scan is the accepted cost of this
-- statement and is why no index on sessions is added for it.
WITH eligible AS (
    SELECT id
    FROM sessions
    WHERE revoked_at IS NOT NULL
       OR absolute_expires_at <= NOW()
       OR last_activity_at < NOW() - sqlc.arg(idle_window)::interval
    ORDER BY id
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE SKIP LOCKED
)
DELETE FROM sessions
USING eligible
WHERE sessions.id = eligible.id;