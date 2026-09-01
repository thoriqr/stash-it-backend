-- name: CreateUser :one
INSERT INTO users (
  email
) VALUES (
  sqlc.arg(email)
)
RETURNING id, email;

-- name: GetUserByID :one
SELECT 
    id,
    email
FROM users
WHERE id = sqlc.arg(id);

-- name: GetUserByEmail :one
SELECT
    id,
    email
FROM users
WHERE email = sqlc.arg(email);

-- name: ListUsers :many
SELECT 
    id,
    email
FROM users
ORDER BY id;