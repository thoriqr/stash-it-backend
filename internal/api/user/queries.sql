-- name: GetUserByEmail :one
SELECT
    id,
    email
FROM users
WHERE email = sqlc.arg(email);


-- name: GetUserByID :one
SELECT
    id,
    email
FROM users
WHERE id = sqlc.arg(id);