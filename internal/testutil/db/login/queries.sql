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

-- name: CreateGoogleAuthIdentity :exec
INSERT INTO auth_identities (
    user_id,
    provider,
    provider_subject,
    email_snapshot,
    display_name_snapshot
)
VALUES (
    sqlc.arg(user_id),
    'google',
    sqlc.arg(provider_subject),
    sqlc.arg(email_snapshot),
    sqlc.arg(display_name_snapshot)
);

-- name: GetSocialRegistrationState :one
SELECT
    vr.id AS verification_id,
    vr.subject_id AS pending_registration_id,
    pr.email,
    pr.registration_type,
    pr.status AS registration_status,
    psi.provider,
    psi.provider_subject,
    psi.email_snapshot,
    psi.display_name_snapshot
FROM verification_requests vr
JOIN pending_registrations pr
    ON pr.id = vr.subject_id
JOIN pending_social_identities psi
    ON psi.pending_registration_id = pr.id
WHERE vr.id = sqlc.arg(verification_id)
  AND vr.subject_type = 'pending_registration';

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
    NOW() + INTERVAL '15 minutes'
)
RETURNING id;

-- name: GetAccountLinkConfirmationState :one
SELECT
    alc.id,
    alc.user_id,
    alc.provider,
    alc.provider_subject,
    alc.email_snapshot,
    alc.display_name_snapshot,
    alc.expires_at,
    alc.confirmed_at,
    u.email AS user_email,
    u.display_name AS user_display_name
FROM account_link_confirmations alc
JOIN users u
    ON u.id = alc.user_id
WHERE alc.id = sqlc.arg(id);

-- name: GetGoogleAuthIdentityState :one
SELECT
    ai.id,
    ai.user_id,
    ai.provider,
    ai.provider_subject,
    ai.email_snapshot,
    ai.display_name_snapshot
FROM auth_identities ai
WHERE ai.user_id = sqlc.arg(user_id)
  AND ai.provider = 'google'
  AND ai.provider_subject = sqlc.arg(provider_subject);