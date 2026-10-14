-- name: TruncateSessionData :exec
TRUNCATE TABLE
    refresh_tokens,
    sessions,
    users
CASCADE;

-- name: CreateSessionUser :one
INSERT INTO users (
    email,
    display_name,
    email_verified_at
) VALUES (
    sqlc.arg(email),
    sqlc.arg(display_name),
    NOW()
)
RETURNING id;

-- name: CreateTestSession :one
INSERT INTO sessions (
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    absolute_expires_at,
    revoked_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(platform),
    sqlc.arg(installation_id),
    sqlc.arg(device_name),
    sqlc.arg(user_agent),
    sqlc.arg(absolute_expires_at),
    sqlc.arg(revoked_at)
)
RETURNING
    id,
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    created_at,
    last_activity_at,
    absolute_expires_at,
    revoked_at;


-- name: CreateTestRefreshToken :one
INSERT INTO refresh_tokens (
    session_id,
    token_hash,
    replaced_by
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(token_hash),
    sqlc.arg(replaced_by)
)
RETURNING
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by;

-- name: GetRefreshTokenState :one
SELECT
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by
FROM refresh_tokens
WHERE id = sqlc.arg(id);

-- name: GetRefreshTokensForSession :many
-- Every token issued for one session, used to check that none of them survived
-- a revocation that raced with a rotation.
SELECT
    id,
    session_id,
    token_hash,
    issued_at,
    replaced_by
FROM refresh_tokens
WHERE session_id = sqlc.arg(session_id)
ORDER BY issued_at, id;

-- name: GetSessionState :one
SELECT
    id,
    user_id,
    platform,
    installation_id,
    device_name,
    user_agent,
    created_at,
    last_activity_at,
    absolute_expires_at,
    revoked_at
FROM sessions
WHERE id = sqlc.arg(id);

-- name: UpdateTestSessionLastActivity :exec
UPDATE sessions
SET last_activity_at = sqlc.arg(last_activity_at)
WHERE id = sqlc.arg(id);

-- name: CountAllSessionsForUser :one
-- Every session row a user has, revoked ones included.
--
-- CountUserSessions in the login and registration test packages filters on
-- revoked_at IS NULL, which is right for asserting that a login created exactly
-- one usable session and wrong for asserting that a failed CreateSession left
-- nothing behind: the row a rollback has to remove is not revoked, so either
-- count would find it, but a test about orphan rows should not depend on that
-- coincidence to notice one.
SELECT COUNT(*)
FROM sessions
WHERE user_id = sqlc.arg(user_id);

-- name: SeedTestSessionsBulk :exec
-- Creates many sessions for one user at once, which is what makes it practical to
-- exercise a batch boundary: a cleanup batch is five hundred rows, so proving the
-- loop runs more than one of them needs at least that many rows to delete.
--
-- last_activity_at is set explicitly because the default is NOW() and every row
-- inserted together would otherwise share one timestamp. idle_ago is an interval
-- so a test can say "this one is idle-expired" without restating the window the
-- production code compares against.
--
-- absolute_in stays positive and revoked_at stays NULL: this exists to produce
-- rows that are eligible on one condition and otherwise entirely ordinary, so the
-- bulk path cannot quietly succeed by deleting sessions that were dead for some
-- other reason.
INSERT INTO sessions (
    user_id,
    platform,
    absolute_expires_at,
    last_activity_at
)
SELECT
    sqlc.arg(user_id)                              AS user_id,
    'web'                                          AS platform,
    NOW() + sqlc.arg(absolute_in)::interval        AS absolute_expires_at,
    NOW() - sqlc.arg(idle_ago)::interval           AS last_activity_at
FROM generate_series(1, sqlc.arg(session_count)::bigint) AS series(n);

-- name: CountAllRefreshTokens :one
-- Every refresh token in the table, across all users.
--
-- Scoped per user this would miss exactly the thing a cascade test is checking:
-- whether the tokens that went with a deleted session really went with it, rather
-- than merely whether the session row did. Counting the whole table also catches a
-- cascade that reached further than the session it was supposed to.
SELECT COUNT(*)
FROM refresh_tokens;