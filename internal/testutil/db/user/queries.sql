-- name: TruncateUserData :exec
TRUNCATE TABLE
    refresh_tokens,
    sessions,
    password_credentials,
    auth_identities,
    users
CASCADE;

-- name: CreateUser :one
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

-- name: CreatePasswordCredentialForUser :exec
INSERT INTO password_credentials (
    user_id,
    password_hash
)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(password_hash)
);

-- name: CreateAuthIdentityForUser :exec
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

-- name: CreateSessionForUser :one
INSERT INTO sessions (
    user_id,
    platform,
    absolute_expires_at
)
VALUES (
    sqlc.arg(user_id),
    'web',
    NOW() + INTERVAL '1 day'
)
RETURNING id;

-- name: RevokeSession :exec
-- Used to arrange a session that is already revoked, so a revocation test can
-- tell "was left alone because it was already revoked" apart from "was never
-- matched at all".
UPDATE sessions
SET revoked_at = NOW()
WHERE id = sqlc.arg(id);

-- name: GetPasswordCredentialHashForUser :one
SELECT password_hash
FROM password_credentials
WHERE user_id = sqlc.arg(user_id);

-- name: CountPasswordCredentialsForUser :one
SELECT COUNT(*) AS count
FROM password_credentials
WHERE user_id = sqlc.arg(user_id);

-- name: CountActiveSessionsForUser :one
SELECT COUNT(*) AS count
FROM sessions
WHERE user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL;

-- name: IsSessionRevoked :one
-- The explicit boolean cast is load-bearing for the generated type: without it
-- sqlc cannot infer what the expression evaluates to and emits interface{},
-- which every caller then has to assert on by hand.
SELECT (revoked_at IS NOT NULL)::boolean AS revoked
FROM sessions
WHERE id = sqlc.arg(id);

-- name: GetUserDisplayName :one
SELECT display_name
FROM users
WHERE id = sqlc.arg(id);

-- name: GetUserRowForTest :one
-- Reads the parts of a users row a profile test compares across a write.
--
-- created_at and email assert that a rename touched nothing else, and updated_at
-- is what shows the users_set_updated_at trigger actually fired. Comparing that
-- against the database's own clock rather than a value the test captured is what
-- makes the assertion safe.
SELECT
    email,
    display_name,
    created_at,
    updated_at
FROM users
WHERE id = sqlc.arg(id);

-- name: ReplacePasswordCredentialHash :exec
-- Writes straight past the conditional the production update carries.
--
-- It exists so a test can put a credential somewhere the caller's verified hash
-- no longer matches, which is the state the conditional update exists to refuse.
-- Driving it through an endpoint would not reach that state: a change always
-- succeeds when the current password is right.
UPDATE password_credentials
SET password_hash = sqlc.arg(password_hash)
WHERE user_id = sqlc.arg(user_id);