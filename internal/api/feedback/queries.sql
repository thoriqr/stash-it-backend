-- name: CreateFeedback :one
-- Writes one feedback row and returns it as it was stored.
--
-- The insert names every column it fills and lets the defaults supply the rest:
-- status comes from the column default, so this statement cannot disagree with
-- what the schema says a new report is. created_at and updated_at are the clock's.
--
-- The returned row is the one the database wrote, not an echo of the arguments.
-- A create response reporting a status or timestamp this code assumed would stop
-- being true the moment the defaults changed.
INSERT INTO feedback (
    user_id,
    type,
    message,
    platform,
    app_version,
    app_build
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(type),
    sqlc.arg(message),
    sqlc.arg(platform),
    sqlc.arg(app_version),
    sqlc.arg(app_build)
)
RETURNING
    id,
    user_id,
    type,
    message,
    status,
    platform,
    app_version,
    app_build,
    created_at,
    updated_at;
