package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/feedback"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	feedbackdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/feedback/generated"
)

const feedbackPath = "/feedback"

func feedbackTestDB() *feedbackdbtest.Queries {
	return feedbackdbtest.New(testPool)
}

// seedFeedbackOwner creates an account that can own a report.
//
// It writes a user row directly rather than going through registration, because
// these tests are about filing a report and not about how an account comes to
// exist. The password and identity are deliberately absent: a report only needs an
// owner, and adding a credential would arrange state nothing here asserts.
func seedFeedbackOwner(t *testing.T, email string) uuid.UUID {
	t.Helper()

	userID, err := feedbackTestDB().CreateFeedbackUser(
		context.Background(),
		feedbackdbtest.CreateFeedbackUserParams{
			Email:       email,
			DisplayName: "Feedback Owner",
		},
	)
	require.NoError(t, err)

	return userID
}

// feedbackRequestBody is the body a test sends, with fields omitted when empty so a
// test can state exactly which optional context it means to include.
func feedbackRequestBody(fields map[string]any) []byte {
	body, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}

	return body
}

// doFeedbackRequest drives one request against the real app with an explicit
// Authorization header and optional extra headers.
//
// It is stated here rather than shared with doUserRequest because it needs to set a
// header that helper does not: X-Platform is the value this endpoint reads, and a
// test that could not vary it would leave the header path unproven.
func doFeedbackRequest(
	t *testing.T,
	method string,
	target string,
	rawBody []byte,
	authorization string,
	headers map[string]string,
) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if rawBody != nil {
		reader = bytes.NewReader(rawBody)
	}

	req := httptest.NewRequest(method, target, reader)

	if rawBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := testApp.Test(
		req,
		fiber.TestConfig{Timeout: userRequestTimeout},
	)
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, body
}

func createFeedback(
	t *testing.T,
	accessToken string,
	body map[string]any,
	platform string,
) (int, []byte) {
	t.Helper()

	headers := map[string]string{}
	if platform != "" {
		headers["X-Platform"] = platform
	}

	return doFeedbackRequest(
		t,
		http.MethodPost,
		feedbackPath,
		feedbackRequestBody(body),
		bearer(accessToken),
		headers,
	)
}

type feedbackCreatedResponse struct {
	Data struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
	Message string `json:"message"`
}

type feedbackErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"fields"`
	} `json:"error"`
}

func decodeFeedbackCreated(t *testing.T, body []byte) feedbackCreatedResponse {
	t.Helper()

	var decoded feedbackCreatedResponse

	require.NoError(t, json.Unmarshal(body, &decoded))

	return decoded
}

func decodeFeedbackError(t *testing.T, body []byte) feedbackErrorResponse {
	t.Helper()

	var decoded feedbackErrorResponse

	require.NoError(t, json.Unmarshal(body, &decoded))

	return decoded
}

// A report filed through the real app is stored with the owner from the token, the
// status the column default wrote, and the context the request carried.
func TestCreateFeedback_StoresReportEndToEnd(t *testing.T) {
	email := uniqueEmail("feedback-create")
	userID := seedFeedbackOwner(t, email)
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":        "bug",
			"message":     "Saving an Instagram reel leaves the title empty.",
			"app_version": "1.3.1",
			"app_build":   7,
		},
		"android",
	)
	require.Equal(t, http.StatusCreated, status, string(body))

	created := decodeFeedbackCreated(t, body)
	require.NotEmpty(t, created.Message)

	feedbackID, err := uuid.Parse(created.Data.ID)
	require.NoError(t, err)

	row, err := feedbackTestDB().GetFeedbackRowForTest(
		context.Background(),
		feedbackID,
	)
	require.NoError(t, err)

	require.Equal(t, userID, row.UserID)
	require.Equal(t, "bug", row.Type)
	require.Equal(t, "Saving an Instagram reel leaves the title empty.", row.Message)
	require.Equal(t, "new", row.Status)

	require.True(t, row.Platform.Valid)
	require.Equal(t, "android", row.Platform.String)

	require.True(t, row.AppVersion.Valid)
	require.Equal(t, "1.3.1", row.AppVersion.String)

	require.True(t, row.AppBuild.Valid)
	require.EqualValues(t, 7, row.AppBuild.Int32)

	// created_at is reported rather than omitted, and it is the row's own value.
	require.WithinDuration(
		t,
		row.CreatedAt.Time,
		mustParseTime(t, created.Data.CreatedAt),
		time.Second,
	)
}

// A report without context is still accepted and stored, which is what lets an
// install predating the context fields keep reporting.
func TestCreateFeedback_AcceptsReportWithoutContext(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-nocontext"))
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "idea",
			"message": "Add a way to sort a collection by platform.",
		},
		"",
	)
	require.Equal(t, http.StatusCreated, status, string(body))

	created := decodeFeedbackCreated(t, body)

	feedbackID, err := uuid.Parse(created.Data.ID)
	require.NoError(t, err)

	row, err := feedbackTestDB().GetFeedbackRowForTest(
		context.Background(),
		feedbackID,
	)
	require.NoError(t, err)

	require.False(t, row.Platform.Valid)
	require.False(t, row.AppVersion.Valid)
	require.False(t, row.AppBuild.Valid)
}

// The platform is normalized before it reaches the column, so two casings of one
// platform do not become two values.
func TestCreateFeedback_NormalizesPlatform(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-platform"))
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "other",
			"message": "Platform casing.",
		},
		"  MacOS  ",
	)
	require.Equal(t, http.StatusCreated, status, string(body))

	created := decodeFeedbackCreated(t, body)

	feedbackID, err := uuid.Parse(created.Data.ID)
	require.NoError(t, err)

	row, err := feedbackTestDB().GetFeedbackRowForTest(
		context.Background(),
		feedbackID,
	)
	require.NoError(t, err)

	require.True(t, row.Platform.Valid)
	require.Equal(t, "macos", row.Platform.String)
}

// An unknown platform is recorded rather than refused. This is the property the
// schema's missing CHECK exists to allow: a platform added after this code shipped
// must not need a migration before anybody can report from it.
func TestCreateFeedback_AcceptsUnknownPlatform(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-unknown-platform"))
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "bug",
			"message": "Reported from a platform this build has never heard of.",
		},
		"fuchsia",
	)
	require.Equal(t, http.StatusCreated, status, string(body))
}

func TestCreateFeedback_RejectsUnknownType(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-bad-type"))
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "complaint",
			"message": "Not a supported type.",
		},
		"",
	)
	require.Equal(t, http.StatusBadRequest, status, string(body))

	decoded := decodeFeedbackError(t, body)
	require.Equal(t, "VALIDATION_ERROR", decoded.Error.Code)
	require.NotEmpty(t, decoded.Error.Fields)
}

// A whitespace-only message passes `required` and is rejected by the service, which
// is the layer that can tell a blank value from an empty one.
func TestCreateFeedback_RejectsBlankMessage(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-blank"))
	accessToken := newTestAccessToken(t, userID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "bug",
			"message": "   \n\t ",
		},
		"",
	)
	require.Equal(t, http.StatusBadRequest, status, string(body))

	decoded := decodeFeedbackError(t, body)
	require.Equal(t, "VALIDATION_ERROR", decoded.Error.Code)
}

func TestCreateFeedback_RequiresAuthentication(t *testing.T) {
	status, body := doFeedbackRequest(
		t,
		http.MethodPost,
		feedbackPath,
		feedbackRequestBody(map[string]any{
			"type":    "bug",
			"message": "No token.",
		}),
		"",
		nil,
	)
	require.Equal(t, http.StatusUnauthorized, status, string(body))
}

// An invalid token is refused before any row is written.
func TestCreateFeedback_RejectsInvalidToken(t *testing.T) {
	status, body := doFeedbackRequest(
		t,
		http.MethodPost,
		feedbackPath,
		feedbackRequestBody(map[string]any{
			"type":    "bug",
			"message": "Bad token.",
		}),
		"Bearer not-a-real-token",
		nil,
	)
	require.Equal(t, http.StatusUnauthorized, status, string(body))
}

// The owner is the token's subject and never the request. There is no user_id field
// in the body, so this asserts that a body carrying one changes nothing.
func TestCreateFeedback_IgnoresAUserIdInTheBody(t *testing.T) {
	ownerID := seedFeedbackOwner(t, uniqueEmail("feedback-owner"))
	otherID := seedFeedbackOwner(t, uniqueEmail("feedback-other"))

	accessToken := newTestAccessToken(t, ownerID)

	status, body := createFeedback(
		t,
		accessToken,
		map[string]any{
			"type":    "bug",
			"message": "Attempting to report as someone else.",
			"user_id": otherID.String(),
		},
		"",
	)
	require.Equal(t, http.StatusCreated, status, string(body))

	created := decodeFeedbackCreated(t, body)

	feedbackID, err := uuid.Parse(created.Data.ID)
	require.NoError(t, err)

	row, err := feedbackTestDB().GetFeedbackRowForTest(
		context.Background(),
		feedbackID,
	)
	require.NoError(t, err)
	require.Equal(t, ownerID, row.UserID)

	// The other account received nothing.
	count, err := feedbackTestDB().CountFeedbackForUser(
		context.Background(),
		otherID,
	)
	require.NoError(t, err)
	require.Zero(t, count)
}

// The route is not exposed as anything but a create. A read, status change or
// delete over HTTP would be an operator capability, and this API has none.
func TestFeedback_ExposesNoOperatorRoutes(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-routes"))
	accessToken := newTestAccessToken(t, userID)

	for _, method := range []string{
		http.MethodGet,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
	} {
		t.Run(method, func(t *testing.T) {
			status, _ := doFeedbackRequest(
				t,
				method,
				feedbackPath,
				nil,
				bearer(accessToken),
				nil,
			)

			require.NotEqual(t, http.StatusOK, status)
			require.NotEqual(t, http.StatusCreated, status)
		})
	}
}

func uniqueEmail(prefix string) string {
	return prefix + "-" + uuid.NewString() + "@example.com"
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)

	return parsed
}

// feedbackRetryAfterResponse is one response as a client sees it: the status, the
// error code, and the Retry-After header the UI reads.
type feedbackRetryAfterResponse struct {
	status     int
	code       string
	retryAfter string
}

// postFeedbackWithLimiter files one report against an app built with the given
// limiter, and reports what reached the client.
//
// The app is built here rather than using testApp because testApp's limiter admits
// everything: a test about a budget running out has to supply its own, exactly as the
// registration rate-limit tests do.
func postFeedbackWithLimiter(
	t *testing.T,
	app *fiber.App,
	accessToken string,
	platform string,
) feedbackRetryAfterResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		feedbackPath,
		bytes.NewReader(feedbackRequestBody(map[string]any{
			"type":    "bug",
			"message": "A report filed against a spent budget.",
		})),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(accessToken))

	if platform != "" {
		req.Header.Set("X-Platform", platform)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: userRequestTimeout})
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	outcome := feedbackRetryAfterResponse{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if resp.StatusCode >= 400 {
		var decoded feedbackErrorResponse
		if json.Unmarshal(body, &decoded) == nil {
			outcome.code = decoded.Error.Code
		}
	}

	return outcome
}

// A spent budget answers 429, names its code, and carries a Retry-After the client
// can count from.
//
// This is the contract the app's UI depends on: without the header it can only say
// "too many requests", and a user has no idea whether that means a minute or a day.
// The wait is asserted as the exact number the limiter reported, because anything the
// handler invented would be a different number from the one the counter holds.
func TestCreateFeedback_RateLimitCarriesRetryAfter(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-retry-after"))
	accessToken := newTestAccessToken(t, userID)

	const wait = 41 * time.Second

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(feedback.CreateUserRateLimitNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: wait,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postFeedbackWithLimiter(t, app, accessToken, "android")

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, "USER_RATE_LIMIT_EXCEEDED", result.code)
	require.Equal(t, "41", result.retryAfter)

	// Nothing was written: a refused request never reaches the insert.
	count, err := feedbackTestDB().CountFeedbackForUser(
		context.Background(),
		userID,
	)
	require.NoError(t, err)
	require.Zero(t, count)
}

// A limiter that cannot answer refuses the report and carries no Retry-After.
//
// The two refusals ask a client to do different things, and the header is what tells
// them apart: a 429 with a wait means "come back then", a 503 without one means the
// server could not tell and a guessed wait would be worse than none.
func TestCreateFeedback_RateLimitUnavailableCarriesNoRetryAfter(t *testing.T) {
	userID := seedFeedbackOwner(t, uniqueEmail("feedback-retry-after-503"))
	accessToken := newTestAccessToken(t, userID)

	limiter := testutil.UnavailablePinRateLimiter(errors.New("dial tcp: connection refused"))

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	result := postFeedbackWithLimiter(t, app, accessToken, "android")

	require.Equal(t, http.StatusServiceUnavailable, result.status)
	require.Equal(t, "USER_RATE_LIMIT_UNAVAILABLE", result.code)
	require.Empty(t, result.retryAfter)

	count, err := feedbackTestDB().CountFeedbackForUser(
		context.Background(),
		userID,
	)
	require.NoError(t, err)
	require.Zero(t, count)
}

// The budget belongs to the account, so one user spending theirs leaves another
// user's untouched.
func TestCreateFeedback_RateLimitIsKeyedOnTheUser(t *testing.T) {
	spentUserID := seedFeedbackOwner(t, uniqueEmail("feedback-spent"))
	otherUserID := seedFeedbackOwner(t, uniqueEmail("feedback-other-budget"))

	limiter := testutil.NewCountingPinRateLimiter().
		AllowNamespace(feedback.CreateUserRateLimitNamespace, testutil.NamespaceOutcome{
			Allowed:    false,
			RetryAfter: time.Minute,
		})

	app, _ := testutil.NewAppWithLimiter(testPool, limiter)

	refused := postFeedbackWithLimiter(
		t,
		app,
		newTestAccessToken(t, spentUserID),
		"",
	)
	require.Equal(t, http.StatusTooManyRequests, refused.status)

	// The limiter is asked about the account, never the address or the report.
	require.Equal(
		t,
		[]string{spentUserID.String()},
		limiter.Subjects,
		"the budget subject must be the account that filed the report",
	)
	require.Equal(
		t,
		[]string{feedback.CreateUserRateLimitNamespace},
		limiter.Namespaces,
	)

	// A different account would be a different subject; this asserts the value that
	// was used rather than only that something was.
	require.NotEqual(t, otherUserID.String(), limiter.Subjects[0])
}
