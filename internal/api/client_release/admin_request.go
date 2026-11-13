package client_release

// PublishReleaseRequest is the body of POST
// /internal/maintenance/client-releases.
//
// The fields are validated by the service rather than by binding tags so every
// failure is reported as INVALID_RELEASE_METADATA with a message naming the
// field, instead of a generic validation error.
type PublishReleaseRequest struct {
	Platform      string `json:"platform" example:"android"`
	Version       string `json:"version" example:"1.5.0"`
	Build         int64  `json:"build" example:"6"`
	DownloadURL   string `json:"download_url" example:"https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk"`
	SHA256        string `json:"sha256" example:"9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef"`
	FileSizeBytes int64  `json:"file_size_bytes" example:"48231001"`
	ReleaseNotes  string `json:"release_notes" example:"- Perbaikan sinkronisasi koleksi"`
	GitTag        string `json:"git_tag" example:"client-v1.5.0-6"`

	// Force also makes the new release the minimum supported build. It is false by
	// default: publishing a release must not force every client unless the
	// operator says so.
	Force bool `json:"force" example:"false"`
}

// ReleaseActionRequest is the body of the force and rollback routes.
type ReleaseActionRequest struct {
	Platform string `json:"platform" example:"android"`
	Build    int64  `json:"build" example:"6"`
}

// ListReleasesRequest is the query for GET
// /internal/maintenance/client-releases.
type ListReleasesRequest struct {
	Platform string `query:"platform" example:"android"`
}
