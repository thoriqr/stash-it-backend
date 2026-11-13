package client_release

import "time"

// AdminReleaseResponse is one release as the operator sees it.
//
// It carries git_tag as well as the artifact fields: the operator uses the tag to
// trace the binary to a commit, and a client never receives this shape.
type AdminReleaseResponse struct {
	Version       string    `json:"version" example:"1.5.0"`
	Build         int64     `json:"build" example:"6"`
	DownloadURL   string    `json:"download_url" example:"https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk"`
	SHA256        string    `json:"sha256" example:"9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef"`
	FileSizeBytes int64     `json:"file_size_bytes" example:"48231001"`
	ReleaseNotes  string    `json:"release_notes" example:"- Perbaikan sinkronisasi koleksi"`
	GitTag        string    `json:"git_tag" example:"client-v1.5.0-6"`
	PublishedAt   time.Time `json:"published_at" example:"2026-11-01T10:00:00Z"`
}

// ReleaseRefResponse is a release named by the two values a policy reports.
type ReleaseRefResponse struct {
	Version string `json:"version" example:"1.5.0"`
	Build   int64  `json:"build" example:"6"`
}

// ReleasePolicyResponse is the state after a force or rollback.
type ReleasePolicyResponse struct {
	Latest       ReleaseRefResponse `json:"latest"`
	MinSupported ReleaseRefResponse `json:"min_supported"`
}

// PublishReleaseResponse reports the inserted release and the resulting policy.
type PublishReleaseResponse struct {
	Release      AdminReleaseResponse `json:"release"`
	Latest       ReleaseRefResponse   `json:"latest"`
	MinSupported ReleaseRefResponse   `json:"min_supported"`
	Forced       bool                 `json:"forced" example:"false"`
}

// ListReleasesResponse is a platform's release history and current policy.
//
// Releases is always an array, never null. Latest and MinSupported are null only
// when the platform has no channel yet.
type ListReleasesResponse struct {
	Releases     []AdminReleaseResponse `json:"releases"`
	Latest       *ReleaseRefResponse    `json:"latest"`
	MinSupported *ReleaseRefResponse    `json:"min_supported"`
}

type PublishReleaseAPIResponse struct {
	Data    *PublishReleaseResponse `json:"data"`
	Message string                  `json:"message" example:"client release published successfully"`
}

type ReleasePolicyAPIResponse struct {
	Data    *ReleasePolicyResponse `json:"data"`
	Message string                 `json:"message" example:"client release policy updated successfully"`
}

type ListReleasesAPIResponse struct {
	Data    *ListReleasesResponse `json:"data"`
	Message string                `json:"message" example:"client releases retrieved successfully"`
}
