-- name: TruncateRegistrationData :exec
TRUNCATE TABLE
    verification_codes,
    verification_requests,
    registration_continuations,
    pending_social_identities,
    pending_registrations,
    auth_identities,
    password_credentials,
    refresh_tokens,
    sessions,
    users
CASCADE;

-- name: GetRegistrationState :one
SELECT
    pr.id,
    pr.email,
    pr.registration_type,
    pr.status,
    pr.expires_at,
    vr.id AS verification_id,
    vr.status AS verification_status,
    vr.pin_issued_count,
    vr.last_sent_at
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
WHERE pr.email = sqlc.arg(email);

-- name: CreateCompletedRegistration :one
WITH registration AS (
    INSERT INTO pending_registrations (
        email,
        registration_type,
        status,
        expires_at
    ) VALUES (
        sqlc.arg(email),
        'manual',
        'completed',
        NOW() + INTERVAL '7 days'
    )
    RETURNING id
)
INSERT INTO verification_requests (
    subject_type,
    subject_id,
    purpose,
    status
)
SELECT
    'pending_registration',
    id,
    'registration',
    'verified'
FROM registration
RETURNING id;

-- name: CreateRegistrationContinuation :one
WITH registration AS (
    INSERT INTO pending_registrations (
        email,
        registration_type,
        status,
        expires_at
    ) VALUES (
        sqlc.arg(email),
        'manual',
        'pending',
        NOW() + INTERVAL '7 days'
    )
    RETURNING id
)
INSERT INTO registration_continuations (
    pending_registration_id,
    token_hash,
    expires_at
)
SELECT
    id,
    sqlc.arg(token_hash),
    NOW() + INTERVAL '15 minutes'
FROM registration
RETURNING
    id,
    pending_registration_id,
    token_hash,
    expires_at,
    consumed_at,
    created_at;

-- name: GetFinalizedRegistrationState :one
SELECT
    u.id AS user_id,
    u.email,
    u.display_name,
    pc.user_id AS password_user_id,
    pr.status AS registration_status,
    rc.consumed_at
FROM users u
JOIN password_credentials pc
    ON pc.user_id = u.id
JOIN pending_registrations pr
    ON pr.email = u.email
JOIN registration_continuations rc
    ON rc.pending_registration_id = pr.id
WHERE u.email = sqlc.arg(email);

-- name: CreateExistingUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
) VALUES (
    sqlc.arg(email),
    'Existing User',
    NOW()
)
RETURNING id;

-- name: CreateExpiredPendingRegistration :one
WITH registration AS (
    INSERT INTO pending_registrations (email, registration_type, status, expires_at)
    VALUES (sqlc.arg(email), 'manual', 'pending', NOW() - INTERVAL '1 hour')
    RETURNING id
), verification AS (
    INSERT INTO verification_requests (subject_type, subject_id, purpose, status)
    SELECT 'pending_registration', id, 'registration', 'pending' FROM registration
    RETURNING id
)
SELECT id
FROM verification;

-- name: GetRegistrationHistory :many
SELECT
    pr.id,
    pr.status,
    vr.id AS verification_id,
    vr.pin_issued_count
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
WHERE pr.email = sqlc.arg(email)
ORDER BY pr.created_at, pr.id;

-- name: MakeVerificationResendable :exec
UPDATE verification_requests
SET last_sent_at = NOW() - INTERVAL '1 day'
WHERE id = sqlc.arg(id);

-- name: CreateSocialRegistrationContinuation :one
WITH registration AS (
    INSERT INTO pending_registrations (
        email,
        registration_type,
        status,
        expires_at
    ) VALUES (
        sqlc.arg(email),
        'social',
        'pending',
        NOW() + INTERVAL '7 days'
    )
    RETURNING id
),
social_identity AS (
    INSERT INTO pending_social_identities (
        pending_registration_id,
        provider,
        provider_subject,
        email_snapshot,
        display_name_snapshot
    )
    SELECT
        id,
        sqlc.arg(provider),
        sqlc.arg(provider_subject),
        sqlc.arg(email_snapshot),
        sqlc.arg(display_name_snapshot)
    FROM registration
)
INSERT INTO registration_continuations (
    pending_registration_id,
    token_hash,
    expires_at
)
SELECT
    id,
    sqlc.arg(token_hash),
    NOW() + INTERVAL '15 minutes'
FROM registration
RETURNING
    id,
    pending_registration_id,
    token_hash,
    expires_at,
    consumed_at,
    created_at;

-- name: GetFinalizedSocialRegistrationState :one
SELECT
    u.id AS user_id,
    u.email,
    u.display_name,
    u.email_verified_at,
    ai.id AS auth_identity_id,
    ai.provider,
    ai.provider_subject,
    ai.email_snapshot,
    ai.display_name_snapshot,
    pr.status AS registration_status,
    rc.consumed_at
FROM users u
JOIN auth_identities ai
    ON ai.user_id = u.id
JOIN pending_registrations pr
    ON pr.email = u.email
JOIN registration_continuations rc
    ON rc.pending_registration_id = pr.id
WHERE u.email = sqlc.arg(email);

-- name: CountUserSessions :one
SELECT COUNT(*) AS count
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: GetUnsortedCollectionsForUser :many
SELECT
    id,
    user_id,
    name,
    type,
    system_key
FROM collections
WHERE user_id = sqlc.arg(user_id)
  AND type = 'system'
  AND system_key = 'unsorted';

-- name: CountAllCollections :one
SELECT COUNT(*) AS count
FROM collections;

-- name: CreateExistingAuthIdentity :exec
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
);