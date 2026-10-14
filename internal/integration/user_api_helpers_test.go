package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	userdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/user/generated"
)

func userTestDB() *userdbtest.Queries {
	return userdbtest.New(testPool)
}

// newSessionAccessToken mints a token naming one real session.
//
// newTestAccessToken uses uuid.New() for the sid, which is fine for routes that
// never look at it. The password routes do, so this mints against a session that
// actually exists — which is also what makes "the caller's own session was spared"
// an assertion about a row rather than about an absent one.
func newSessionAccessToken(t *testing.T, userID uuid.UUID, sessionID uuid.UUID) string {
	t.Helper()

	generator := security.NewAccessTokenGenerator(
		[]byte(testutil.TestAccessTokenSecret),
	)

	accessToken, err := generator.Generate(userID, sessionID, time.Hour)
	require.NoError(t, err)

	return accessToken
}

type userAPIResponse struct {
	Data struct {
		User struct {
			ID                    string `json:"id"`
			Email                 string `json:"email"`
			DisplayName           string `json:"display_name"`
			HasPasswordCredential bool   `json:"has_password_credential"`
			CreatedAt             string `json:"created_at"`
			Identities            []struct {
				Provider            string  `json:"provider"`
				LinkedAt            string  `json:"linked_at"`
				EmailSnapshot       *string `json:"email_snapshot"`
				DisplayNameSnapshot *string `json:"display_name_snapshot"`
			} `json:"identities"`
		} `json:"user"`
	} `json:"data"`
	Message string `json:"message"`
}

type userAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fields  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"fields"`
	} `json:"error"`
}

func decodeUserBody(t *testing.T, body []byte) userAPIResponse {
	t.Helper()

	var decoded userAPIResponse

	require.NoError(t, json.Unmarshal(body, &decoded))

	return decoded
}

func decodeUserError(t *testing.T, body []byte) userAPIErrorResponse {
	t.Helper()

	var decoded userAPIErrorResponse

	require.NoError(t, json.Unmarshal(body, &decoded))

	return decoded
}

// userRequestTimeout overrides Fiber's one-second test default.
//
// It is needed because these routes perform Argon2id derivations, and a
// concurrency test here deliberately runs several of them at once against a
// limiter at the suite's own concurrency. Fiber's default would time the client
// out and report a transport error rather than the status code under test, which
// would make the failure look like a defect in the endpoint. The endpoint's own
// capacity behaviour is bounded separately, by security.PasswordWorkLimiter, and
// is asserted by the unit tests with the limiter's slot held from outside.
const userRequestTimeout = 30 * time.Second

// doUserRequest drives one request against the real app with an explicit
// Authorization header, so a test can send a malformed one on purpose.
func doUserRequest(
	t *testing.T,
	method string,
	target string,
	rawBody []byte,
	authorization string,
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

	resp, err := testApp.Test(
		req,
		fiber.TestConfig{Timeout: userRequestTimeout},
	)
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, body
}

// getProfile drives GET /users/me end to end, through the real app, middleware,
// handler, service and repository.
//
// query is appended verbatim so a test can send a parameter this endpoint does not
// define, which is how "a supplied user id changes nothing" is demonstrated.
func getProfile(t *testing.T, accessToken string, query string) (int, []byte) {
	t.Helper()

	target := "/users/me"
	if query != "" {
		target += "?" + query
	}

	return doUserRequest(t, http.MethodGet, target, nil, bearer(accessToken))
}

// patchProfile drives PATCH /users/me end to end.
//
// rawBody is the body exactly as a caller means to send it, including any field
// this endpoint does not accept.
func patchProfile(
	t *testing.T,
	accessToken string,
	rawBody []byte,
) (int, []byte) {
	t.Helper()

	return doUserRequest(
		t,
		http.MethodPatch,
		"/users/me",
		rawBody,
		bearer(accessToken),
	)
}

// setPassword drives PUT /users/me/password end to end.
func setPassword(
	t *testing.T,
	accessToken string,
	body map[string]string,
) (int, []byte) {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	return doUserRequest(
		t,
		http.MethodPut,
		"/users/me/password",
		raw,
		bearer(accessToken),
	)
}

func bearer(accessToken string) string {
	if accessToken == "" {
		return ""
	}

	return "Bearer " + accessToken
}

// seedUserWithCredential creates a user who already holds a password credential
// and returns the user, a session for them, and the stored hash.
func seedUserWithCredential(
	t *testing.T,
	ctx context.Context,
	emailAddress string,
	displayName string,
	password string,
) (uuid.UUID, uuid.UUID, string) {
	t.Helper()

	db := userTestDB()

	userID, err := db.CreateUser(ctx, userdbtest.CreateUserParams{
		Email:       emailAddress,
		DisplayName: displayName,
	})
	require.NoError(t, err)

	passwordHash, err := testutil.NewPasswordHasher().Hash(ctx, password)
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredentialForUser(ctx, userdbtest.CreatePasswordCredentialForUserParams{
			UserID:       userID,
			PasswordHash: passwordHash,
		}),
	)

	session, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	return userID, session, passwordHash
}

// seedUserWithoutCredential creates an account holding no credential, which is the
// state a social registration leaves behind.
func seedUserWithoutCredential(
	t *testing.T,
	ctx context.Context,
	emailAddress string,
	displayName string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()

	db := userTestDB()

	userID, err := db.CreateUser(ctx, userdbtest.CreateUserParams{
		Email:       emailAddress,
		DisplayName: displayName,
	})
	require.NoError(t, err)

	session, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	return userID, session
}

func nameOfLength(length int) string {
	return strings.Repeat("a", length)
}
