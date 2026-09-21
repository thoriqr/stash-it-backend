-- name: TruncateSessionData :exec
TRUNCATE TABLE
    refresh_tokens,
    sessions,
    users
CASCADE;

-- name: CreateSessionUser :one
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

-- name: CreateTestSession :one
INSERT INTO sessions (
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    absolute_expires_at,
    revoked_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(platform),
    sqlc.arg(installation_id),
    sqlc.arg(device_name),
    sqlc.arg(user_agent),
    sqlc.arg(absolute_expires_at),
    sqlc.arg(revoked_at)
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


-- name: CreateTestRefreshToken :one
INSERT INTO refresh_tokens (
    session_id,
    token_hash,
    replaced_by
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(token_hash),
    sqlc.arg(replaced_by)
)
RETURNING
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by;

-- name: GetRefreshTokenState :one
SELECT
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by
FROM refresh_tokens
WHERE id = sqlc.arg(id);

-- name: GetSessionState :one
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
WHERE id = sqlc.arg(id);

-- name: UpdateTestSessionLastActivity :exec
UPDATE sessions
SET last_activity_at = sqlc.arg(last_activity_at)
WHERE id = sqlc.arg(id);