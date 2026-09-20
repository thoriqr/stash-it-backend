-- name: TruncatePasswordResetData :exec
TRUNCATE TABLE
    refresh_tokens,
    sessions,
    verification_codes,
    verification_requests,
    password_reset_continuations,
    pending_password_resets,
    password_credentials,
    users CASCADE;

-- name: CreatePasswordResetUser :one
INSERT INTO users (email, display_name, email_verified_at)
VALUES (sqlc.arg(email), 'Reset User', NOW())
RETURNING id;

-- name: CreatePasswordCredentialForUser :exec
INSERT INTO password_credentials (user_id, password_hash)
VALUES (sqlc.arg(user_id), sqlc.arg(password_hash));

-- name: SetVerificationCodeHash :exec
UPDATE verification_codes
SET code_hash = sqlc.arg(code_hash)
WHERE verification_request_id = sqlc.arg(verification_request_id)
  AND consumed_at IS NULL
  AND invalidated_at IS NULL;

-- name: GetPasswordResetState :one
SELECT
    ppr.id AS password_reset_id,
    ppr.status AS password_reset_status,
    vr.id AS verification_id,
    vc.id AS verification_code_id
FROM pending_password_resets ppr
JOIN verification_requests vr ON vr.subject_id = ppr.id
    AND vr.subject_type = 'pending_password_reset'
JOIN verification_codes vc ON vc.verification_request_id = vr.id
WHERE ppr.email = sqlc.arg(email);

-- name: CreateSessionForUser :one
INSERT INTO sessions (user_id, platform, absolute_expires_at)
VALUES (sqlc.arg(user_id), 'web', NOW() + INTERVAL '1 day')
RETURNING id;

-- name: GetPasswordResetFinalState :one
SELECT
    pc.password_hash,
    ppr.status AS password_reset_status,
    prc.consumed_at,
    COUNT(s.id) FILTER (WHERE s.revoked_at IS NULL)::bigint AS active_session_count
FROM users u
JOIN password_credentials pc ON pc.user_id = u.id
JOIN pending_password_resets ppr ON ppr.email = u.email
JOIN password_reset_continuations prc ON prc.pending_password_reset_id = ppr.id
LEFT JOIN sessions s ON s.user_id = u.id
WHERE u.email = sqlc.arg(email)
GROUP BY pc.password_hash, ppr.status, prc.consumed_at;
