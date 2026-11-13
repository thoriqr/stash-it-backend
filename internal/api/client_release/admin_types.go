package client_release

import "context"

// AdminRepository is the maintenance-side persistence contract.
//
// It embeds Repository because every mutation needs the current channel to
// validate the change, and the read query already answers that. The struct that
// implements it is the same one the public check uses; only the admin constructor
// supplies the pool needed for transactions.
type AdminRepository interface {
	Repository

	PublishRelease(
		ctx context.Context,
		params PublishReleaseParams,
	) (PublishReleaseResult, error)

	SetMinSupported(
		ctx context.Context,
		params ReleaseActionParams,
	) (ChannelPolicy, error)

	Rollback(
		ctx context.Context,
		params ReleaseActionParams,
	) (ChannelPolicy, error)

	ListReleases(
		ctx context.Context,
		platform string,
	) (ListReleasesResult, error)
}

// PublishReleaseParams is one release to record and offer.
type PublishReleaseParams struct {
	Platform      string
	Version       string
	Build         int64
	DownloadURL   string
	SHA256        string
	FileSizeBytes int64
	ReleaseNotes  string
	GitTag        string
	Force         bool
}

// PublishReleaseResult reports the inserted release and the resulting policy.
type PublishReleaseResult struct {
	Release Release
	Policy  ChannelPolicy
	Forced  bool
}

// ReleaseActionParams addresses one existing release by the pair an operator
// knows, rather than by an id the CLI would have to look up first.
type ReleaseActionParams struct {
	Platform string
	Build    int64
}

// ListReleasesResult is the platform's release history together with its current
// policy. Policy is nil when the platform has no channel yet.
type ListReleasesResult struct {
	Releases []Release
	Policy   *ChannelPolicy
}

// ChannelPolicy is the two bounds a platform currently reports, without the
// artifact fields that belong to the latest release alone.
type ChannelPolicy struct {
	Latest       ReleaseRef
	MinSupported ReleaseRef
}
