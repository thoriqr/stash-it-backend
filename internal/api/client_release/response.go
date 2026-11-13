package client_release

import "time"

// MinSupportedReleaseResponse is the oldest build still allowed.
type MinSupportedReleaseResponse struct {
	Version string `json:"version" example:"1.3.0"`
	Build   int64  `json:"build" example:"3"`
}

// LatestReleaseResponse is the release a client is offered.
//
// Every field except release_notes is what the client needs to download and
// verify the artifact. release_notes is plain text for the client to render.
type LatestReleaseResponse struct {
	Version       string    `json:"version" example:"1.5.0"`
	Build         int64     `json:"build" example:"6"`
	DownloadURL   string    `json:"download_url" example:"https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk"`
	SHA256        string    `json:"sha256" example:"9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef"`
	FileSizeBytes int64     `json:"file_size_bytes" example:"48231001"`
	ReleaseNotes  string    `json:"release_notes" example:"- Perbaikan sinkronisasi koleksi"`
	PublishedAt   time.Time `json:"published_at" example:"2026-11-01T10:00:00Z"`
}

// CheckResponse is the decision for one client.
//
// Status is authoritative: none, optional or required. Latest and MinSupported
// are null only when no channel exists for the platform. When a channel exists
// they are present even if status is none, so a client can display the release it
// is already on.
type CheckResponse struct {
	Platform     string                       `json:"platform" example:"android"`
	Status       string                       `json:"status" example:"optional"`
	CurrentBuild int64                        `json:"current_build" example:"4"`
	MinSupported *MinSupportedReleaseResponse `json:"min_supported"`
	Latest       *LatestReleaseResponse       `json:"latest"`
}

// CheckAPIResponse is the documented envelope.
//
// There is no Meta field: httpx.OK emits {data, message}, and meta is only used
// by paginated endpoints through OKWithMeta. Declaring it here would document a
// key the endpoint never returns.
type CheckAPIResponse struct {
	Data    *CheckResponse `json:"data"`
	Message string         `json:"message" example:"client release check completed successfully"`
}
