package client_release

const (
	// CodeInvalidPlatform is returned when platform is absent, blank or not one
	// of the supported values. A platform that is valid but has no channel is not
	// an error: it is answered with status none.
	CodeInvalidPlatform = "INVALID_PLATFORM"

	// CodeInvalidClientBuild is returned when build is absent, not an integer, or
	// not positive. Nothing is defaulted: zero would force every client and the
	// latest build would hide updates.
	CodeInvalidClientBuild = "INVALID_CLIENT_BUILD"

	// CodeInvalidReleaseSecret is reported by the release maintenance guard. One
	// code covers a missing header, a wrong value and a deployment with no secret
	// configured; distinguishing them would tell an unauthenticated caller whether
	// the routes are enabled.
	CodeInvalidReleaseSecret = "INVALID_RELEASE_SECRET"

	// CodeInvalidReleaseMetadata is returned when a maintenance request is missing
	// a usable platform, version, build, URL, digest or size. The message names the
	// field; no internal detail is returned.
	CodeInvalidReleaseMetadata = "INVALID_RELEASE_METADATA"

	// CodeClientReleaseAlreadyExists is returned when the platform/build, the
	// platform/version, or the download URL is already recorded.
	CodeClientReleaseAlreadyExists = "CLIENT_RELEASE_ALREADY_EXISTS"

	// CodeReleaseNotFound is returned when a force or rollback names a build that
	// does not exist on the platform, or when the platform has no channel.
	CodeReleaseNotFound = "RELEASE_NOT_FOUND"

	// CodeInvalidReleasePolicy is returned when a publish, force or rollback would
	// break the invariant that the minimum supported build is not above the latest.
	CodeInvalidReleasePolicy = "INVALID_RELEASE_POLICY"
)
