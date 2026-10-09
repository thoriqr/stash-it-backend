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
ORDER BY last_activity_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountSessions :one
SELECT COUNT(*)
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;