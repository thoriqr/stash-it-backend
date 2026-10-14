package health_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/health"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

// These tests pin the three properties that make this endpoint what it is.
//
// They are small on purpose: the handler has no dependencies, so there is no
// database, no Redis and no repository to stand up. What is worth proving is
// that the endpoint answers, that it answers the documented body, and that it
// answers without a credential — and the last of those is a property the code
// cannot state about itself.

func newLivenessApp() *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.NewErrorHandler(zap.NewNop()),
	})

	health.Routes(app, health.NewHandler())

	return app
}

// The body is part of the contract, not an implementation detail: something
// consuming this endpoint reads it, and a rename that quietly reshaped it would
// break that consumer while the status code still said 200.
func TestLiveness_Check_ReturnsOKWithTheDocumentedBody(t *testing.T) {
	resp, err := newLivenessApp().Test(
		httptest.NewRequest(http.MethodGet, "/livez", nil),
	)
	require.NoError(t, err)

	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var body map[string]string

	require.NoError(t, json.Unmarshal(raw, &body))
	require.Equal(t, map[string]string{"status": "ok"}, body)
}

// A platform calls this without holding a credential of any kind, so requiring
// one would make the endpoint unreachable for the only caller that matters.
//
// This is the assertion most likely to be broken by a well-meaning sweep to
// "secure all routes", and it is the one that would fail silently — the probe
// would simply stop working in production, with nothing in the application
// logging to explain it.
func TestLiveness_Check_RequiresNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{name: "no Authorization header", headers: nil},
		{
			name:    "an empty Authorization header",
			headers: map[string]string{"Authorization": ""},
		},
		{
			name:    "a malformed Authorization header",
			headers: map[string]string{"Authorization": "Bearer"},
		},
		{
			name:    "a garbage bearer token",
			headers: map[string]string{"Authorization": "Bearer not-a-jwt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/livez", nil)

			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}

			resp, err := newLivenessApp().Test(req)
			require.NoError(t, err)

			defer resp.Body.Close()

			require.Equal(
				t,
				http.StatusOK,
				resp.StatusCode,
				"the liveness probe must answer whatever the Authorization header says",
			)
		})
	}
}

// The old path has to be gone rather than left behind as an alias. Two live
// endpoints with the same meaning invite someone to wire the platform to the
// one whose name they recognise and leave the other to rot.
func TestLiveness_Routes_DoesNotServeTheOldPath(t *testing.T) {
	resp, err := newLivenessApp().Test(
		httptest.NewRequest(http.MethodGet, "/health", nil),
	)
	require.NoError(t, err)

	defer resp.Body.Close()

	require.NotEqual(
		t,
		http.StatusOK,
		resp.StatusCode,
		"/health must no longer answer",
	)
}