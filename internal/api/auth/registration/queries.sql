-- name: CreatePendingRegistration :one
INSERT INTO pending_registrations (
    email,
    registration_type,
    status,
    expires_at
) VALUES (
    sqlc.arg(email),
    sqlc.arg(registration_type),
    sqlc.arg(status),
    sqlc.arg(expires_at)
)
RETURNING
    id,
    email,
    registration_type,
    status,
    created_at,
    expires_at;


-- name: CreateVerificationRequest :one
INSERT INTO verification_requests (
    subject_type,
    subject_id,
    purpose,
    status
) VALUES (
    sqlc.arg(subject_type),
    sqlc.arg(subject_id),
    sqlc.arg(purpose),
    sqlc.arg(status)
)
RETURNING
    id,
    subject_type,
    subject_id,
    purpose,
    status,
    resend_count,
    last_sent_at,
    created_at;


-- name: CreateVerificationCode :one
INSERT INTO verification_codes (
    verification_request_id,
    code_hash,
    expires_at
) VALUES (
    sqlc.arg(verification_request_id),
    sqlc.arg(code_hash),
    sqlc.arg(expires_at)
)
RETURNING
    id,
    verification_request_id,
    code_hash,
    attempts,
    created_at,
    expires_at,
    consumed_at,
    invalidated_at;

-- name: GetVerification :one
SELECT
    vr.id,
    vr.subject_type,
    vr.subject_id,
    vr.purpose,
    vr.status,
    vr.resend_count,
    vr.last_sent_at,
    vr.created_at,

    pr.status AS registration_status,
    pr.expires_at AS registration_expires_at
FROM verification_requests vr
JOIN pending_registrations pr
    ON pr.id = vr.subject_id
WHERE vr.id = sqlc.arg(id);

-- name: InvalidateVerificationCode :exec
UPDATE verification_codes
SET invalidated_at = NOW()
WHERE id = (
    SELECT vc.id
    FROM verification_codes vc
    WHERE vc.verification_request_id = sqlc.arg(verification_request_id)
      AND vc.consumed_at IS NULL
      AND vc.invalidated_at IS NULL
    ORDER BY vc.created_at DESC
    LIMIT 1
);

-- name: UpdateVerificationRequestResend :one
UPDATE verification_requests
SET
    resend_count = resend_count + 1,
    last_sent_at = NOW()
WHERE id = sqlc.arg(id)
RETURNING
    id,
    subject_type,
    subject_id,
    purpose,
    status,
    resend_count,
    last_sent_at,
    created_at;

-- name: GetActiveVerificationCode :one
SELECT
    id,
    verification_request_id,
    code_hash,
    attempts,
    created_at,
    expires_at,
    consumed_at,
    invalidated_at
FROM verification_codes
WHERE verification_request_id = sqlc.arg(verification_request_id)
  AND consumed_at IS NULL
  AND invalidated_at IS NULL
  AND expires_at > NOW()
  AND attempts < sqlc.arg(max_attempts)
ORDER BY created_at DESC
LIMIT 1;

-- name: IncrementVerificationCodeAttempts :one
UPDATE verification_codes
SET attempts = attempts + 1
WHERE id = sqlc.arg(id)
  AND attempts < sqlc.arg(max_attempts)
RETURNING attempts;

-- name: ConsumeVerificationCode :execrows
UPDATE verification_codes
SET consumed_at = NOW()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND invalidated_at IS NULL
  AND expires_at > NOW();

-- name: MarkVerificationRequestVerified :execrows
UPDATE verification_requests
SET status = 'verified'
WHERE id = sqlc.arg(id)
  AND status = 'pending';

-- name: CreateRegistrationContinuation :one
INSERT INTO registration_continuations (
    pending_registration_id,
    token_hash,
    expires_at
)
VALUES (
    sqlc.arg(pending_registration_id),
    sqlc.arg(token_hash),
    sqlc.arg(expires_at)
)
RETURNING
    id,
    pending_registration_id,
    token_hash,
    expires_at,
    consumed_at,
    created_at;

-- name: GetRegistrationByEmail :one
SELECT
    pr.id,
    pr.email,
    pr.registration_type,
    pr.status,
    pr.created_at,
    pr.expires_at,
    vr.id AS verification_id
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
WHERE pr.email = sqlc.arg(email)
  AND pr.status IN ('pending', 'completed');

-- name: GetPendingRegistrationByEmailForUpdate :one
SELECT
    pr.id,
    pr.expires_at,
    pr.expires_at <= NOW() AS is_expired,
    vr.id AS verification_id
FROM pending_registrations pr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_registration'
    AND vr.subject_id = pr.id
    AND vr.purpose = 'registration'
WHERE pr.email = sqlc.arg(email)
  AND pr.status = 'pending'
FOR UPDATE;

-- name: ExpirePendingRegistration :execrows
UPDATE pending_registrations
SET status = 'expired'
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND expires_at <= NOW();

-- name: GetRegistrationContinuation :one
SELECT
    rc.id,
    rc.pending_registration_id,
    rc.token_hash,
    rc.expires_at,
    rc.consumed_at,
    rc.created_at,

    pr.email,
    pr.registration_type,
    pr.status AS registration_status,
    pr.expires_at AS registration_expires_at
FROM registration_continuations rc
JOIN pending_registrations pr
    ON pr.id = rc.pending_registration_id
WHERE rc.token_hash = sqlc.arg(token_hash);

-- name: CreateUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
)
VALUES (
    sqlc.arg(email),
    sqlc.arg(display_name),
    sqlc.arg(email_verified_at)
)
RETURNING
    id,
    email,
    display_name,
    email_verified_at,
    created_at,
    updated_at;

-- name: CreatePasswordCredential :one
INSERT INTO password_credentials (
    user_id,
    password_hash
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(password_hash)
)
RETURNING
    user_id,
    password_hash,
    created_at,
    updated_at;

-- name: ConsumeRegistrationContinuation :execrows
UPDATE registration_continuations
SET consumed_at = NOW()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND expires_at > NOW();

-- name: CompletePendingRegistration :execrows
UPDATE pending_registrations
SET status = 'completed'
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND expires_at > NOW();
