package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/client_release"
)

// clientReleaseTestSHA256 is a syntactically valid lowercase hex digest. The
// database only checks its shape; the API test asserts that whatever is stored is
// reported unchanged.
const clientReleaseTestSHA256 = "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef"

func truncateClientReleaseData(t *testing.T) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		`TRUNCATE client_release_channels, client_releases`,
	)
	require.NoError(t, err)
}

func createClientRelease(
	t *testing.T,
	platform, version string,
	build int64,
	notes string,
) uuid.UUID {
	t.Helper()

	var id uuid.UUID

	err := testPool.QueryRow(
		context.Background(),
		`INSERT INTO client_releases (
			platform,
			version,
			build,
			download_url,
			sha256,
			file_size_bytes,
			release_notes,
			git_tag
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		platform,
		version,
		build,
		fmt.Sprintf(
			"https://downloads.stashit.thoriqr.my.id/%s/stash-it-%s-%d.apk",
			platform,
			version,
			build,
		),
		clientReleaseTestSHA256,
		int64(48231001),
		notes,
		fmt.Sprintf("client-v%s-%d", version, build),
	).Scan(&id)
	require.NoError(t, err)

	return id
}

func setClientReleaseChannel(
	t *testing.T,
	platform string,
	latestID, minID uuid.UUID,
) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		`INSERT INTO client_release_channels (
			platform,
			latest_release_id,
			min_supported_release_id
		) VALUES ($1, $2, $3)
		ON CONFLICT (platform) DO UPDATE
		SET latest_release_id = EXCLUDED.latest_release_id,
			min_supported_release_id = EXCLUDED.min_supported_release_id`,
		platform,
		latestID,
		minID,
	)
	require.NoError(t, err)
}

func seedClientReleaseChannel(
	t *testing.T,
	platform, minVersion string,
	minBuild int64,
	latestVersion string,
	latestBuild int64,
) {
	t.Helper()

	minID := createClientRelease(
		t,
		platform,
		minVersion,
		minBuild,
		"- minimum supported",
	)
	latestID := createClientRelease(
		t,
		platform,
		latestVersion,
		latestBuild,
		"- latest release",
	)

	setClientReleaseChannel(t, platform, latestID, minID)
}

func clientReleaseTarget(platform, build string) string {
	return "/client-releases/latest?platform=" +
		url.QueryEscape(platform) +
		"&build=" +
		url.QueryEscape(build)
}

func doClientReleaseCheck(
	t *testing.T,
	target string,
) *http.Response {
	t.Helper()

	// No Authorization header is set: the route is public.
	resp, err := testApp.Test(
		httptest.NewRequest(http.MethodGet, target, nil),
	)
	require.NoError(t, err)

	return resp
}

func readClientReleaseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(raw)
}

type clientReleaseAPIResponse struct {
	Data struct {
		Platform     string                       `json:"platform"`
		Status       string                       `json:"status"`
		CurrentBuild int64                        `json:"current_build"`
		MinSupported *clientReleaseRefResponse    `json:"min_supported"`
		Latest       *clientReleaseLatestResponse `json:"latest"`
	} `json:"data"`
	Message string `json:"message"`
}

type clientReleaseRefResponse struct {
	Version string `json:"version"`
	Build   int64  `json:"build"`
}

type clientReleaseLatestResponse struct {
	Version       string    `json:"version"`
	Build         int64     `json:"build"`
	DownloadURL   string    `json:"download_url"`
	SHA256        string    `json:"sha256"`
	FileSizeBytes int64     `json:"file_size_bytes"`
	ReleaseNotes  string    `json:"release_notes"`
	PublishedAt   time.Time `json:"published_at"`
}

type clientReleaseAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"fields"`
	} `json:"error"`
}

func decodeClientReleaseAPI(
	t *testing.T,
	raw string,
) clientReleaseAPIResponse {
	t.Helper()

	var body clientReleaseAPIResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeClientReleaseAPIError(
	t *testing.T,
	raw string,
) clientReleaseAPIErrorResponse {
	t.Helper()

	var body clientReleaseAPIErrorResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func TestClientReleaseAPI_NoChannel(t *testing.T) {
	truncateClientReleaseData(t)

	resp := doClientReleaseCheck(t, clientReleaseTarget("android", "1"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	raw := readClientReleaseBody(t, resp)

	// No release is a successful answer, not a 404, and both optional objects are
	// explicitly null rather than absent.
	require.Contains(t, raw, `"status":"none"`)
	require.Contains(t, raw, `"latest":null`)
	require.Contains(t, raw, `"min_supported":null`)
	require.NotContains(t, raw, `"meta"`)

	body := decodeClientReleaseAPI(t, raw)

	require.Equal(t, client_release.PlatformAndroid, body.Data.Platform)
	require.Equal(t, client_release.StatusNone, body.Data.Status)
	require.Equal(t, int64(1), body.Data.CurrentBuild)
	require.Nil(t, body.Data.MinSupported)
	require.Nil(t, body.Data.Latest)
	require.Equal(
		t,
		"client release check completed successfully",
		body.Message,
	)
}

func TestClientReleaseAPI_Statuses(t *testing.T) {
	truncateClientReleaseData(t)
	seedClientReleaseChannel(t, "android", "1.3.0", 3, "1.5.0", 6)

	cases := []struct {
		name         string
		build        string
		currentBuild int64
		status       string
	}{
		{
			name:         "required below the minimum",
			build:        "2",
			currentBuild: 2,
			status:       client_release.StatusRequired,
		},
		{
			name:         "optional at the minimum",
			build:        "3",
			currentBuild: 3,
			status:       client_release.StatusOptional,
		},
		{
			name:         "optional below the latest",
			build:        "5",
			currentBuild: 5,
			status:       client_release.StatusOptional,
		},
		{
			name:         "none on the latest",
			build:        "6",
			currentBuild: 6,
			status:       client_release.StatusNone,
		},
		{
			name:         "none when the client is ahead",
			build:        "7",
			currentBuild: 7,
			status:       client_release.StatusNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doClientReleaseCheck(
				t,
				clientReleaseTarget("android", tc.build),
			)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			body := decodeClientReleaseAPI(
				t,
				readClientReleaseBody(t, resp),
			)

			require.Equal(t, tc.status, body.Data.Status)
			require.Equal(t, tc.currentBuild, body.Data.CurrentBuild)

			// Both pointers are present whenever a channel exists, including when
			// status is none: the response still names the current release.
			require.NotNil(t, body.Data.MinSupported)
			require.Equal(t, "1.3.0", body.Data.MinSupported.Version)
			require.Equal(t, int64(3), body.Data.MinSupported.Build)

			require.NotNil(t, body.Data.Latest)
			require.Equal(t, "1.5.0", body.Data.Latest.Version)
			require.Equal(t, int64(6), body.Data.Latest.Build)
		})
	}

	t.Run("reports the artifact the client must download", func(t *testing.T) {
		resp := doClientReleaseCheck(
			t,
			clientReleaseTarget("android", "4"),
		)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body := decodeClientReleaseAPI(
			t,
			readClientReleaseBody(t, resp),
		)
		require.NotNil(t, body.Data.Latest)

		latest := body.Data.Latest

		require.Equal(
			t,
			"https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
			latest.DownloadURL,
		)
		require.Equal(t, clientReleaseTestSHA256, latest.SHA256)
		require.Equal(t, int64(48231001), latest.FileSizeBytes)
		require.Equal(t, "- latest release", latest.ReleaseNotes)
		require.False(t, latest.PublishedAt.IsZero())
	})
}

func TestClientReleaseAPI_PlatformsAreIndependent(t *testing.T) {
	truncateClientReleaseData(t)

	seedClientReleaseChannel(t, "android", "1.0.0", 1, "1.5.0", 6)
	seedClientReleaseChannel(t, "windows", "1.0.0", 1, "2.1.0", 7)

	android := decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("android", "1"),
			),
		),
	)
	windows := decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("windows", "1"),
			),
		),
	)

	require.Equal(t, client_release.StatusOptional, android.Data.Status)
	require.NotNil(t, android.Data.Latest)
	require.Equal(t, "1.5.0", android.Data.Latest.Version)
	require.Equal(t, int64(6), android.Data.Latest.Build)

	require.Equal(t, client_release.StatusOptional, windows.Data.Status)
	require.NotNil(t, windows.Data.Latest)
	require.Equal(t, "2.1.0", windows.Data.Latest.Version)
	require.Equal(t, int64(7), windows.Data.Latest.Build)
}

func TestClientReleaseAPI_Validation(t *testing.T) {
	cases := []struct {
		name   string
		target string
		code   string
	}{
		{
			name:   "missing platform",
			target: "/client-releases/latest?build=1",
			code:   client_release.CodeInvalidPlatform,
		},
		{
			name:   "unknown platform",
			target: "/client-releases/latest?platform=macos&build=1",
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
			resp := doClientReleaseCheck(t, tc.target)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

			body := decodeClientReleaseAPIError(
				t,
				readClientReleaseBody(t, resp),
			)

			require.Equal(t, tc.code, body.Error.Code)
			require.NotEmpty(t, body.Error.Message)
		})
	}
}

func TestClientReleaseAPI_RejectsCrossPlatformChannelPointer(t *testing.T) {
	truncateClientReleaseData(t)

	androidID := createClientRelease(
		t,
		"android",
		"1.0.0",
		1,
		"- android",
	)
	windowsMinID := createClientRelease(
		t,
		"windows",
		"1.0.0",
		1,
		"- windows minimum",
	)

	// A Windows channel pointing at an Android release must be rejected by the
	// composite foreign key, even though both ids exist.
	_, err := testPool.Exec(
		context.Background(),
		`INSERT INTO client_release_channels (
			platform,
			latest_release_id,
			min_supported_release_id
		) VALUES ('windows', $1, $2)`,
		androidID,
		windowsMinID,
	)
	require.Error(t, err)

	windowsLatestID := createClientRelease(
		t,
		"windows",
		"1.1.0",
		2,
		"- windows latest",
	)

	// The same pointer shape succeeds when both releases belong to Windows.
	_, err = testPool.Exec(
		context.Background(),
		`INSERT INTO client_release_channels (
			platform,
			latest_release_id,
			min_supported_release_id
		) VALUES ('windows', $1, $2)`,
		windowsLatestID,
		windowsMinID,
	)
	require.NoError(t, err)
}
