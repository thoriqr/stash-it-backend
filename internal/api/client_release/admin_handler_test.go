package client_release_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/client_release"
	"github.com/thoriqr/stash-it-backend/internal/api/client_release/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const (
	adminHandlerTestSecret = "admin-handler-test-secret"
	adminPublishPath       = "/internal/maintenance/client-releases"
	adminForcePath         = adminPublishPath + "/force"
	adminRollbackPath      = adminPublishPath + "/rollback"
	adminListPath          = adminPublishPath + "?platform=android"
)

const adminPublishBody = `{
	"platform": "android",
	"version": "1.5.0",
	"build": 6,
	"download_url": "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
	"sha256": "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
	"file_size_bytes": 48231001,
	"release_notes": "- notes",
	"git_tag": "client-v1.5.0-6",
	"force": false
}`

const adminActionBody = `{"platform": "android", "build": 6}`

func newAdminHandlerApp(
	t *testing.T,
	repo client_release.AdminRepository,
	secret string,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	client_release.MaintenanceRoutes(
		app.Group("/internal/maintenance"),
		client_release.NewAdminHandler(
			client_release.NewAdminService(repo, zap.NewNop()),
		),
		secret,
	)

	return app
}

func doAdminCheck(
	t *testing.T,
	app *fiber.App,
	method, target, body, secret string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if secret != "" {
		req.Header.Set("X-Release-Secret", secret)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	return resp
}

func decodePublishReleaseAPI(
	t *testing.T,
	raw string,
) client_release.PublishReleaseAPIResponse {
	t.Helper()

	var body client_release.PublishReleaseAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeReleasePolicyAPI(
	t *testing.T,
	raw string,
) client_release.ReleasePolicyAPIResponse {
	t.Helper()

	var body client_release.ReleasePolicyAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeListReleasesAPI(
	t *testing.T,
	raw string,
) client_release.ListReleasesAPIResponse {
	t.Helper()

	var body client_release.ListReleasesAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func expectedAdminPublishParams() client_release.PublishReleaseParams {
	return client_release.PublishReleaseParams{
		Platform:      "android",
		Version:       "1.5.0",
		Build:         6,
		DownloadURL:   "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
		SHA256:        "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
		FileSizeBytes: 48231001,
		ReleaseNotes:  "- notes",
		GitTag:        "client-v1.5.0-6",
		Force:         false,
	}
}

func adminPublishedResult() client_release.PublishReleaseResult {
	return client_release.PublishReleaseResult{
		Release: client_release.Release{
			Version:       "1.5.0",
			Build:         6,
			DownloadURL:   "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
			SHA256:        "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
			FileSizeBytes: 48231001,
			ReleaseNotes:  "- notes",
			GitTag:        "client-v1.5.0-6",
			PublishedAt:   time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		},
		Policy: client_release.ChannelPolicy{
			Latest: client_release.ReleaseRef{
				Version: "1.5.0",
				Build:   6,
			},
			MinSupported: client_release.ReleaseRef{
				Version: "1.3.0",
				Build:   3,
			},
		},
		Forced: false,
	}
}

func TestAdminHandler_Guard(t *testing.T) {
	t.Run("rejects a missing secret", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := mocks.NewMockAdminRepository(ctrl)

		app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

		resp := doAdminCheck(
			t,
			app,
			http.MethodPost,
			adminPublishPath,
			adminPublishBody,
			"",
		)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body := decodeCheckAPIError(t, readCheckBody(t, resp))
		require.Equal(t, client_release.CodeInvalidReleaseSecret, body.Error.Code)
	})

	t.Run("rejects a wrong secret", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := mocks.NewMockAdminRepository(ctrl)

		app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

		resp := doAdminCheck(
			t,
			app,
			http.MethodPost,
			adminPublishPath,
			adminPublishBody,
			"not-the-secret",
		)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body := decodeCheckAPIError(t, readCheckBody(t, resp))
		require.Equal(t, client_release.CodeInvalidReleaseSecret, body.Error.Code)
	})

	t.Run("rejects every request when no secret is configured", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := mocks.NewMockAdminRepository(ctrl)

		app := newAdminHandlerApp(t, repo, "")

		resp := doAdminCheck(
			t,
			app,
			http.MethodPost,
			adminPublishPath,
			adminPublishBody,
			"any-secret",
		)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		body := decodeCheckAPIError(t, readCheckBody(t, resp))
		require.Equal(t, client_release.CodeInvalidReleaseSecret, body.Error.Code)
	})
}

func TestAdminHandler_PublishRelease(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		PublishRelease(gomock.Any(), expectedAdminPublishParams()).
		Return(adminPublishedResult(), nil)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminPublishPath,
		adminPublishBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	body := decodePublishReleaseAPI(t, readCheckBody(t, resp))

	require.Equal(
		t,
		"client release published successfully",
		body.Message,
	)
	require.NotNil(t, body.Data)
	require.Equal(t, "1.5.0", body.Data.Release.Version)
	require.Equal(t, int64(6), body.Data.Release.Build)
	require.Equal(t, "client-v1.5.0-6", body.Data.Release.GitTag)
	require.False(t, body.Data.Release.PublishedAt.IsZero())
	require.Equal(t, int64(6), body.Data.Latest.Build)
	require.Equal(t, int64(3), body.Data.MinSupported.Build)
	require.False(t, body.Data.Forced)
}

func TestAdminHandler_PublishRelease_ValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		PublishRelease(gomock.Any(), gomock.Any()).
		Times(0)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	invalidBody := strings.Replace(
		adminPublishBody,
		`"version": "1.5.0"`,
		`"version": "1.5"`,
		1,
	)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminPublishPath,
		invalidBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	body := decodeCheckAPIError(t, readCheckBody(t, resp))
	require.Equal(t, client_release.CodeInvalidReleaseMetadata, body.Error.Code)
}

func TestAdminHandler_PublishRelease_Conflict(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		PublishRelease(gomock.Any(), gomock.Any()).
		Return(
			client_release.PublishReleaseResult{},
			apperror.ConflictWith(
				client_release.CodeClientReleaseAlreadyExists,
				"client release already exists",
				nil,
			),
		)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminPublishPath,
		adminPublishBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	body := decodeCheckAPIError(t, readCheckBody(t, resp))
	require.Equal(
		t,
		client_release.CodeClientReleaseAlreadyExists,
		body.Error.Code,
	)
}

func TestAdminHandler_ForceRelease(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

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

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminForcePath,
		adminActionBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	body := decodeReleasePolicyAPI(t, readCheckBody(t, resp))

	require.Equal(
		t,
		"client release policy updated successfully",
		body.Message,
	)
	require.NotNil(t, body.Data)
	require.Equal(t, int64(6), body.Data.Latest.Build)
	require.Equal(t, int64(6), body.Data.MinSupported.Build)
}

func TestAdminHandler_ForceRelease_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		SetMinSupported(gomock.Any(), gomock.Any()).
		Return(
			client_release.ChannelPolicy{},
			apperror.NotFoundWith(
				client_release.CodeReleaseNotFound,
				"client release not found",
				nil,
			),
		)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminForcePath,
		adminActionBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	body := decodeCheckAPIError(t, readCheckBody(t, resp))
	require.Equal(t, client_release.CodeReleaseNotFound, body.Error.Code)
}

func TestAdminHandler_RollbackRelease(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		Rollback(
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
					Version: "1.3.0",
					Build:   3,
				},
			},
			nil,
		)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminRollbackPath,
		adminActionBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeReleasePolicyAPI(t, readCheckBody(t, resp))

	require.Equal(
		t,
		"client release rolled back successfully",
		body.Message,
	)
	require.NotNil(t, body.Data)
	require.Equal(t, int64(6), body.Data.Latest.Build)
	require.Equal(t, int64(3), body.Data.MinSupported.Build)
}

func TestAdminHandler_ListReleases(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		ListReleases(gomock.Any(), "android").
		Return(
			client_release.ListReleasesResult{
				Releases: []client_release.Release{
					{
						Version: "1.5.0",
						Build:   6,
						GitTag:  "client-v1.5.0-6",
					},
					{
						Version: "1.4.0",
						Build:   5,
						GitTag:  "client-v1.4.0-5",
					},
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

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodGet,
		adminListPath,
		"",
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	body := decodeListReleasesAPI(t, readCheckBody(t, resp))

	require.Equal(
		t,
		"client releases retrieved successfully",
		body.Message,
	)
	require.NotNil(t, body.Data)
	require.Len(t, body.Data.Releases, 2)
	require.Equal(t, int64(6), body.Data.Releases[0].Build)
	require.Equal(t, int64(5), body.Data.Releases[1].Build)
	require.NotNil(t, body.Data.Latest)
	require.Equal(t, int64(6), body.Data.Latest.Build)
	require.NotNil(t, body.Data.MinSupported)
	require.Equal(t, int64(3), body.Data.MinSupported.Build)
}

func TestAdminHandler_ListReleases_NoChannel(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		ListReleases(gomock.Any(), "android").
		Return(
			client_release.ListReleasesResult{
				Releases: []client_release.Release{},
			},
			nil,
		)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodGet,
		adminListPath,
		"",
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw := readCheckBody(t, resp)

	require.Contains(t, raw, `"releases":[]`)
	require.Contains(t, raw, `"latest":null`)
	require.Contains(t, raw, `"min_supported":null`)
}

func TestAdminHandler_InternalError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockAdminRepository(ctrl)

	repo.EXPECT().
		PublishRelease(gomock.Any(), gomock.Any()).
		Return(
			client_release.PublishReleaseResult{},
			apperror.Internal(errors.New("connection reset by peer")),
		)

	app := newAdminHandlerApp(t, repo, adminHandlerTestSecret)

	resp := doAdminCheck(
		t,
		app,
		http.MethodPost,
		adminPublishPath,
		adminPublishBody,
		adminHandlerTestSecret,
	)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	raw := readCheckBody(t, resp)

	body := decodeCheckAPIError(t, raw)

	require.Equal(t, apperror.CodeInternal, body.Error.Code)
	require.NotContains(t, raw, "connection reset by peer")
}
