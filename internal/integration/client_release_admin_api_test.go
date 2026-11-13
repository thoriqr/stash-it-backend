package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/client_release"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

const (
	clientReleaseAdminPath         = "/internal/maintenance/client-releases"
	clientReleaseAdminForcePath    = clientReleaseAdminPath + "/force"
	clientReleaseAdminRollbackPath = clientReleaseAdminPath + "/rollback"
)

type clientReleaseAdminPublishRequest struct {
	Platform      string `json:"platform"`
	Version       string `json:"version"`
	Build         int64  `json:"build"`
	DownloadURL   string `json:"download_url"`
	SHA256        string `json:"sha256"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	ReleaseNotes  string `json:"release_notes"`
	GitTag        string `json:"git_tag"`
	Force         bool   `json:"force"`
}

type clientReleaseAdminActionRequest struct {
	Platform string `json:"platform"`
	Build    int64  `json:"build"`
}

func clientReleaseAdminPublishBody(
	t *testing.T,
	platform, version string,
	build int64,
	force bool,
) string {
	t.Helper()

	body, err := json.Marshal(clientReleaseAdminPublishRequest{
		Platform: platform,
		Version:  version,
		Build:    build,
		DownloadURL: fmt.Sprintf(
			"https://downloads.stashit.thoriqr.my.id/%s/stash-it-%s-%d.apk",
			platform,
			version,
			build,
		),
		SHA256:        clientReleaseTestSHA256,
		FileSizeBytes: 48231001,
		ReleaseNotes:  "- release " + version,
		GitTag:        fmt.Sprintf("client-v%s-%d", version, build),
		Force:         force,
	})
	require.NoError(t, err)

	return string(body)
}

func clientReleaseAdminActionBody(
	t *testing.T,
	platform string,
	build int64,
) string {
	t.Helper()

	body, err := json.Marshal(clientReleaseAdminActionRequest{
		Platform: platform,
		Build:    build,
	})
	require.NoError(t, err)

	return string(body)
}

func doClientReleaseAdmin(
	t *testing.T,
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

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}

type clientReleaseAdminPublishResponse struct {
	Data struct {
		Release struct {
			Version       string    `json:"version"`
			Build         int64     `json:"build"`
			DownloadURL   string    `json:"download_url"`
			SHA256        string    `json:"sha256"`
			FileSizeBytes int64     `json:"file_size_bytes"`
			ReleaseNotes  string    `json:"release_notes"`
			GitTag        string    `json:"git_tag"`
			PublishedAt   time.Time `json:"published_at"`
		} `json:"release"`
		Latest struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"latest"`
		MinSupported struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"min_supported"`
		Forced bool `json:"forced"`
	} `json:"data"`
	Message string `json:"message"`
}

type clientReleaseAdminPolicyResponse struct {
	Data struct {
		Latest struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"latest"`
		MinSupported struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"min_supported"`
	} `json:"data"`
	Message string `json:"message"`
}

type clientReleaseAdminListResponse struct {
	Data struct {
		Releases []struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
			GitTag  string `json:"git_tag"`
		} `json:"releases"`
		Latest *struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"latest"`
		MinSupported *struct {
			Version string `json:"version"`
			Build   int64  `json:"build"`
		} `json:"min_supported"`
	} `json:"data"`
	Message string `json:"message"`
}

func decodeClientReleaseAdminPublish(
	t *testing.T,
	raw string,
) clientReleaseAdminPublishResponse {
	t.Helper()

	var body clientReleaseAdminPublishResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeClientReleaseAdminPolicy(
	t *testing.T,
	raw string,
) clientReleaseAdminPolicyResponse {
	t.Helper()

	var body clientReleaseAdminPolicyResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func decodeClientReleaseAdminList(
	t *testing.T,
	raw string,
) clientReleaseAdminListResponse {
	t.Helper()

	var body clientReleaseAdminListResponse

	require.NoError(t, json.Unmarshal([]byte(raw), &body))

	return body
}

func TestClientReleaseAdminAPI_PublishFlow(t *testing.T) {
	truncateClientReleaseData(t)

	// First release: both bounds are the new release.
	resp := doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	published := decodeClientReleaseAdminPublish(
		t,
		readClientReleaseBody(t, resp),
	)

	require.Equal(t, int64(1), published.Data.Release.Build)
	require.Equal(t, int64(1), published.Data.Latest.Build)
	require.Equal(t, int64(1), published.Data.MinSupported.Build)
	require.False(t, published.Data.Forced)

	check := decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("android", "1"),
			),
		),
	)
	require.Equal(t, client_release.StatusNone, check.Data.Status)
	require.NotNil(t, check.Data.Latest)
	require.Equal(t, int64(1), check.Data.Latest.Build)

	// Second release: latest moves, minimum stays. The update is optional.
	resp = doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		clientReleaseAdminPublishBody(t, "android", "1.1.0", 2, false),
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	published = decodeClientReleaseAdminPublish(
		t,
		readClientReleaseBody(t, resp),
	)
	require.Equal(t, int64(2), published.Data.Latest.Build)
	require.Equal(t, int64(1), published.Data.MinSupported.Build)
	require.False(t, published.Data.Forced)

	check = decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("android", "1"),
			),
		),
	)
	require.Equal(t, client_release.StatusOptional, check.Data.Status)
	require.Equal(t, int64(2), check.Data.Latest.Build)
	require.Equal(t, int64(1), check.Data.MinSupported.Build)

	// Third release with force: both pointers move. The update becomes required.
	resp = doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		clientReleaseAdminPublishBody(t, "android", "1.2.0", 3, true),
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	published = decodeClientReleaseAdminPublish(
		t,
		readClientReleaseBody(t, resp),
	)
	require.True(t, published.Data.Forced)
	require.Equal(t, int64(3), published.Data.Latest.Build)
	require.Equal(t, int64(3), published.Data.MinSupported.Build)

	check = decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("android", "1"),
			),
		),
	)
	require.Equal(t, client_release.StatusRequired, check.Data.Status)
	require.Equal(t, int64(3), check.Data.MinSupported.Build)
}

func TestClientReleaseAdminAPI_Guard(t *testing.T) {
	truncateClientReleaseData(t)

	body := clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false)

	resp := doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		body,
		"",
	)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	errBody := decodeClientReleaseAPIError(
		t,
		readClientReleaseBody(t, resp),
	)
	require.Equal(t, client_release.CodeInvalidReleaseSecret, errBody.Error.Code)

	resp = doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		body,
		"wrong-secret",
	)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp = doClientReleaseAdmin(
		t,
		http.MethodGet,
		clientReleaseAdminPath+"?platform=android",
		"",
		"",
	)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// The refused calls wrote nothing.
	resp = doClientReleaseAdmin(
		t,
		http.MethodGet,
		clientReleaseAdminPath+"?platform=android",
		"",
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	list := decodeClientReleaseAdminList(
		t,
		readClientReleaseBody(t, resp),
	)
	require.Empty(t, list.Data.Releases)
	require.Nil(t, list.Data.Latest)
	require.Nil(t, list.Data.MinSupported)

	// The same secret that lists can publish.
	resp = doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		body,
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestClientReleaseAdminAPI_PublishConflicts(t *testing.T) {
	t.Run("rejects a duplicate release", func(t *testing.T) {
		truncateClientReleaseData(t)

		body := clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false)

		resp := doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			body,
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		resp = doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			body,
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusConflict, resp.StatusCode)

		errBody := decodeClientReleaseAPIError(
			t,
			readClientReleaseBody(t, resp),
		)
		require.Equal(
			t,
			client_release.CodeClientReleaseAlreadyExists,
			errBody.Error.Code,
		)
	})

	t.Run("rejects a publish below the latest", func(t *testing.T) {
		truncateClientReleaseData(t)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
			testutil.TestClientReleaseSecret,
		)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.2.0", 3, false),
			testutil.TestClientReleaseSecret,
		)

		// Build 2 was never recorded, so the insert itself succeeds; the policy
		// check then refuses to move latest backwards and the whole transaction
		// rolls back.
		resp := doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "9.9.9", 2, false),
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusConflict, resp.StatusCode)

		errBody := decodeClientReleaseAPIError(
			t,
			readClientReleaseBody(t, resp),
		)
		require.Equal(
			t,
			client_release.CodeInvalidReleasePolicy,
			errBody.Error.Code,
		)
	})

	t.Run("rejects forcing a release above the latest", func(t *testing.T) {
		truncateClientReleaseData(t)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
			testutil.TestClientReleaseSecret,
		)

		// Recorded directly so the channel still points at build 1.
		createClientRelease(t, "android", "9.9.9", 2, "- hidden")

		resp := doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminForcePath,
			clientReleaseAdminActionBody(t, "android", 2),
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusConflict, resp.StatusCode)

		errBody := decodeClientReleaseAPIError(
			t,
			readClientReleaseBody(t, resp),
		)
		require.Equal(
			t,
			client_release.CodeInvalidReleasePolicy,
			errBody.Error.Code,
		)
	})

	t.Run("rejects a missing force target", func(t *testing.T) {
		truncateClientReleaseData(t)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
			testutil.TestClientReleaseSecret,
		)

		resp := doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminForcePath,
			clientReleaseAdminActionBody(t, "android", 99),
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		errBody := decodeClientReleaseAPIError(
			t,
			readClientReleaseBody(t, resp),
		)
		require.Equal(t, client_release.CodeReleaseNotFound, errBody.Error.Code)
	})

	t.Run("rejects a missing rollback target", func(t *testing.T) {
		truncateClientReleaseData(t)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
			testutil.TestClientReleaseSecret,
		)

		resp := doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminRollbackPath,
			clientReleaseAdminActionBody(t, "android", 99),
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		errBody := decodeClientReleaseAPIError(
			t,
			readClientReleaseBody(t, resp),
		)
		require.Equal(t, client_release.CodeReleaseNotFound, errBody.Error.Code)
	})
}

func TestClientReleaseAdminAPI_Rollback(t *testing.T) {
	truncateClientReleaseData(t)

	doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
		testutil.TestClientReleaseSecret,
	)
	doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminPath,
		clientReleaseAdminPublishBody(t, "android", "1.2.0", 3, false),
		testutil.TestClientReleaseSecret,
	)

	// Force build 3 so the minimum is above the rollback target.
	resp := doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminForcePath,
		clientReleaseAdminActionBody(t, "android", 3),
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	forced := decodeClientReleaseAdminPolicy(
		t,
		readClientReleaseBody(t, resp),
	)
	require.Equal(t, int64(3), forced.Data.MinSupported.Build)

	// Roll back to build 1. The minimum is lowered only because it would otherwise
	// sit above the new latest.
	resp = doClientReleaseAdmin(
		t,
		http.MethodPost,
		clientReleaseAdminRollbackPath,
		clientReleaseAdminActionBody(t, "android", 1),
		testutil.TestClientReleaseSecret,
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rolledBack := decodeClientReleaseAdminPolicy(
		t,
		readClientReleaseBody(t, resp),
	)
	require.Equal(t, int64(1), rolledBack.Data.Latest.Build)
	require.Equal(t, int64(1), rolledBack.Data.MinSupported.Build)

	// A client on build 2 is ahead of the rolled-back latest and has no action.
	check := decodeClientReleaseAPI(
		t,
		readClientReleaseBody(
			t,
			doClientReleaseCheck(
				t,
				clientReleaseTarget("android", "2"),
			),
		),
	)
	require.Equal(t, client_release.StatusNone, check.Data.Status)
	require.Equal(t, int64(1), check.Data.Latest.Build)
}

func TestClientReleaseAdminAPI_List(t *testing.T) {
	t.Run("lists releases newest first with the current policy", func(t *testing.T) {
		truncateClientReleaseData(t)

		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.0.0", 1, false),
			testutil.TestClientReleaseSecret,
		)
		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.1.0", 2, false),
			testutil.TestClientReleaseSecret,
		)
		doClientReleaseAdmin(
			t,
			http.MethodPost,
			clientReleaseAdminPath,
			clientReleaseAdminPublishBody(t, "android", "1.2.0", 3, true),
			testutil.TestClientReleaseSecret,
		)

		resp := doClientReleaseAdmin(
			t,
			http.MethodGet,
			clientReleaseAdminPath+"?platform=android",
			"",
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

		list := decodeClientReleaseAdminList(
			t,
			readClientReleaseBody(t, resp),
		)

		require.Len(t, list.Data.Releases, 3)
		require.Equal(t, int64(3), list.Data.Releases[0].Build)
		require.Equal(t, int64(2), list.Data.Releases[1].Build)
		require.Equal(t, int64(1), list.Data.Releases[2].Build)
		require.Equal(t, "client-v1.2.0-3", list.Data.Releases[0].GitTag)

		require.NotNil(t, list.Data.Latest)
		require.Equal(t, int64(3), list.Data.Latest.Build)
		require.NotNil(t, list.Data.MinSupported)
		require.Equal(t, int64(3), list.Data.MinSupported.Build)
	})

	t.Run("reports no policy when the platform has no channel", func(t *testing.T) {
		truncateClientReleaseData(t)

		resp := doClientReleaseAdmin(
			t,
			http.MethodGet,
			clientReleaseAdminPath+"?platform=android",
			"",
			testutil.TestClientReleaseSecret,
		)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw := readClientReleaseBody(t, resp)

		require.Contains(t, raw, `"releases":[]`)
		require.Contains(t, raw, `"latest":null`)
		require.Contains(t, raw, `"min_supported":null`)
	})
}
