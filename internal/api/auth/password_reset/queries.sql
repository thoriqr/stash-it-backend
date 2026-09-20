-- name: GetUserByEmail :one
SELECT
    id,
    email
FROM users
WHERE email = sqlc.arg(email);

-- name: GetActivePasswordResetByEmail :one
SELECT
    ppr.id,
    ppr.email,
    ppr.status,
    ppr.created_at,
    ppr.expires_at,
    vr.id AS verification_id
FROM pending_password_resets ppr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_password_reset'
    AND vr.subject_id = ppr.id
    AND vr.purpose = 'password_reset'
WHERE ppr.email = sqlc.arg(email)
  AND ppr.status = 'pending'
  AND ppr.expires_at > NOW()
LIMIT 1;

-- name: CreatePendingPasswordReset :one
INSERT INTO pending_password_resets (
    email,
    status,
    expires_at
)
VALUES (
    sqlc.arg(email),
    'pending',
    sqlc.arg(expires_at)
)
RETURNING
    id,
    email,
    status,
    created_at,
    expires_at;

-- name: GetPendingPasswordResetByEmailForUpdate :one
SELECT
    ppr.id,
    ppr.expires_at <= NOW() AS is_expired,
    vr.id AS verification_id
FROM pending_password_resets ppr
JOIN verification_requests vr
    ON vr.subject_type = 'pending_password_reset'
    AND vr.subject_id = ppr.id
    AND vr.purpose = 'password_reset'
WHERE ppr.email = sqlc.arg(email)
  AND ppr.status = 'pending'
FOR UPDATE;

-- name: ExpirePendingPasswordReset :execrows
UPDATE pending_password_resets
SET status = 'expired'
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND expires_at <= NOW();

-- name: CreateVerificationRequest :one
INSERT INTO verification_requests (
    subject_type,
    subject_id,
    purpose,
    status
)
VALUES (
    'pending_password_reset',
    sqlc.arg(subject_id),
    'password_reset',
    'pending'
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
)
VALUES (
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

    ppr.status AS password_reset_status,
    ppr.expires_at AS password_reset_expires_at
FROM verification_requests vr
JOIN pending_password_resets ppr
    ON ppr.id = vr.subject_id
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

-- name: CreatePasswordResetContinuation :one
INSERT INTO password_reset_continuations (
    pending_password_reset_id,
    token_hash,
    expires_at
)
VALUES (
    sqlc.arg(pending_password_reset_id),
    sqlc.arg(token_hash),
    sqlc.arg(expires_at)
)
RETURNING
    id,
    pending_password_reset_id,
    token_hash,
    expires_at,
    consumed_at,
    created_at;

-- name: GetPasswordResetContinuation :one
SELECT
    prc.id,
    prc.pending_password_reset_id,
    prc.token_hash,
    prc.expires_at,
    prc.consumed_at,
    prc.created_at,

    ppr.email,
    ppr.status AS password_reset_status,
    ppr.expires_at AS password_reset_expires_at,

    CASE
        WHEN pc.user_id IS NOT NULL THEN TRUE
        ELSE FALSE
    END AS has_password_credential
FROM password_reset_continuations prc
JOIN pending_password_resets ppr
    ON ppr.id = prc.pending_password_reset_id
JOIN users u
    ON u.email = ppr.email
LEFT JOIN password_credentials pc
    ON pc.user_id = u.id
WHERE prc.token_hash = sqlc.arg(token_hash);

-- name: UpsertPasswordCredential :exec
INSERT INTO password_credentials (
    user_id,
    password_hash
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(password_hash)
)
ON CONFLICT (user_id)
DO UPDATE SET
    password_hash = EXCLUDED.password_hash;

-- name: ConsumePasswordResetContinuation :execrows
UPDATE password_reset_continuations
SET consumed_at = NOW()
WHERE id = sqlc.arg(id)
  AND consumed_at IS NULL
  AND expires_at > NOW();

-- name: CompletePendingPasswordReset :execrows
UPDATE pending_password_resets
SET status = 'completed'
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND expires_at > NOW();

-- name: RevokeAllSessionsForUser :exec
UPDATE sessions
SET revoked_at = NOW()
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;