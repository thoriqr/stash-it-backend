-- name: TruncateFeedback :exec
-- Feedback cascades from users, so this is stated for a suite that wants a clean
-- table without clearing every account.
TRUNCATE TABLE feedback CASCADE;

-- name: DeleteFeedbackForUser :exec
DELETE FROM feedback
WHERE user_id = sqlc.arg(user_id);

-- name: CreateFeedbackUser :one
-- Seeds an account only. Feedback tests need an owner and nothing else, so this
-- fills the two NOT NULL columns users carries beyond the id.
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

-- name: CountFeedbackForUser :one
SELECT COUNT(*) AS count
FROM feedback
WHERE user_id = sqlc.arg(user_id);

-- name: GetFeedbackRowForTest :one
-- Reads back what the create statement wrote, including the columns the API does
-- not report.
--
-- status and created_at are read from the row rather than compared against values
-- the test sent, which is what makes "the column default decided it" an assertion
-- about the database instead of about the test.
SELECT
    id,
    user_id,
    type,
    message,
    status,
    platform,
    app_version,
    app_build,
    created_at,
    updated_at
FROM feedback
WHERE id = sqlc.arg(id);

-- name: SetFeedbackStatusForTest :exec
-- Moves one report's status directly, so a test can arrange a filtered listing or
-- a triage state without a route that sets one.
UPDATE feedback
SET status = sqlc.arg(status)
WHERE id = sqlc.arg(id);
