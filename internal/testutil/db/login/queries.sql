-- name: TruncateLoginData :exec
TRUNCATE TABLE
    refresh_tokens,
    sessions,
    password_credentials,
    users
CASCADE;

-- name: CreateLoginUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
)
VALUES (
    sqlc.arg(email),
    sqlc.arg(display_name),
    NOW()
)
RETURNING id;

-- name: CreatePasswordCredential :exec
INSERT INTO password_credentials (
    user_id,
    password_hash
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(password_hash)
);

-- name: GetLoginUserState :one
SELECT
    u.id,
    u.email,
    u.display_name,
    pc.user_id AS password_user_id
FROM users u
LEFT JOIN password_credentials pc
    ON pc.user_id = u.id
WHERE u.email = sqlc.arg(email);

-- name: CountUserSessions :one
SELECT COUNT(*) AS count
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;