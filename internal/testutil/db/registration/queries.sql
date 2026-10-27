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

-- name: CreateSocialPendingRegistration :one
--
-- A pending social registration, the identity that opened it, and the
-- registration verification request, with no continuation.
--
-- The verification request is not decoration.
-- GetPendingRegistrationByEmailForUpdate inner-joins verification_requests, so a
-- seeded registration without one is invisible to CreateSocialRegistration: the
-- lookup finds nothing and the flow falls through to the insert. Omitting it
-- would produce a fixture the production code cannot see, and the case would
-- pass for the wrong reason.
--
-- The lifetime is the feature's own registrationExpiresIn so a seeded
-- registration is indistinguishable from one the flow created. An address past
-- its deadline is a separate query, because that is a different case rather than
-- a different argument to this one.
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
    RETURNING pending_registration_id
),
verification_request AS (
    INSERT INTO verification_requests (
        subject_type,
        subject_id,
        purpose,
        status
    )
    SELECT
        'pending_registration',
        pending_registration_id,
        'registration',
        'pending'
    FROM social_identity
    RETURNING id
)
SELECT id AS verification_id FROM verification_request;

-- name: CreateExpiredSocialPendingRegistration :one
--
-- The same state, already past its deadline. Written out rather than derived
-- from the query above because a sqlc query name is not a SQL function. What the
-- case is testing then reads as the word expired, not as a negative duration.
--
-- status stays 'pending' past the deadline because nothing reaps it, which is
-- exactly the state the expired-registration mismatch case depends on.
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
        NOW() - INTERVAL '1 hour'
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
    RETURNING pending_registration_id
),
verification_request AS (
    INSERT INTO verification_requests (
        subject_type,
        subject_id,
        purpose,
        status
    )
    SELECT
        'pending_registration',
        pending_registration_id,
        'registration',
        'pending'
    FROM social_identity
    RETURNING id
)
SELECT id AS verification_id FROM verification_request;

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

-- name: GetSocialPendingRegistrationStates :many
--
-- Every pending social registration for an address, with the identity that opened
-- it, ordered by creation so a before/after comparison reads in a stable order.
--
-- It is a :many because "exactly one" is the property under test: a decision that
-- silently created a second registration would be invisible to a query returning a
-- single row.
SELECT
    pr.id,
    pr.email,
    pr.registration_type,
    pr.status,
    pr.expires_at,
    psi.provider,
    psi.provider_subject,
    psi.email_snapshot,
    vr.id AS verification_id,
    vr.status AS verification_status
FROM pending_registrations pr
LEFT JOIN pending_social_identities psi
    ON psi.pending_registration_id = pr.id
LEFT JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
WHERE pr.email = sqlc.arg(email)
ORDER BY pr.created_at, pr.id;

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

-- name: GetVerificationRequestsForEmail :many
SELECT
    vr.id,
    vr.subject_id,
    vr.subject_type,
    vr.purpose,
    vr.status,
    vr.pin_issued_count,
    pr.status AS registration_status
FROM verification_requests vr
JOIN pending_registrations pr
    ON pr.id = vr.subject_id
WHERE pr.email = sqlc.arg(email)
ORDER BY vr.created_at, vr.id;

-- name: CountContinuationsForPendingRegistration :one
SELECT COUNT(*) AS count
FROM registration_continuations
WHERE pending_registration_id = sqlc.arg(pending_registration_id);

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