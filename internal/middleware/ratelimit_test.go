package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

const testNamespace = "pin-ip"

// recordingLimiter answers with a fixed result and remembers every subject it was
// asked about.
//
// The subjects matter as much as the answers here: the property under test is
// which address the middleware decided the request came from, and that is only
// visible on the far side of the call.
type recordingLimiter struct {
	allowed    bool
	err        error
	retryAfter time.Duration

	subjects   []string
	namespaces []string
	policies   []ratelimit.Policy
}

func (l *recordingLimiter) Allow(
	_ context.Context,
	namespace string,
	subject string,
	policy ratelimit.Policy,
) (ratelimit.Result, error) {
	l.subjects = append(l.subjects, subject)
	l.namespaces = append(l.namespaces, namespace)
	l.policies = append(l.policies, policy)

	if l.err != nil {
		return ratelimit.Result{}, l.err
	}

	return ratelimit.Result{Allowed: l.allowed, RetryAfter: l.retryAfter}, nil
}

// testApp builds an app whose proxy settings are stated explicitly.
//
// Every case below names the configuration it is testing rather than inheriting
// a default, because the whole subject of these tests is which configuration is
// in force.
func testApp(
	t *testing.T,
	limiter middleware.RateLimiter,
	settings configProxySettings,
) *fiber.App {
	t.Helper()

	app := fiber.New(fiber.Config{
		ErrorHandler:       testErrorHandler,
		TrustProxy:         settings.trustProxy,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: settings.proxies},
		ProxyHeader:        settings.proxyHeader,
		EnableIPValidation: settings.enableIPValidation,
	})

	app.Post("/limited", middleware.IPRateLimit(
		limiter,
		testNamespace,
		ratelimit.Policy{Max: 20, Window: 10 * time.Minute},
	), func(c fiber.Ctx) error {
		return c.SendString("reached the handler")
	})

	return app
}

// configProxySettings mirrors what a deployment declares, so a test can state the
// same thing the application would be configured with.
type configProxySettings struct {
	trustProxy         bool
	proxies            []string
	proxyHeader        string
	enableIPValidation bool
}

// directPeer is what a deployment that is not behind a proxy resolves.
var directPeer = configProxySettings{}

// behindAProxy is what a deployment that has verified its proxy resolves. The
// allowlist is the peer `app.Test` can present, which is the only address a test
// can vary.
var behindAProxy = configProxySettings{
	trustProxy:         true,
	proxies:            []string{"0.0.0.0"},
	proxyHeader:        "X-Forwarded-For",
	enableIPValidation: true,
}

// behindAProxyWithoutValidation is a misconfigured deployment: it believes a
// forwarded header but has not asked the framework to parse it.
//
// It is here because it is the dangerous shape, not because it is recommended.
// Without validation the framework hands back the header's raw bytes, so the
// "client address" becomes whatever the request carried.
var behindAProxyWithoutValidation = configProxySettings{
	trustProxy:  true,
	proxies:     []string{"0.0.0.0"},
	proxyHeader: "X-Forwarded-For",
}

func testErrorHandler(c fiber.Ctx, err error) error {
	appErr := apperror.FromError(err)

	if appErr.Status == http.StatusTooManyRequests {
		if retryAfter := appErr.RetryAfterSeconds(); retryAfter != nil {
			c.Set(fiber.HeaderRetryAfter, strconv.Itoa(*retryAfter))
		}
	}

	return c.Status(appErr.Status).JSON(map[string]any{
		"error": map[string]string{
			"code":    appErr.Code,
			"message": appErr.Message,
		},
	})
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// A request within budget reaches the handler untouched.
func TestIPRateLimit_WithinBudgetReachesTheHandler(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, directPeer)

	resp := doRequest(t, app, nil)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
	require.Equal(t, []string{"0.0.0.0"}, limiter.subjects)
}

// An exhausted budget is a 429, and the wait reaches the response as a header
// rather than only as a field on an error object nobody serializes.
func TestIPRateLimit_ExceededIs429WithRetryAfter(t *testing.T) {
	limiter := &recordingLimiter{allowed: false, retryAfter: 137 * time.Second}

	app := testApp(t, limiter, directPeer)

	resp := doRequest(t, app, nil)

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "137", resp.Header.Get(fiber.HeaderRetryAfter))

	body := decodeError(t, resp)
	require.Equal(t, middleware.CodeIPRateLimitExceeded, body.Error.Code)

	// The body must not leak how the subject is keyed.
	require.NotContains(t, body.Error.Message, "0.0.0.0")
}

// The header carries whole seconds and never a fraction, because a client told
// to retry in half a second would retry before the window closed.
func TestIPRateLimit_RetryAfterIsWholeSeconds(t *testing.T) {
	cases := map[string]struct {
		retryAfter time.Duration
		want       string
	}{
		"exactly a second":          {retryAfter: time.Second, want: "1"},
		"a fraction of a second":    {retryAfter: 1500 * time.Millisecond, want: "2"},
		"no wait at all":            {retryAfter: 0, want: "0"},
		"a negative wait is zero":   {retryAfter: -5 * time.Second, want: "0"},
		"ten minutes exactly":       {retryAfter: 600 * time.Second, want: "600"},
		"ten minutes less a second": {retryAfter: 599*time.Second + 999*time.Millisecond, want: "600"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limiter := &recordingLimiter{allowed: false, retryAfter: tc.retryAfter}

			app := testApp(t, limiter, directPeer)

			resp := doRequest(t, app, nil)

			require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
			require.Equal(t, tc.want, resp.Header.Get(fiber.HeaderRetryAfter))
		})
	}
}

// A counter that cannot be evaluated is not permission. Letting it through would
// let anyone remove the ceiling by causing a failure.
func TestIPRateLimit_UnavailableIs503AndDoesNotFabricateAWait(t *testing.T) {
	limiter := &recordingLimiter{err: errors.New("dial tcp: connection refused")}

	app := testApp(t, limiter, directPeer)

	resp := doRequest(t, app, nil)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Empty(
		t,
		resp.Header.Get(fiber.HeaderRetryAfter),
		"a 503 has no known recovery time and must not name one",
	)

	body := decodeError(t, resp)
	require.Equal(t, middleware.CodeIPRateLimitUnavailable, body.Error.Code)
}

// A forwarded value that is not an address must not become a subject.
//
// This is the shape a misconfigured deployment produces: a proxy allowlist with
// no validation configured, where the framework hands back the header's raw
// bytes. Inventing a subject from those bytes would merge every caller that sent
// the same nonsense into one counter, and would let a caller choose which counter
// it lands in.
func TestIPRateLimit_AnUnparseableForwardedValueIsRefused(t *testing.T) {
	for _, forwarded := range []string{
		"not-an-address",
		// A real chain is as unresolvable as nonsense here, because nothing in
		// this configuration knows how to read one. That is exactly why the
		// application always pairs proxy trust with validation.
		"198.51.100.77, 203.0.113.99",
		"",
		"999.999.999.999",
		"<script>",
	} {
		t.Run(forwarded, func(t *testing.T) {
			limiter := &recordingLimiter{allowed: true}

			app := testApp(t, limiter, behindAProxyWithoutValidation)

			resp := doRequest(t, app, map[string]string{
				"X-Forwarded-For": forwarded,
			})

			require.Equal(
				t,
				http.StatusServiceUnavailable,
				resp.StatusCode,
				"an address that cannot be resolved must not become a subject",
			)

			body := decodeError(t, resp)
			require.Equal(t, middleware.CodeClientIPUnavailable, body.Error.Code)

			require.Empty(
				t,
				limiter.subjects,
				"the limiter must never be asked about a subject that is not an address",
			)
		})
	}
}

// The single most important property: a forwarding header in the request must not
// decide the client address when nothing has been configured to say who is
// allowed to write it.
func TestIPRateLimit_ForwardingHeadersAreIgnoredWithoutAConfiguredProxy(t *testing.T) {
	cases := map[string]string{
		"x-forwarded-for":  "198.51.100.77",
		"x-real-ip":        "198.51.100.78",
		"forwarded":        "for=198.51.100.79",
		"client-ip":        "198.51.100.80",
		"cf-connecting-ip": "198.51.100.81",
		"true-client-ip":   "198.51.100.82",
	}

	for header, value := range cases {
		t.Run(header, func(t *testing.T) {
			limiter := &recordingLimiter{allowed: true}

			app := testApp(t, limiter, directPeer)

			resp := doRequest(t, app, map[string]string{header: value})

			require.Equal(t, http.StatusOK, resp.StatusCode)

			require.Equal(
				t,
				[]string{"0.0.0.0"},
				limiter.subjects,
				"a %s header must not move the client address", header,
			)
		})
	}
}

// With a verified proxy configured, the forwarded value is what identifies the
// client. That is the only configuration in which a header may be believed.
func TestIPRateLimit_AConfiguredProxyIsBelieved(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, behindAProxy)

	resp := doRequest(t, app, map[string]string{
		"X-Forwarded-For": "198.51.100.77",
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []string{"198.51.100.77"}, limiter.subjects)
}

// Two clients behind the proxy are two budgets. If this failed, one neighbour's
// activity would block another's.
func TestIPRateLimit_DistinctForwardedClientsGetDistinctIdentities(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, behindAProxy)

	for _, forwarded := range []string{
		"198.51.100.77",
		"198.51.100.78",
		"2001:db8::1",
	} {
		resp := doRequest(t, app, map[string]string{
			"X-Forwarded-For": forwarded,
		})
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	require.Equal(t, []string{
		"198.51.100.77",
		"198.51.100.78",
		"2001:db8::1",
	}, limiter.subjects)
}

// A proxy that appends to the chain leaves a client's own forged entries on the
// left of it. The rightmost entry the proxy added is the real one, and reading
// anything further left would be reading what the client chose.
func TestIPRateLimit_AClientCannotPrependToTheForwardedChain(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, behindAProxy)

	resp := doRequest(t, app, map[string]string{
		// The client claims to be somewhere it is not, and the real proxy
		// appends the address it actually saw.
		"X-Forwarded-For": "203.0.113.99, 198.51.100.77",
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(
		t,
		[]string{"198.51.100.77"},
		limiter.subjects,
		"a forged leftmost entry must not become the client address",
	)
}

// The configured policy is passed through, so an endpoint's budget is decided in
// one place rather than inferred by the middleware.
func TestIPRateLimit_ConfiguredPolicyIsApplied(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, directPeer)

	doRequest(t, app, nil)

	require.Equal(t, []ratelimit.Policy{
		{Max: 20, Window: 10 * time.Minute},
	}, limiter.policies)
}

func TestIPRateLimit_NamespaceIsUsedAsGiven(t *testing.T) {
	limiter := &recordingLimiter{allowed: true}

	app := testApp(t, limiter, directPeer)

	doRequest(t, app, nil)

	require.Equal(t, []string{testNamespace}, limiter.namespaces)
}

func doRequest(
	t *testing.T,
	app *fiber.App,
	headers map[string]string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/limited", nil)

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	return resp
}

func decodeError(t *testing.T, resp *http.Response) apiError {
	t.Helper()

	var body apiError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return body
}
