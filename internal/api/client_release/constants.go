package client_release

const (
	// PlatformAndroid and PlatformWindows are the values Flutter's
	// Platform.operatingSystem reports, and the only values the schema accepts.
	PlatformAndroid = "android"
	PlatformWindows = "windows"

	// StatusNone means there is nothing to install: either no release exists for
	// the platform, or the client is already on the latest build.
	StatusNone = "none"

	// StatusOptional means a newer release exists but the client is still above
	// the minimum supported build.
	StatusOptional = "optional"

	// StatusRequired means the client's build is below the minimum supported build.
	StatusRequired = "required"

	// releaseSecretHeader carries the shared secret that guards the internal
	// client-release maintenance routes. It follows the X-Cleanup-Secret family.
	releaseSecretHeader = "X-Release-Secret"
)
