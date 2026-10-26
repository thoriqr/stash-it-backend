package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// This is a deliberate no-change regression. Password reset shares the shape of
// the registration bug — a terminal verification request beside an unfinished
// business row — but not its safe recovery. Its reset and its continuation expire
// together at fifteen minutes, so the only window in which the request route
// returns a terminal verification id is a window in which a live continuation
// exists. Replacing it there would let any unauthenticated caller who knows an
// address cancel the owner's in-flight reset. Once the reset expires, the
// existing `ExpirePendingPasswordReset` path already recovers, so there is
// nothing for a force-expire branch to do.
//
// This test protects that decision against a future blanket application of the
// registration fix. If someone adds a verification-status branch to the reset
// request path, the second request below stops returning the existing id and the
// original continuation stops working.
func TestPasswordReset_VerifiedWithLiveContinuationIsReusedAndRemainsUsable(
	t *testing.T,
) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "verified-live-reset@example.com"

	userID, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	oldHash, err := testutil.NewPasswordHasher().Hash(ctx, "old-password")
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredentialForUser(
			ctx,
			passwordresetdbtest.CreatePasswordCredentialForUserParams{
				UserID:       userID,
				PasswordHash: oldHash,
			},
		),
	)

	testEmailSender.Messages = nil

	status, body := postPasswordReset(t, emailAddress)
	require.Equal(t, http.StatusCreated, status)
	require.NotNil(t, body.Data)

	verificationID := body.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// Issue the code the way a client would, then read the PIN out of the mail
	// that was actually sent. The code is six random digits, so no value may be
	// assumed.
	pinResponse := postPasswordResetPIN(t, verificationID)
	require.Equal(t, http.StatusCreated, pinResponse.StatusCode)

	require.Len(t, testEmailSender.Messages, 1)

	issuedPIN := strings.TrimPrefix(
		testEmailSender.Messages[0].Text,
		passwordResetPINPrefix,
	)
	require.Len(t, issuedPIN, 6, "a verification code is six digits")

	verified := verifyPasswordResetPIN(verificationID, issuedPIN)
	require.NoError(t, verified.err)
	require.Equal(t, http.StatusOK, verified.status)
	require.NotEmpty(t, verified.token)

	// Request the reset again while its continuation is still usable. The
	// request route must reuse the open reset rather than replace it.
	secondStatus, secondBody := postPasswordReset(t, emailAddress)
	require.Equal(t, http.StatusCreated, secondStatus)
	require.NotNil(t, secondBody.Data)
	require.Equal(
		t,
		verificationID,
		secondBody.Data.VerificationID,
		"a verified reset with a live continuation must preserve its verification id",
	)

	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.PendingResetCount)
	require.Equal(t, int64(1), counts.VerificationRequestCount)

	// The original continuation is still usable after that request.
	continuationRequest := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/continuation",
		nil,
	)
	continuationRequest.Header.Set(
		"X-Password-Reset-Continuation",
		verified.token,
	)

	continuationResponse, err := testApp.Test(continuationRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, continuationResponse.StatusCode)

	// And it can still complete the reset through the existing flow.
	finalized := finalizePasswordReset(verified.token, "new-password")
	require.NoError(t, finalized.err)
	require.Equal(t, http.StatusOK, finalized.status)

	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
	require.True(
		t,
		finalState.ConsumedAt.Valid,
		"the continuation must have been consumed by the finalization",
	)
	require.NotEqual(
		t,
		oldHash,
		finalState.PasswordHash,
		"finalization must have written the new password",
	)
	require.Equal(t, int64(0), finalState.ActiveSessionCount)
}

func postPasswordResetPIN(
	t *testing.T,
	verificationID uuid.UUID,
) *http.Response {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/pin",
		nil,
	)

	response, err := testApp.Test(request)
	require.NoError(t, err)

	return response
}
