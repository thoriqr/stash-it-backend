package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// A password reset has to work for an account that has no password yet.
//
// A social registration creates no `password_credentials` row: registration's
// manual path is the only thing that inserts one, and a Google-only account never
// goes near it. Finalization therefore has to *create* the credential rather than
// update it, which is what `UpsertPasswordCredential`'s ON CONFLICT clause is for.
//
// No unit test can show any of that. A mock cannot distinguish an upsert that
// created a row from one that updated a row — that is the whole claim — so this is
// driven end to end through the real endpoints against real PostgreSQL.
//
// The flow is the production one: the PIN is read out of the message that was
// actually sent rather than pinned to a known value, because the issued code is
// six random digits and no value may be assumed.
func TestPasswordReset_EndToEndForAccountWithoutCredential(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "no-credential@example.com"
	const newPassword = "the-password-they-chose"

	// The flow creates the account — a reset cannot be opened for an address that
	// holds none — and hands back the user id the sessions are seeded against.
	userID, verificationID, issuedPIN := startPasswordResetWithPIN(
		t,
		emailAddress,
	)

	// Two sessions, because a reset is supposed to end whatever the account was
	// signed into, and one would not show that it revokes all of them.
	for range 2 {
		_, err := db.CreateSessionForUser(ctx, userID)
		require.NoError(t, err)
	}

	// The precondition, asserted rather than assumed: this account holds no
	// password, which is the whole reason the upsert path is under test.
	before, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Zero(
		t,
		before.CredentialCount,
		"this account must start with no password credential",
	)

	minted := verifyPasswordResetPIN(verificationID, issuedPIN)
	require.NoError(t, minted.err)
	require.Equal(t, http.StatusOK, minted.status, minted.code)
	require.NotEmpty(t, minted.token)

	// The continuation route is what tells the frontend whether it should ask for
	// a password at all, so for an account with none it must say so.
	continuationReq := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/continuation",
		nil,
	)
	continuationReq.Header.Set(
		"X-Password-Reset-Continuation",
		minted.token,
	)

	continuationResp, err := testApp.Test(continuationReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, continuationResp.StatusCode)

	var continuationBody struct {
		Data struct {
			Email                 string `json:"email"`
			HasPasswordCredential bool   `json:"has_password_credential"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(continuationResp.Body).Decode(&continuationBody),
	)

	require.Equal(t, emailAddress, continuationBody.Data.Email)
	require.False(
		t,
		continuationBody.Data.HasPasswordCredential,
		"an account with no password must be reported as having none",
	)

	// Reading the continuation must not have consumed it.
	stillPending, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Zero(t, stillPending.CredentialCount)

	// Finalize.
	finalized := postResetFinalizeFrom(
		t,
		testApp,
		minted.token,
		newPassword,
		"",
	)

	require.Equal(
		t,
		http.StatusOK,
		finalized.status,
		"a passwordless account must be able to complete a reset: %s",
		finalized.code,
	)

	// Exactly one credential now exists, holding the new password.
	after, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		int64(1),
		after.CredentialCount,
		"finalization must create the credential rather than duplicate it",
	)

	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)

	require.NotEqual(
		t,
		newPassword,
		finalState.PasswordHash,
		"the password must be stored hashed, never in the clear",
	)

	verified, err := testutil.NewPasswordHasher().Verify(
		ctx,
		newPassword,
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.True(
		t,
		verified.Match,
		"the stored credential must verify against the submitted password",
	)

	// The reset is finished and the capability is spent.
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
	require.True(
		t,
		finalState.ConsumedAt.Valid,
		"the continuation must be consumed, or it could be replayed",
	)

	// Every session the account had is gone.
	require.Zero(
		t,
		finalState.ActiveSessionCount,
		"a successful reset must revoke every active session",
	)

	// And the credential is a usable one: the account can now be signed into with
	// the password it just set, which is the only externally visible proof that
	// the row written is a real credential rather than a well-formed row.
	signedIn := postLogin(t, testApp, emailAddress, newPassword)
	require.Equal(
		t,
		http.StatusOK,
		signedIn.status,
		"the account must be able to sign in with the new password: %s",
		signedIn.code,
	)

	// Replaying the continuation is refused, so the reset cannot be re-run to
	// change the password a second time.
	replayed := postResetFinalizeFrom(
		t,
		testApp,
		minted.token,
		"a-different-password",
		"",
	)

	require.Equal(t, http.StatusConflict, replayed.status)
	require.Equal(
		t,
		passwordreset.CodePasswordResetContinuationConsumed,
		replayed.code,
	)
}

// The continuation still reports "no password" after the reset has been
// completed only in the sense that the row it reads is the live one; what matters
// here is that an account which had a credential all along still reports one.
//
// This is the case the existing end-to-end test covers, and it is restated here
// only to make the contrast explicit: the two accounts differ in exactly one row,
// and the reset works for both.
func TestPasswordReset_ContinuationReportsCredentialPresenceAccurately(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	tests := []struct {
		name             string
		email            string
		seedCredential   bool
		expectedHasValue bool
	}{
		{
			name:             "account with a password",
			email:            "has-credential@example.com",
			seedCredential:   true,
			expectedHasValue: true,
		},
		{
			name:             "account without a password",
			email:            "no-credential-2@example.com",
			seedCredential:   false,
			expectedHasValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID, verificationID, issuedPIN := startPasswordResetWithPIN(
				t,
				tt.email,
			)

			if tt.seedCredential {
				hash, hashErr := testutil.NewPasswordHasher().Hash(
					ctx,
					"a-password-that-already-existed",
				)
				require.NoError(t, hashErr)

				require.NoError(
					t,
					db.CreatePasswordCredentialForUser(
						ctx,
						passwordresetdbtest.CreatePasswordCredentialForUserParams{
							UserID:       userID,
							PasswordHash: hash,
						},
					),
				)
			}

			minted := verifyPasswordResetPIN(verificationID, issuedPIN)
			require.NoError(t, minted.err)
			require.Equal(t, http.StatusOK, minted.status, minted.code)

			continuationReq := httptest.NewRequest(
				http.MethodGet,
				"/auth/password-reset/continuation",
				nil,
			)
			continuationReq.Header.Set(
				"X-Password-Reset-Continuation",
				minted.token,
			)

			continuationResp, err := testApp.Test(continuationReq)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, continuationResp.StatusCode)

			var body struct {
				Data struct {
					HasPasswordCredential bool `json:"has_password_credential"`
				} `json:"data"`
			}

			require.NoError(
				t,
				json.NewDecoder(continuationResp.Body).Decode(&body),
			)

			require.Equal(t, tt.expectedHasValue, body.Data.HasPasswordCredential)
		})
	}
}