package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestAuth_ValidToken(t *testing.T) {
	secret := []byte("test-secret")
	generator := security.NewAccessTokenGenerator(secret)
	verifier := security.NewAccessTokenVerifier(secret)

	userID := uuid.New()
	sessionID := uuid.New()

	token, err := generator.Generate(userID, sessionID, time.Hour)
	require.NoError(t, err)

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})

	var receivedClaims security.AccessTokenClaims

	app.Get(
		"/protected",
		Auth(verifier),
		func(c fiber.Ctx) error {
			claims, ok := c.Locals(AuthClaimsKey).(security.AccessTokenClaims)
			require.True(t, ok)

			receivedClaims = claims

			return c.SendStatus(fiber.StatusOK)
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	require.NoError(t, err)

	require.Equal(t, fiber.StatusOK, resp.StatusCode)
	require.Equal(t, userID.String(), receivedClaims.Subject)
	require.Equal(t, sessionID, receivedClaims.SessionID)
}

func TestAuth_InvalidAuthorizationHeader(t *testing.T) {
	verifier := security.NewAccessTokenVerifier([]byte("test-secret"))

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})

	app.Get("/protected", Auth(verifier))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)

	resp, err := app.Test(req)
	require.NoError(t, err)

	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, CodeInvalidAuthorizationHeader, body.Error.Code)
}

func TestAuth_EmptyBearerToken(t *testing.T) {
	verifier := security.NewAccessTokenVerifier([]byte("test-secret"))

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})

	app.Get("/protected", Auth(verifier))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer ")

	resp, err := app.Test(req)
	require.NoError(t, err)

	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, CodeInvalidAuthorizationHeader, body.Error.Code)
}

func TestAuth_InvalidAccessToken(t *testing.T) {
	verifier := security.NewAccessTokenVerifier([]byte("test-secret"))

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})

	app.Get("/protected", Auth(verifier))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")

	resp, err := app.Test(req)
	require.NoError(t, err)

	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, CodeInvalidAccessToken, body.Error.Code)
}

func TestAuth_ExpiredAccessToken(t *testing.T) {
	secret := []byte("test-secret")
	generator := security.NewAccessTokenGenerator(secret)
	verifier := security.NewAccessTokenVerifier(secret)

	token, err := generator.Generate(
		uuid.New(),
		uuid.New(),
		-time.Second,
	)
	require.NoError(t, err)

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
	})

	app.Get("/protected", Auth(verifier))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	require.NoError(t, err)

	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, CodeAccessTokenExpired, body.Error.Code)
}