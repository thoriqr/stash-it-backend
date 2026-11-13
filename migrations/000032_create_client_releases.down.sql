-- Drop the channel table first: its composite foreign keys reference
-- client_releases.
DROP TABLE IF EXISTS client_release_channels;
DROP TABLE IF EXISTS client_releases;
