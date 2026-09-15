-- name: GetUserForLogin :one
SELECT
    u.id,
    u.email,
    u.display_name,
    pc.password_hash
FROM users u
JOIN password_credentials pc
    ON pc.user_id = u.id
WHERE u.email = sqlc.arg(email);