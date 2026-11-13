package client_release_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/client_release"
	"github.com/thoriqr/stash-it-backend/internal/api/client_release/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func newCheckService(t *testing.T) (client_release.Service, *mocks.MockRepository) {
	t.Helper()

	repo := mocks.NewMockRepository(gomock.NewController(t))

	return client_release.NewService(repo, zap.NewNop()), repo
}

func channelWithBuilds(
	platform string,
	latestBuild, minBuild int64,
) *client_release.Channel {
	return &client_release.Channel{
		Platform: platform,
		Latest: client_release.Release{
			Version:       "1.5.0",
			Build:         latestBuild,
			DownloadURL:   "https://downloads.stashit.thoriqr.my.id/" + platform + "/stash-it-1.5.0.apk",
			SHA256:        strings.Repeat("a", 64),
			FileSizeBytes: 48231001,
			ReleaseNotes:  "- notes",
			PublishedAt:   time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		},
		MinSupported: client_release.ReleaseRef{
			Version: "1.3.0",
			Build:   minBuild,
		},
	}
}

func TestService_Check_Statuses(t *testing.T) {
	t.Run("returns none when the platform has no channel", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(nil, nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"4",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusNone, result.Status)
		require.Equal(t, client_release.PlatformAndroid, result.Platform)
		require.Equal(t, int64(4), result.CurrentBuild)
		require.Nil(t, result.Latest)
		require.Nil(t, result.MinSupported)
	})

	t.Run("requires an update below the minimum", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"2",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusRequired, result.Status)
	})

	t.Run("offers an optional update at the minimum", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"3",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusOptional, result.Status)
	})

	t.Run("offers an optional update between the minimum and latest", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"5",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusOptional, result.Status)
	})

	t.Run("returns none on the latest build", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"6",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusNone, result.Status)
		require.NotNil(t, result.Latest)
		require.Equal(t, int64(6), result.Latest.Build)
	})

	t.Run("returns none when the client is ahead", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"7",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusNone, result.Status)
		require.Equal(t, int64(7), result.CurrentBuild)
		require.NotNil(t, result.Latest)
		require.Equal(t, int64(6), result.Latest.Build)
	})

	t.Run("clamps a minimum above the latest release", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 5, 6), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"4",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusRequired, result.Status)
		require.NotNil(t, result.MinSupported)
		require.Equal(t, int64(5), result.MinSupported.Build)
		require.Equal(t, "1.5.0", result.MinSupported.Version)
	})

	t.Run("returns none when the client is on the clamped latest", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 5, 6), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"5",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.StatusNone, result.Status)
		require.NotNil(t, result.MinSupported)
		require.Equal(t, int64(5), result.MinSupported.Build)
	})

	t.Run("normalizes the platform before reading the channel", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			"  ANDROID  ",
			"4",
		)
		require.NoError(t, err)

		require.Equal(t, client_release.PlatformAndroid, result.Platform)
	})

	t.Run("parses a build above the int32 range", func(t *testing.T) {
		svc, repo := newCheckService(t)

		repo.EXPECT().
			GetChannel(gomock.Any(), client_release.PlatformAndroid).
			Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

		result, err := svc.Check(
			context.Background(),
			client_release.PlatformAndroid,
			"2147483648",
		)
		require.NoError(t, err)

		require.Equal(t, int64(2147483648), result.CurrentBuild)
		require.Equal(t, client_release.StatusNone, result.Status)
	})
}

func TestService_Check_Validation(t *testing.T) {
	for _, platform := range []string{"", "   ", "macos", "win32", "linux"} {
		t.Run("rejects platform "+platform, func(t *testing.T) {
			svc, repo := newCheckService(t)

			// Validation happens before the channel is read, so the repository is
			// never consulted for a request that cannot be answered.
			repo.EXPECT().
				GetChannel(gomock.Any(), gomock.Any()).
				Times(0)

			_, err := svc.Check(context.Background(), platform, "4")

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, client_release.CodeInvalidPlatform, appErr.Code)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		})
	}

	for _, build := range []string{"", "   ", "abc", "0", "-1", "1.5"} {
		t.Run("rejects build "+build, func(t *testing.T) {
			svc, repo := newCheckService(t)

			repo.EXPECT().
				GetChannel(gomock.Any(), gomock.Any()).
				Times(0)

			_, err := svc.Check(
				context.Background(),
				client_release.PlatformAndroid,
				build,
			)

			require.Error(t, err)

			appErr := apperror.FromError(err)
			require.Equal(t, client_release.CodeInvalidClientBuild, appErr.Code)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		})
	}
}

func TestService_Check_ReturnsRepositoryError(t *testing.T) {
	svc, repo := newCheckService(t)

	repoErr := apperror.Internal(errors.New("channel read failed"))

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformAndroid).
		Return(nil, repoErr)

	_, err := svc.Check(
		context.Background(),
		client_release.PlatformAndroid,
		"4",
	)

	require.Equal(t, repoErr, err)
}
