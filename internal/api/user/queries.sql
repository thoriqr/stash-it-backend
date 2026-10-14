-- name: GetUserProfile :one
-- Reads the profile of exactly one user, together with whether that user holds a
-- password credential.
--
-- The credential check is an EXISTS rather than a JOIN so a user without one
-- still produces a row: a missing credential is an ordinary state of an account
-- (a social registration creates none), not a failed lookup. A LEFT JOIN and a
-- CASE, as GetPasswordResetContinuation uses, would report the same thing more
-- verbosely.
--
-- Scoped by id alone. The caller of this query is always the authenticated user,
-- and the id it passes is read from the verified access token's sub claim rather
-- than from the request, so there is no caller-chosen identifier to scope against
-- — see internal/api/user/handler.go.
SELECT
    u.id,
    u.email,
    u.display_name,
    u.created_at,
    EXISTS (
        SELECT 1
        FROM password_credentials pc
        WHERE pc.user_id = u.id
    ) AS has_password_credential
FROM users u
WHERE u.id = sqlc.arg(user_id);


-- name: ListAuthIdentitiesForUser :many
-- Lists the authentication providers linked to one user.
--
-- provider_subject is the credential the identity is bound to, so it is still
-- not selected: nothing this feature's caller can be shown includes it.
--
-- The two snapshot columns are selected and are nullable. They are the values
-- the provider asserted at the moment the link was made, kept deliberately as
-- historical metadata: migration 000026 removed the duplicate address from
-- account_link_confirmations precisely because the account's own email is the
-- authoritative one, and these are not it. They are reported as stored, null
-- included — a NULL snapshot is a real state, not a missing value to fill in.
--
-- provider is returned as stored. The column is free text with no check
-- constraint and no curated registry, so a client renders whatever arrives rather
-- than switching over a set the server maintains.
--
-- Ordered by created_at then id. created_at defaults to NOW(), which is constant
-- within a transaction, so equal timestamps are a real state and the tie-break is
-- load-bearing rather than decorative: auth_identities.id is uuidv7() and unique.
-- id is referenced by the ORDER BY and deliberately not projected.
SELECT
    ai.provider,
    ai.created_at AS linked_at,
    ai.email_snapshot,
    ai.display_name_snapshot
FROM auth_identities ai
WHERE ai.user_id = sqlc.arg(user_id)
ORDER BY ai.created_at ASC, ai.id ASC;


-- name: UpdateUserDisplayName :one
-- Renames one user.
--
-- The returned row is the row as written, inside the statement that wrote it, so
-- the response cannot disagree with what was persisted and cannot be raced by a
-- concurrent rename — which is strictly better than committing and re-reading,
-- where a second request landing between the two would report a name this
-- request did not set.
--
-- Scoped by id alone for the reason given on GetUserProfile.
--
-- updated_at is not named here. The users_set_updated_at trigger sets it before
-- this clause runs, so RETURNING already reports the new value.
UPDATE users
SET display_name = sqlc.arg(display_name)
WHERE id = sqlc.arg(user_id)
RETURNING
    id,
    email,
    display_name,
    created_at,
    EXISTS (
        SELECT 1
        FROM password_credentials pc
        WHERE pc.user_id = users.id
    ) AS has_password_credential;


-- name: GetPasswordCredentialForUser :one
-- Reads the stored hash for one user's password credential.
--
-- No row is a legitimate state, not a fault: an account created through a
-- provider has no credential until it sets one. The repository turns
-- pgx.ErrNoRows into a conflict the caller can act on rather than a 404, because
-- "you have no password yet, use the other request shape" is not a missing
-- resource.
--
-- This read is never what authorizes the write that follows it. It supplies the
-- hash to verify a submitted password against; the write itself is authorized by
-- an INSERT that the primary key refuses, or by an UPDATE whose predicate carries
-- this hash. See internal/api/user/service.go.
SELECT
    pc.password_hash
FROM password_credentials pc
WHERE pc.user_id = sqlc.arg(user_id);


-- name: CreatePasswordCredential :one
-- Creates one user's password credential.
--
-- A plain INSERT on purpose. The credential's primary key is user_id, so a second
-- row for the same account is structurally impossible, and a concurrent creator
-- loses here and is told so.
--
-- The password-reset flow writes this table with INSERT ... ON CONFLICT DO
-- UPDATE, which is correct there and wrong here. A reset has already proven
-- mailbox control and does not need to know whether a credential existed; this
-- flow does need to know, because an account that has one must verify the
-- current password before it is replaced. An upsert would make two concurrent
-- creators both succeed, the second silently overwriting the first, and both
-- callers would be told they had set the password they submitted.
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
    created_at,
    updated_at;


-- name: UpdatePasswordCredentialIfUnchanged :execrows
-- Replaces one user's password hash, but only while it still holds the value the
-- caller verified against.
--
-- previous_password_hash is the hash this request read and verified a submitted
-- password against. Carrying it in the predicate is what makes the write a
-- check-and-act in one statement: two requests changing the same password both
-- verify against the same row, and without it the second would overwrite the
-- first invisibly. With it the second matches no row and is refused.
--
-- The caller is the credential's owner by construction — the hash it passes was
-- read under this same user id — so the user_id predicate is a scope, not a
-- permission the caller could have chosen differently.
UPDATE password_credentials
SET password_hash = sqlc.arg(password_hash)
WHERE user_id = sqlc.arg(user_id)
  AND password_hash = sqlc.arg(previous_password_hash);


-- name: RevokeOtherSessionsForUser :execrows
-- Revokes every active session of one user except one.
--
-- excluded_session_id comes from the verified access token's sid claim, which the
-- verifier has already established is present and non-zero. It is never supplied
-- by the caller, and the user id is the token's sub claim, so the pair cannot be
-- aimed at another account: a user_id that did not match would match nothing.
--
-- `id <>` rather than `id IS DISTINCT FROM` is safe because the value is never
-- NULL and uuid.Nil is not something uuidv7() produces, so the exclusion cannot
-- widen to every row.
--
-- `revoked_at IS NULL` keeps this idempotent and matches the reasoning already
-- recorded on session.RevokeSession: a session already revoked needs no write,
-- and a request that finds nothing to revoke has still asked for the right thing.
UPDATE sessions
SET revoked_at = NOW()
WHERE user_id = sqlc.arg(user_id)
  AND id <> sqlc.arg(excluded_session_id)
  AND revoked_at IS NULL;