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

-- name: GetRefreshTokenWithSession :one
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
WHERE rt.token_hash = sqlc.arg(token_hash);

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
UPDATE sessions
SET revoked_at = NOW()
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL;


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