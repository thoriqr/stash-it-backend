-- Client release metadata and per-platform policy.
--
-- client_releases holds immutable artifacts: one row per platform and build.
-- client_release_channels holds the mutable policy for each platform: which
-- release is currently offered and which build is the minimum still supported.
--
-- min_supported is a pointer to a release rather than a copied version string or
-- integer. That keeps it a real, downloadable artifact with a version to display,
-- and it means publishing a release never silently changes the minimum.
--
-- The composite foreign keys require UNIQUE (id, platform). id is already the
-- primary key, so the pair is unique by construction; the constraint exists
-- because a composite foreign key needs a target of exactly that shape. It also
-- prevents a channel from pointing at a release of another platform.

CREATE TABLE client_releases (
    id UUID PRIMARY KEY DEFAULT uuidv7(),

    platform TEXT NOT NULL,
    version  TEXT NOT NULL,
    build    INTEGER NOT NULL,

    download_url    TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    release_notes   TEXT NOT NULL,
    git_tag         TEXT NOT NULL,

    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT client_releases_platform_check
        CHECK (platform IN ('android', 'windows')),

    -- version is the human-facing semver, without the "+build" suffix. The build
    -- number travels separately in build and is the only value clients compare.
    CONSTRAINT client_releases_version_check
        CHECK (version ~ '^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)$'),

    CONSTRAINT client_releases_build_check
        CHECK (build > 0),

    -- Lowercase hex. The client compares the downloaded bytes against this value
    -- before installing.
    CONSTRAINT client_releases_sha256_check
        CHECK (sha256 ~ '^[0-9a-f]{64}$'),

    CONSTRAINT client_releases_size_check
        CHECK (file_size_bytes > 0),

    CONSTRAINT client_releases_download_url_https_check
        CHECK (download_url ~ '^https://'),

    CONSTRAINT client_releases_platform_build_unique
        UNIQUE (platform, build),

    CONSTRAINT client_releases_platform_version_unique
        UNIQUE (platform, version),

    CONSTRAINT client_releases_download_url_unique
        UNIQUE (download_url),

    CONSTRAINT client_releases_id_platform_unique
        UNIQUE (id, platform)
);

CREATE TRIGGER client_releases_set_updated_at
BEFORE UPDATE ON client_releases
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();


CREATE TABLE client_release_channels (
    platform TEXT PRIMARY KEY,

    latest_release_id        UUID NOT NULL,
    min_supported_release_id UUID NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT client_release_channels_platform_check
        CHECK (platform IN ('android', 'windows')),

    CONSTRAINT client_release_channels_latest_fkey
        FOREIGN KEY (latest_release_id, platform)
        REFERENCES client_releases (id, platform)
        ON DELETE RESTRICT,

    CONSTRAINT client_release_channels_min_supported_fkey
        FOREIGN KEY (min_supported_release_id, platform)
        REFERENCES client_releases (id, platform)
        ON DELETE RESTRICT
);

CREATE TRIGGER client_release_channels_set_updated_at
BEFORE UPDATE ON client_release_channels
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
