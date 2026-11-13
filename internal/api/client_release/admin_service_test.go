package client_release_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/client_release"
	"github.com/thoriqr/stash-it-backend/internal/api/client_release/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func newAdminService(
	t *testing.T,
) (client_release.AdminService, *mocks.MockAdminRepository) {
	t.Helper()

	repo := mocks.NewMockAdminRepository(gomock.NewController(t))

	return client_release.NewAdminService(repo, zap.NewNop()), repo
}

func validPublishReleaseRequest() client_release.PublishReleaseRequest {
	return client_release.PublishReleaseRequest{
		Platform:      "android",
		Version:       "1.5.0",
		Build:         6,
		DownloadURL:   "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
		SHA256:        strings.Repeat("a", 64),
		FileSizeBytes: 48231001,
		ReleaseNotes:  "- notes",
		GitTag:        "client-v1.5.0-6",
	}
}

func TestAdminService_PublishRelease_Validation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*client_release.PublishReleaseRequest)
		code   string
	}{
		{
			name: "blank platform",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Platform = ""
			},
			code: client_release.CodeInvalidPlatform,
		},
		{
			name: "unknown platform",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Platform = "macos"
			},
			code: client_release.CodeInvalidPlatform,
		},
		{
			name: "non-semver version",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Version = "1.5"
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "leading zero version",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Version = "1.05.0"
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "zero build",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Build = 0
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "negative build",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Build = -1
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "build above int32",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.Build = 1 << 31
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "http download url",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.DownloadURL = "http://downloads.example/app.apk"
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "uppercase sha256",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.SHA256 = strings.ToUpper(r.SHA256)
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "short sha256",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.SHA256 = "abc"
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "zero file size",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.FileSizeBytes = 0
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
		{
			name: "negative file size",
			mutate: func(r *client_release.PublishReleaseRequest) {
				r.FileSizeBytes = -1
			},
			code: client_release.CodeInvalidReleaseMetadata,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newAdminService(t)

			// Validation happens before the repository is called, so an invalid
			// request never opens a transaction.
			repo.EXPECT().
				PublishRelease(gomock.Any(), gomock.Any()).
				Times(0)

			request := validPublishReleaseRequest()
			tc.mutate(&request)

			_, err := svc.PublishRelease(context.Background(), request)

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, tc.code, appErr.Code)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		})
	}
}

func TestAdminService_PublishRelease_Delegates(t *testing.T) {
	svc, repo := newAdminService(t)

	repo.EXPECT().
		PublishRelease(
			gomock.Any(),
			client_release.PublishReleaseParams{
				Platform:      "android",
				Version:       "1.5.0",
				Build:         6,
				DownloadURL:   "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
				SHA256:        strings.Repeat("a", 64),
				FileSizeBytes: 48231001,
				ReleaseNotes:  "- notes",
				GitTag:        "client-v1.5.0-6",
				Force:         true,
			},
		).
		Return(
			client_release.PublishReleaseResult{
				Release: client_release.Release{
					Version: "1.5.0",
					Build:   6,
				},
				Policy: client_release.ChannelPolicy{
					Latest: client_release.ReleaseRef{
						Version: "1.5.0",
						Build:   6,
					},
					MinSupported: client_release.ReleaseRef{
						Version: "1.5.0",
						Build:   6,
					},
				},
				Forced: true,
			},
			nil,
		)

	request := validPublishReleaseRequest()
	request.Platform = "  ANDROID  "
	request.Force = true

	result, err := svc.PublishRelease(context.Background(), request)
	require.NoError(t, err)

	require.True(t, result.Forced)
	require.Equal(t, int64(6), result.Policy.Latest.Build)
	require.Equal(t, int64(6), result.Policy.MinSupported.Build)
}

func TestAdminService_ReleaseAction_Validation(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		build    int64
		code     string
	}{
		{"blank platform", "", 6, client_release.CodeInvalidPlatform},
		{"unknown platform", "macos", 6, client_release.CodeInvalidPlatform},
		{"zero build", "android", 0, client_release.CodeInvalidReleaseMetadata},
		{"negative build", "android", -1, client_release.CodeInvalidReleaseMetadata},
		{"build above int32", "android", 1 << 31, client_release.CodeInvalidReleaseMetadata},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newAdminService(t)

			repo.EXPECT().
				SetMinSupported(gomock.Any(), gomock.Any()).
				Times(0)
			repo.EXPECT().
				Rollback(gomock.Any(), gomock.Any()).
				Times(0)

			request := client_release.ReleaseActionRequest{
				Platform: tc.platform,
				Build:    tc.build,
			}

			_, err := svc.ForceRelease(context.Background(), request)
			require.Error(t, err)
			require.Equal(t, tc.code, apperror.FromError(err).Code)

			_, err = svc.RollbackRelease(context.Background(), request)
			require.Error(t, err)
			require.Equal(t, tc.code, apperror.FromError(err).Code)
		})
	}
}

func TestAdminService_ForceRelease_Delegates(t *testing.T) {
	svc, repo := newAdminService(t)

	repo.EXPECT().
		SetMinSupported(
			gomock.Any(),
			client_release.ReleaseActionParams{
				Platform: "android",
				Build:    6,
			},
		).
		Return(
			client_release.ChannelPolicy{
				Latest: client_release.ReleaseRef{
					Version: "1.5.0",
					Build:   6,
				},
				MinSupported: client_release.ReleaseRef{
					Version: "1.5.0",
					Build:   6,
				},
			},
			nil,
		)

	policy, err := svc.ForceRelease(
		context.Background(),
		client_release.ReleaseActionRequest{
			Platform: "android",
			Build:    6,
		},
	)
	require.NoError(t, err)

	require.Equal(t, int64(6), policy.MinSupported.Build)
}

func TestAdminService_RollbackRelease_Delegates(t *testing.T) {
	svc, repo := newAdminService(t)

	repo.EXPECT().
		Rollback(
			gomock.Any(),
			client_release.ReleaseActionParams{
				Platform: "android",
				Build:    5,
			},
		).
		Return(
			client_release.ChannelPolicy{
				Latest: client_release.ReleaseRef{
					Version: "1.4.0",
					Build:   5,
				},
				MinSupported: client_release.ReleaseRef{
					Version: "1.3.0",
					Build:   3,
				},
			},
			nil,
		)

	policy, err := svc.RollbackRelease(
		context.Background(),
		client_release.ReleaseActionRequest{
			Platform: "android",
			Build:    5,
		},
	)
	require.NoError(t, err)

	require.Equal(t, int64(5), policy.Latest.Build)
	require.Equal(t, int64(3), policy.MinSupported.Build)
}

func TestAdminService_ListReleases_Validation(t *testing.T) {
	svc, repo := newAdminService(t)

	repo.EXPECT().
		ListReleases(gomock.Any(), gomock.Any()).
		Times(0)

	_, err := svc.ListReleases(context.Background(), "macos")

	require.Error(t, err)
	require.Equal(
		t,
		client_release.CodeInvalidPlatform,
		apperror.FromError(err).Code,
	)
}

func TestAdminService_ListReleases_Delegates(t *testing.T) {
	svc, repo := newAdminService(t)

	repo.EXPECT().
		ListReleases(gomock.Any(), "android").
		Return(
			client_release.ListReleasesResult{
				Releases: []client_release.Release{
					{Version: "1.5.0", Build: 6},
				},
				Policy: &client_release.ChannelPolicy{
					Latest: client_release.ReleaseRef{
						Version: "1.5.0",
						Build:   6,
					},
					MinSupported: client_release.ReleaseRef{
						Version: "1.3.0",
						Build:   3,
					},
				},
			},
			nil,
		)

	result, err := svc.ListReleases(context.Background(), " Android ")
	require.NoError(t, err)

	require.Len(t, result.Releases, 1)
	require.NotNil(t, result.Policy)
	require.Equal(t, int64(6), result.Policy.Latest.Build)
}

func TestAdminService_PublishRelease_ReturnsRepositoryError(t *testing.T) {
	svc, repo := newAdminService(t)

	repoErr := apperror.ConflictWith(
		client_release.CodeClientReleaseAlreadyExists,
		"client release already exists",
		nil,
	)

	repo.EXPECT().
		PublishRelease(gomock.Any(), gomock.Any()).
		Return(client_release.PublishReleaseResult{}, repoErr)

	_, err := svc.PublishRelease(
		context.Background(),
		validPublishReleaseRequest(),
	)

	require.Equal(t, repoErr, err)
}
