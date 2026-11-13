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
