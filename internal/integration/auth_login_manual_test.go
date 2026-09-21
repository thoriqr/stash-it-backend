package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/security"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

func TestLoginManual_Success(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "login@example.com"
	password := "password123"

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: "Login User",
		},
	)
	require.NoError(t, err)

	passwordHasher := security.NewPasswordHasher()

	passwordHash, err := passwordHasher.Hash(password)
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredential(
			ctx,
			logintestdb.CreatePasswordCredentialParams{
				UserID:       userID,
				PasswordHash: passwordHash,
			},
		),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(`{
			"email": "login@example.com",
			"password": "password123"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				ID          string `json:"id"`
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, "login successful", body.Message)
	require.NotEmpty(t, body.Data.AccessToken)
	require.NotEmpty(t, body.Data.RefreshToken)
	require.Equal(t, userID.String(), body.Data.User.ID)
	require.Equal(t, email, body.Data.User.Email)
	require.Equal(t, "Login User", body.Data.User.DisplayName)

	state, err := db.GetLoginUserState(ctx, email)
	require.NoError(t, err)

	require.Equal(t, userID, state.ID)
	require.Equal(t, email, state.Email)
	require.Equal(t, "Login User", state.DisplayName)
	require.True(t, state.PasswordUserID.Valid)
	require.Equal(t, userID, uuid.UUID(state.PasswordUserID.Bytes))

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, int64(1), sessionCount)
}

func TestLoginManual_InvalidCredentials(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "login@example.com"
	passwordHasher := security.NewPasswordHasher()

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: "Login User",
		},
	)
	require.NoError(t, err)

	passwordHash, err := passwordHasher.Hash("password123")
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredential(
			ctx,
			logintestdb.CreatePasswordCredentialParams{
				UserID:       userID,
				PasswordHash: passwordHash,
			},
		),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(`{
			"email": "login@example.com",
			"password": "wrong-password"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, login.CodeInvalidCredentials, body.Error.Code)
	require.Equal(t, "invalid email or password", body.Error.Message)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, int64(0), sessionCount)
}

func TestLoginManual_UnknownEmail(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(`{
			"email": "unknown@example.com",
			"password": "password123"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, login.CodeInvalidCredentials, body.Error.Code)
	require.Equal(t, "invalid email or password", body.Error.Message)
}