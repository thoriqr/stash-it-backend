package client_release_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func newCheckHandlerApp(
	t *testing.T,
	repo client_release.Repository,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(zap.NewNop()),
		StructValidator: validation.New(),
	})

	client_release.Routes(
		app.Group("/client-releases"),
		client_release.NewHandler(
			client_release.NewService(repo, zap.NewNop()),
		),
	)

	return app
}

func doCheck(
	t *testing.T,
	app *fiber.App,
	target string,
) *http.Response {
	t.Helper()

	// No Authorization header is set anywhere in this file: the route is public.
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	require.NoError(t, err)

	return resp
}

func readCheckBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(raw)
}

type checkAPIResponse struct {
	Data struct {
		Platform     string `json:"platform"`
		Status       string `json:"status"`
		CurrentBuild int64  `json:"current_build"`
		MinSupported *struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"min_supported"`
		Latest *struct {
			Version       string    `json:"version"`
			Build         int64     `json:"build"`
			DownloadURL   string    `json:"download_url"`
			SHA256        string    `json:"sha256"`
			FileSizeBytes int64     `json:"file_size_bytes"`
			ReleaseNotes  string    `json:"release_notes"`
			PublishedAt   time.Time `json:"published_at"`
		} `json:"latest"`
	} `json:"data"`
	Message string `json:"message"`
}

type checkAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"fields"`
	} `json:"error"`
}

func decodeCheckAPI(t *testing.T, raw string) checkAPIResponse {
	t.Helper()

	var body checkAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeCheckAPIError(t *testing.T, raw string) checkAPIErrorResponse {
	t.Helper()

	var body checkAPIErrorResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func TestHandler_Check_OptionalUpdate(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository(ctrl)

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformAndroid).
		Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

	app := newCheckHandlerApp(t, repo)

	resp := doCheck(
		t,
		app,
		"/client-releases/latest?platform=android&build=4",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	raw := readCheckBody(t, resp)

	// httpx.OK emits {data, message}; meta is only for paginated responses.
	require.NotContains(t, raw, `"meta"`)

	body := decodeCheckAPI(t, raw)

	require.Equal(t, "client release check completed successfully", body.Message)
	require.Equal(t, client_release.PlatformAndroid, body.Data.Platform)
	require.Equal(t, client_release.StatusOptional, body.Data.Status)
	require.Equal(t, int64(4), body.Data.CurrentBuild)

	require.NotNil(t, body.Data.MinSupported)
	require.Equal(t, "1.3.0", body.Data.MinSupported.Version)
	require.Equal(t, int64(3), body.Data.MinSupported.Build)

	require.NotNil(t, body.Data.Latest)
	require.Equal(t, "1.5.0", body.Data.Latest.Version)
	require.Equal(t, int64(6), body.Data.Latest.Build)
	require.Contains(t, body.Data.Latest.DownloadURL, "/android/")
	require.Len(t, body.Data.Latest.SHA256, 64)
	require.Equal(t, int64(48231001), body.Data.Latest.FileSizeBytes)
	require.Equal(t, "- notes", body.Data.Latest.ReleaseNotes)
	require.False(t, body.Data.Latest.PublishedAt.IsZero())
}

func TestHandler_Check_NoChannel(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository(ctrl)

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformWindows).
		Return(nil, nil)

	app := newCheckHandlerApp(t, repo)

	resp := doCheck(
		t,
		app,
		"/client-releases/latest?platform=windows&build=1",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	raw := readCheckBody(t, resp)

	require.Contains(t, raw, `"status":"none"`)
	require.Contains(t, raw, `"latest":null`)
	require.Contains(t, raw, `"min_supported":null`)
	require.Contains(t, raw, `"platform":"windows"`)
	require.Contains(t, raw, `"current_build":1`)

	body := decodeCheckAPI(t, raw)

	require.Nil(t, body.Data.Latest)
	require.Nil(t, body.Data.MinSupported)
	require.Equal(t, client_release.StatusNone, body.Data.Status)
}

func TestHandler_Check_RequiredUpdate(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository(ctrl)

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformAndroid).
		Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

	app := newCheckHandlerApp(t, repo)

	resp := doCheck(
		t,
		app,
		"/client-releases/latest?platform=android&build=2",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeCheckAPI(t, readCheckBody(t, resp))

	require.Equal(t, client_release.StatusRequired, body.Data.Status)
	require.Equal(t, int64(2), body.Data.CurrentBuild)
	require.NotNil(t, body.Data.Latest)
}

func TestHandler_Check_ClientAhead(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository(ctrl)

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformAndroid).
		Return(channelWithBuilds(client_release.PlatformAndroid, 6, 3), nil)

	app := newCheckHandlerApp(t, repo)

	resp := doCheck(
		t,
		app,
		"/client-releases/latest?platform=android&build=7",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeCheckAPI(t, readCheckBody(t, resp))

	// The client is ahead of the published release. Nothing is offered, and the
	// lower latest.build is informational rather than an instruction to downgrade.
	require.Equal(t, client_release.StatusNone, body.Data.Status)
	require.Equal(t, int64(7), body.Data.CurrentBuild)
	require.NotNil(t, body.Data.Latest)
	require.Equal(t, int64(6), body.Data.Latest.Build)
}

func TestHandler_Check_Validation(t *testing.T) {
	cases := []struct {
		name   string
		target string
		code   string
	}{
		{
			name:   "missing platform",
			target: "/client-releases/latest?build=4",
			code:   client_release.CodeInvalidPlatform,
		},
		{
			name:   "unknown platform",
			target: "/client-releases/latest?platform=macos&build=4",
			code:   client_release.CodeInvalidPlatform,
		},
		{
			name:   "missing build",
			target: "/client-releases/latest?platform=android",
			code:   client_release.CodeInvalidClientBuild,
		},
		{
			name:   "non-numeric build",
			target: "/client-releases/latest?platform=android&build=abc",
			code:   client_release.CodeInvalidClientBuild,
		},
		{
			name:   "zero build",
			target: "/client-releases/latest?platform=android&build=0",
			code:   client_release.CodeInvalidClientBuild,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repo := mocks.NewMockRepository(ctrl)

			repo.EXPECT().
				GetChannel(gomock.Any(), gomock.Any()).
				Times(0)

			app := newCheckHandlerApp(t, repo)

			resp := doCheck(t, app, tc.target)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

			body := decodeCheckAPIError(t, readCheckBody(t, resp))

			require.Equal(t, tc.code, body.Error.Code)
			require.NotEmpty(t, body.Error.Message)
			require.NotNil(t, body.Error.Fields)
		})
	}
}

func TestHandler_Check_RepositoryError(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockRepository(ctrl)

	repo.EXPECT().
		GetChannel(gomock.Any(), client_release.PlatformAndroid).
		Return(
			nil,
			apperror.Internal(errors.New("connection reset by peer")),
		)

	app := newCheckHandlerApp(t, repo)

	resp := doCheck(
		t,
		app,
		"/client-releases/latest?platform=android&build=4",
	)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	raw := readCheckBody(t, resp)

	body := decodeCheckAPIError(t, raw)

	require.Equal(t, apperror.CodeInternal, body.Error.Code)
	require.NotContains(t, raw, "connection reset by peer")
}
