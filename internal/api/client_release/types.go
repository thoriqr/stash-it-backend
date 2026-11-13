package client_release

import (
	"context"
	"time"
)

// Repository reads the release channel a check is answered from.
//
// It is consumer-defined rather than exported by the persistence package,
// matching the other features in this repository. The only method it needs is the
// one read: this endpoint cannot publish anything.
type Repository interface {
	GetChannel(ctx context.Context, platform string) (*Channel, error)
}

// Channel is the published policy for one platform.
//
// Latest is the release currently offered. MinSupported is the oldest build that
// may still be used; it is a reference rather than an integer so it always names
// a real artifact and carries the version a client can be shown.
type Channel struct {
	Platform     string
	Latest       Release
	MinSupported ReleaseRef
}

// Release is one published artifact.
type Release struct {
	Version       string
	Build         int64
	DownloadURL   string
	SHA256        string
	FileSizeBytes int64
	ReleaseNotes  string
	GitTag        string
	PublishedAt   time.Time
}

// ReleaseRef identifies one published release by the two values the check reports
// for the minimum supported build.
type ReleaseRef struct {
	Version string
	Build   int64
}
