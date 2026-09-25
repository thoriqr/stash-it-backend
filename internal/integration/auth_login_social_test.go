package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

func TestLoginGoogle_Authenticated(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "google@example.com"
	displayName := "Google User"
	providerSubject := "google-subject-123"

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: displayName,
		},
	)
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreateGoogleAuthIdentity(
			ctx,
			logintestdb.CreateGoogleAuthIdentityParams{
				UserID:          userID,
				ProviderSubject: providerSubject,
				EmailSnapshot: pgtype.Text{
					String: email,
					Valid:  true,
				},
				DisplayNameSnapshot: pgtype.Text{
					String: displayName,
					Valid:  true,
				},
			},
		),
	)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login/google",
		strings.NewReader(`{
			"id_token": "test-google-id-token"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Outcome      login.LoginOutcome `json:"outcome"`
			AccessToken  string             `json:"access_token"`
			RefreshToken string             `json:"refresh_token"`
			User         struct {
				ID          string `json:"id"`
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		login.LoginOutcomeAuthenticated,
		body.Data.Outcome,
	)

	require.NotEmpty(t, body.Data.AccessToken)
	require.NotEmpty(t, body.Data.RefreshToken)

	require.Equal(t, userID.String(), body.Data.User.ID)
	require.Equal(t, email, body.Data.User.Email)
	require.Equal(t, displayName, body.Data.User.DisplayName)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, int64(1), sessionCount)
}

func TestLoginGoogle_AccountLinkRequired(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "google@example.com"
	displayName := "Existing User"

	_, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: displayName,
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login/google",
		strings.NewReader(`{
			"id_token": "test-google-id-token"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Outcome        login.LoginOutcome `json:"outcome"`
			Provider       string             `json:"provider"`
			ConfirmationID string             `json:"confirmation_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		login.LoginOutcomeAccountLinkRequired,
		body.Data.Outcome,
	)

	require.Equal(t, "google", body.Data.Provider)
	require.NotEmpty(t, body.Data.ConfirmationID)
}

func TestLoginGoogle_RegistrationRequired(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login/google",
		strings.NewReader(`{
			"id_token": "test-google-id-token"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Outcome         login.LoginOutcome `json:"outcome"`
			VerificationID string             `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		login.LoginOutcomeRegistrationRequired,
		body.Data.Outcome,
	)

	require.NotEmpty(t, body.Data.VerificationID)

	verificationID, err := uuid.Parse(body.Data.VerificationID)
	require.NoError(t, err)

	state, err := db.GetSocialRegistrationState(
		ctx,
		verificationID,
	)
	require.NoError(t, err)

	require.Equal(t, verificationID, state.VerificationID)

	require.NotEqual(
		t,
		uuid.Nil,
		state.PendingRegistrationID,
	)

	require.Equal(
		t,
		"google@example.com",
		state.Email,
	)

	require.Equal(
		t,
		"social",
		state.RegistrationType,
	)

	require.Equal(
		t,
		"pending",
		state.RegistrationStatus,
	)

	require.Equal(
		t,
		"google",
		state.Provider,
	)

	require.Equal(
		t,
		"google-subject-123",
		state.ProviderSubject,
	)

	require.True(t, state.EmailSnapshot.Valid)
	require.Equal(
		t,
		"google@example.com",
		state.EmailSnapshot.String,
	)

	require.True(t, state.DisplayNameSnapshot.Valid)
	require.Equal(
		t,
		"Google User",
		state.DisplayNameSnapshot.String,
	)
}

func TestGetAccountLinkConfirmation(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "google@example.com"
	displayName := "Existing User"
	providerSubject := "google-subject-123"

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: displayName,
		},
	)
	require.NoError(t, err)

	confirmationID, err := db.CreateAccountLinkConfirmation(
		ctx,
		logintestdb.CreateAccountLinkConfirmationParams{
			UserID:          userID,
			Provider:        "google",
			ProviderSubject: providerSubject,
			EmailSnapshot: pgtype.Text{
				String: "google@example.com",
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/login/google/account-link/"+confirmationID.String(),
		nil,
	)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			ID                  string `json:"id"`
			Provider            string `json:"provider"`
			EmailSnapshot       string `json:"email_snapshot"`
			DisplayNameSnapshot string `json:"display_name_snapshot"`
			UserEmail           string `json:"user_email"`
			UserDisplayName     string `json:"user_display_name"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, confirmationID.String(), body.Data.ID)
	require.Equal(t, "google", body.Data.Provider)
	require.Equal(t, "google@example.com", body.Data.EmailSnapshot)
	require.Equal(t, "Google User", body.Data.DisplayNameSnapshot)
	require.Equal(t, email, body.Data.UserEmail)
	require.Equal(t, displayName, body.Data.UserDisplayName)
}

func TestConfirmAccountLink(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	email := "user@example.com"
	displayName := "Existing User"
	providerSubject := "google-subject-123"

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       email,
			DisplayName: displayName,
		},
	)
	require.NoError(t, err)

	confirmationID, err := db.CreateAccountLinkConfirmation(
		ctx,
		logintestdb.CreateAccountLinkConfirmationParams{
			UserID:          userID,
			Provider:        "google",
			ProviderSubject: providerSubject,
			EmailSnapshot: pgtype.Text{
				String: "google@example.com",
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login/google/account-link/"+confirmationID.String()+"/confirm",
		nil,
	)

	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Outcome      login.LoginOutcome `json:"outcome"`
			AccessToken  string             `json:"access_token"`
			RefreshToken string             `json:"refresh_token"`
			User         struct {
				ID          string `json:"id"`
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		login.LoginOutcomeAuthenticated,
		body.Data.Outcome,
	)

	require.NotEmpty(t, body.Data.AccessToken)
	require.NotEmpty(t, body.Data.RefreshToken)

	require.Equal(t, userID.String(), body.Data.User.ID)
	require.Equal(t, email, body.Data.User.Email)
	require.Equal(t, displayName, body.Data.User.DisplayName)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, int64(1), sessionCount)

	confirmationState, err := db.GetAccountLinkConfirmationState(
		ctx,
		confirmationID,
	)
	require.NoError(t, err)

	require.Equal(t, confirmationID, confirmationState.ID)
	require.Equal(t, userID, confirmationState.UserID)
	require.Equal(t, "google", confirmationState.Provider)
	require.Equal(
		t,
		providerSubject,
		confirmationState.ProviderSubject,
	)

	require.True(t, confirmationState.EmailSnapshot.Valid)
	require.Equal(
		t,
		"google@example.com",
		confirmationState.EmailSnapshot.String,
	)

	require.True(t, confirmationState.DisplayNameSnapshot.Valid)
	require.Equal(
		t,
		"Google User",
		confirmationState.DisplayNameSnapshot.String,
	)

	require.True(t, confirmationState.ConfirmedAt.Valid)

	identity, err := db.GetGoogleAuthIdentityState(
		ctx,
		logintestdb.GetGoogleAuthIdentityStateParams{
			UserID:          userID,
			ProviderSubject: providerSubject,
		},
	)
	require.NoError(t, err)

	require.NotEqual(t, uuid.Nil, identity.ID)
	require.Equal(t, userID, identity.UserID)
	require.Equal(t, "google", identity.Provider)
	require.Equal(t, providerSubject, identity.ProviderSubject)

	require.True(t, identity.EmailSnapshot.Valid)
	require.Equal(
		t,
		"google@example.com",
		identity.EmailSnapshot.String,
	)

	require.True(t, identity.DisplayNameSnapshot.Valid)
	require.Equal(
		t,
		"Google User",
		identity.DisplayNameSnapshot.String,
	)
}