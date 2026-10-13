package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

// revokeSessionOverHTTP calls DELETE /auth/sessions/:session_id with the given
// token and returns the status and body.
//
// The session id is a path parameter here, which is what makes this the one
// endpoint in the session feature where the caller names the thing being acted
// on. Logout cannot be aimed at a session its caller chose: both identifiers come
// out of the verified token. This one can, so its ownership guard is the only
// thing standing between a valid token and somebody else's device list.
func revokeSessionOverHTTP(
	t *testing.T,
	token string,
	sessionID uuid.UUID,
) (int, string) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodDelete,
		"/auth/sessions/"+sessionID.String(),
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

// The counterpart to the logout ownership tests, and the one that matters more.
//
// The attacker presents a token that is genuinely theirs and names the victim's
// session in the path. Nothing about the request is malformed or forged; it is
// exactly what a session id picked up from a shared machine, a screenshot or a
// support transcript reaches the handler as.
//
// The refusal has to be the endpoint's existing one. RevokeSessionForUser
// returns RESOURCE_NOT_FOUND when its update matches nothing, which is the same
// answer a session id that names nothing at all gets — and the assertions below
// pin that sameness, because a distinguishable response for "exists but is not
// yours" would turn this endpoint into a probe for which session ids are real.
func TestSession_RevokeSession_CannotRevokeAnotherUsersSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	attackerID, attackerSessionID := seedUserWithActiveSession(
		t,
		db,
		"revoke-attacker@test.com",
	)
	victimID, victimSessionID := seedUserWithActiveSession(
		t,
		db,
		"revoke-victim@test.com",
	)

	// A token naming the attacker's own session, so the request is authenticated
	// as them and only the path parameter disagrees.
	attackerToken := newTestSessionToken(t, attackerID, attackerSessionID)

	status, body := revokeSessionOverHTTP(t, attackerToken, victimSessionID)

	require.Equal(
		t,
		http.StatusNotFound,
		status,
		"revoking another user's session must be refused",
	)
	require.Contains(t, body, apperror.CodeNotFound)

	// The victim's session is untouched.
	victimSession, err := db.GetSessionState(ctx, victimSessionID)
	require.NoError(t, err)

	require.False(
		t,
		victimSession.RevokedAt.Valid,
		"one user must not be able to revoke another user's session",
	)
	require.Equal(
		t,
		victimID,
		victimSession.UserID,
		"the session must still belong to the user it always did",
	)

	// The attacker's own session was not spent trying, so this cannot be
	// satisfied by refusing everything.
	attackerSession, err := db.GetSessionState(ctx, attackerSessionID)
	require.NoError(t, err)

	require.False(t, attackerSession.RevokedAt.Valid)

	// An id that names nothing produces the same refusal, which is what makes the
	// 404 above non-enumerating.
	unknownStatus, unknownBody := revokeSessionOverHTTP(
		t,
		attackerToken,
		uuid.New(),
	)

	require.Equal(t, http.StatusNotFound, unknownStatus)
	require.Contains(t, unknownBody, apperror.CodeNotFound)
}

// The other half: the owner can revoke their own session, so the guard above is
// a check and not a blanket refusal.
func TestSession_RevokeSession_RevokesOwnSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, sessionID := seedUserWithActiveSession(t, db, "revoke-owner@test.com")

	token := newTestSessionToken(t, userID, sessionID)

	status, body := revokeSessionOverHTTP(t, token, sessionID)

	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "session revoked successfully")

	updatedSession, err := db.GetSessionState(ctx, sessionID)
	require.NoError(t, err)

	require.True(
		t,
		updatedSession.RevokedAt.Valid,
		"the owner must be able to revoke their own session",
	)
}
