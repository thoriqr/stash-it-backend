-- name: TruncateRegistrationData :exec
TRUNCATE TABLE
    verification_codes,
    verification_requests,
    registration_continuations,
    pending_registrations;

-- name: GetRegistrationState :one
SELECT
    pr.id,
    pr.email,
    pr.registration_type,
    pr.status,
    pr.expires_at,
    vr.id AS verification_id,
    vr.status AS verification_status,
    vc.id AS verification_code_id,
    vc.expires_at AS verification_code_expires_at
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
JOIN verification_codes vc
    ON vc.verification_request_id = vr.id
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
INSERT INTO verification_codes (verification_request_id, code_hash, expires_at)
SELECT id, 'expired-code', NOW() - INTERVAL '30 minutes' FROM verification
RETURNING verification_request_id;

-- name: GetRegistrationHistory :many
SELECT
    pr.id,
    pr.status,
    vr.id AS verification_id,
    vc.id AS verification_code_id
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
JOIN verification_codes vc ON vc.verification_request_id = vr.id
WHERE pr.email = sqlc.arg(email)
ORDER BY pr.created_at, pr.id;
