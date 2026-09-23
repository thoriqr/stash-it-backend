-- name: GetUserForLogin :one
SELECT
    u.id,
    u.email,
    u.display_name,
    pc.password_hash
FROM users u
JOIN password_credentials pc
    ON pc.user_id = u.id
WHERE u.email = sqlc.arg(email);

-- name: GetUserForLoginByID :one
SELECT
    u.id,
    u.email,
    u.display_name,
    pc.password_hash
FROM users u
LEFT JOIN password_credentials pc
    ON pc.user_id = u.id
WHERE u.id = sqlc.arg(user_id);

-- name: GetAuthIdentity :one
SELECT
    ai.id,
    ai.user_id,
    ai.provider,
    ai.provider_subject,
    u.email,
    u.display_name
FROM auth_identities ai
JOIN users u
    ON u.id = ai.user_id
WHERE ai.provider = sqlc.arg(provider)
  AND ai.provider_subject = sqlc.arg(provider_subject);

-- name: GetUserByEmail :one
SELECT
    u.id,
    u.email,
    u.display_name
FROM users u
WHERE u.email = sqlc.arg(email);

-- name: CreateAuthIdentity :one
INSERT INTO auth_identities (
    user_id,
    provider,
    provider_subject
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(provider),
    sqlc.arg(provider_subject)
)
RETURNING
    id,
    user_id,
    provider,
    provider_subject,
    created_at;