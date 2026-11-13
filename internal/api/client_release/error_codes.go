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
)
