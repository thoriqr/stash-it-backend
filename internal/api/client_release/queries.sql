-- name: GetClientReleaseChannel :one
-- Reads the single channel row for a platform, together with both releases it
-- points at.
--
-- The pointers are the published state. There is deliberately no ORDER BY build
-- here: the latest release is whatever the channel says it is, which is what makes
-- a rollback a pointer move rather than a new row. The composite foreign keys on
-- client_release_channels guarantee that both joined rows belong to the same
-- platform as the channel.
--
-- Only version and build are projected from the minimum release. The API reports
-- min_supported as those two values; the artifact URL and hash belong to the
-- latest release alone.
SELECT
    channels.platform,
    latest.version AS latest_version,
    latest.build AS latest_build,
    latest.download_url,
    latest.sha256,
    latest.file_size_bytes,
    latest.release_notes,
    latest.published_at,
    minimum.version AS min_supported_version,
    minimum.build AS min_supported_build
FROM client_release_channels AS channels
JOIN client_releases AS latest
  ON latest.id = channels.latest_release_id
JOIN client_releases AS minimum
  ON minimum.id = channels.min_supported_release_id
WHERE channels.platform = sqlc.arg(platform);

-- name: InsertClientRelease :one
-- Inserts one immutable release row.
--
-- ON CONFLICT DO NOTHING is deliberate: every unique constraint on the table
-- means the same thing to the operator — this release or artifact is already
-- recorded. A suppressed conflict returns no row, which the repository maps to
-- CLIENT_RELEASE_ALREADY_EXISTS. Constraint names are not read into the API.
INSERT INTO client_releases (
    platform,
    version,
    build,
    download_url,
    sha256,
    file_size_bytes,
    release_notes,
    git_tag
) VALUES (
    sqlc.arg(platform),
    sqlc.arg(version),
    sqlc.arg(build),
    sqlc.arg(download_url),
    sqlc.arg(sha256),
    sqlc.arg(file_size_bytes),
    sqlc.arg(release_notes),
    sqlc.arg(git_tag)
)
ON CONFLICT DO NOTHING
RETURNING
    id,
    platform,
    version,
    build,
    download_url,
    sha256,
    file_size_bytes,
    release_notes,
    git_tag,
    published_at;

-- name: GetClientReleaseChannelForUpdate :one
-- Locks the platform's channel row for a read-modify-write.
--
-- Only the channel row is locked: the release rows it points at are immutable, so
-- there is nothing to gain from locking them. The returned ids are what an update
-- writes back when the release is deliberately kept rather than replaced.
SELECT
    channels.platform,
    channels.latest_release_id,
    channels.min_supported_release_id,
    latest.version AS latest_version,
    latest.build AS latest_build,
    minimum.version AS min_supported_version,
    minimum.build AS min_supported_build
FROM client_release_channels AS channels
JOIN client_releases AS latest
  ON latest.id = channels.latest_release_id
JOIN client_releases AS minimum
  ON minimum.id = channels.min_supported_release_id
WHERE channels.platform = sqlc.arg(platform)
FOR UPDATE OF channels;

-- name: InsertClientReleaseChannel :exec
-- Creates a platform's channel with the first release as both bounds.
INSERT INTO client_release_channels (
    platform,
    latest_release_id,
    min_supported_release_id
) VALUES (
    sqlc.arg(platform),
    sqlc.arg(latest_release_id),
    sqlc.arg(min_supported_release_id)
);

-- name: UpdateClientReleaseChannelPolicy :exec
-- Moves one or both pointers. The caller passes the value it wants to keep
-- unchanged for the other pointer.
UPDATE client_release_channels
SET latest_release_id = sqlc.arg(latest_release_id),
    min_supported_release_id = sqlc.arg(min_supported_release_id)
WHERE platform = sqlc.arg(platform);

-- name: GetClientReleaseByPlatformAndBuild :one
-- Resolves a release for force and rollback actions, which address it by the
-- pair an operator knows.
SELECT
    id,
    platform,
    version,
    build,
    download_url,
    sha256,
    file_size_bytes,
    release_notes,
    git_tag,
    published_at
FROM client_releases
WHERE platform = sqlc.arg(platform)
  AND build = sqlc.arg(build);

-- name: ListClientReleases :many
-- Lists a platform's releases newest first for the operator.
SELECT
    id,
    platform,
    version,
    build,
    download_url,
    sha256,
    file_size_bytes,
    release_notes,
    git_tag,
    published_at
FROM client_releases
WHERE platform = sqlc.arg(platform)
ORDER BY build DESC;
