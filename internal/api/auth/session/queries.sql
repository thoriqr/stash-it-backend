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