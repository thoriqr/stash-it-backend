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
    provider_subject,
    email_snapshot,
    display_name_snapshot
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(provider),
    sqlc.arg(provider_subject),
    sqlc.arg(email_snapshot),
    sqlc.arg(display_name_snapshot)
)
RETURNING
    id,
    user_id,
    provider,
    provider_subject,
    email_snapshot,
    display_name_snapshot,
    created_at;

-- name: CreateAccountLinkConfirmation :one
INSERT INTO account_link_confirmations (
    user_id,
    provider,
    provider_subject,
    email_snapshot,
    display_name_snapshot,
    expires_at
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(provider),
    sqlc.arg(provider_subject),
    sqlc.arg(email_snapshot),
    sqlc.arg(display_name_snapshot),
    sqlc.arg(expires_at)
)
RETURNING
    id,
    user_id,
    provider,
    provider_subject,
    email_snapshot,
    display_name_snapshot,
    created_at,
    expires_at,
    confirmed_at;

-- name: GetActiveAccountLinkConfirmation :one
SELECT
    alc.id,
    alc.user_id,
    alc.provider,
    alc.provider_subject,
    alc.email_snapshot,
    alc.display_name_snapshot,
    alc.created_at,
    alc.expires_at,
    alc.confirmed_at,
    u.email AS user_email,
    u.display_name AS user_display_name
FROM account_link_confirmations alc
JOIN users u ON u.id = alc.user_id
WHERE alc.id = sqlc.arg(id)
  AND alc.confirmed_at IS NULL
  AND alc.expires_at > NOW();

-- name: GetAccountLinkConfirmationForUpdate :one
SELECT
    alc.id,
    alc.user_id,
    alc.provider,
    alc.provider_subject,
    alc.email_snapshot,
    alc.display_name_snapshot,
    u.email AS user_email,
    u.display_name AS user_display_name
FROM account_link_confirmations alc
JOIN users u ON u.id = alc.user_id
WHERE alc.id = sqlc.arg(id)
  AND alc.confirmed_at IS NULL
  AND alc.expires_at > NOW()
FOR UPDATE;

-- name: MarkAccountLinkConfirmationConfirmed :execrows
UPDATE account_link_confirmations
SET confirmed_at = NOW()
WHERE id = sqlc.arg(id)
  AND confirmed_at IS NULL;