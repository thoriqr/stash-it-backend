package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	session "github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

func TestSession_CreateSession(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "session@test.com",
		DisplayName: "Session Test User",
	})
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	absoluteExpiresAt := time.Now().Add(24 * time.Hour)
	refreshTokenHash := "test-refresh-token-hash"

	createdSession, createdRefreshToken, err := repository.CreateSession(
		ctx,
		sessiondb.CreateSessionParams{
			UserID:   userID,
			Platform: "web",
			InstallationID: pgtype.UUID{
				Bytes: [16]byte{1, 2, 3},
				Valid: true,
			},
			DeviceName: pgtype.Text{
				String: "Chrome",
				Valid:  true,
			},
			UserAgent: pgtype.Text{
				String: "Mozilla/5.0",
				Valid:  true,
			},
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  absoluteExpiresAt,
				Valid: true,
			},
		},
		refreshTokenHash,
	)
	require.NoError(t, err)

	assert.NotEqual(t, userID, createdSession.ID)
	assert.Equal(t, userID, createdSession.UserID)
	assert.Equal(t, "web", createdSession.Platform)

	assert.Equal(t, refreshTokenHash, createdRefreshToken.TokenHash)
	assert.Equal(t, createdSession.ID, createdRefreshToken.SessionID)

	assert.True(t, createdSession.InstallationID.Valid)
	assert.Equal(
		t,
		[16]byte{1, 2, 3},
		createdSession.InstallationID.Bytes,
	)

	assert.True(t, createdSession.DeviceName.Valid)
	assert.Equal(t, "Chrome", createdSession.DeviceName.String)

	assert.True(t, createdSession.UserAgent.Valid)
	assert.Equal(t, "Mozilla/5.0", createdSession.UserAgent.String)

	assert.True(t, createdSession.CreatedAt.Valid)
	assert.True(t, createdSession.LastActivityAt.Valid)
	assert.True(t, createdSession.AbsoluteExpiresAt.Valid)
	assert.WithinDuration(
		t,
		absoluteExpiresAt,
		createdSession.AbsoluteExpiresAt.Time,
		time.Second,
	)

	assert.False(t, createdSession.RevokedAt.Valid)
	assert.True(t, createdRefreshToken.IssuedAt.Valid)
	assert.False(t, createdRefreshToken.ReplacedBy.Valid)
}

// CreateSession writes two rows in one transaction: the session, and the refresh
// token that belongs to it. The second write is the one that can fail, because
// token_hash carries a unique index, so this is the write whose failure has to
// undo the first.
//
// The service cannot produce that failure on its own — a refresh token is 32
// random bytes, so the hashes never collide in practice — which is exactly why it
// is worth forcing here. The trigger is a real constraint violation rather than
// an injected fault, so what is exercised is the transaction the repository
// actually opens, not a stand-in for it.
func TestSession_CreateSession_RollsBackWhenRefreshTokenInsertFails(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "rollback@test.com",
		DisplayName: "Rollback Test User",
	})
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	// Held by the first, successful CreateSession below, and collided on by the
	// second.
	const duplicateHash = "duplicate-refresh-token-hash"

	newSessionParams := func() sessiondb.CreateSessionParams {
		return sessiondb.CreateSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
		}
	}

	createdSession, createdRefreshToken, err := repository.CreateSession(
		ctx,
		newSessionParams(),
		duplicateHash,
	)
	require.NoError(t, err)

	_, _, err = repository.CreateSession(
		ctx,
		newSessionParams(),
		duplicateHash,
	)
	require.Error(t, err)

	// The failure is the unique index and not something incidental, so this test
	// cannot quietly start passing for a different reason than the one it names.
	var pgErr *pgconn.PgError

	require.ErrorAs(t, err, &pgErr)
	require.Equal(
		t,
		"refresh_tokens_token_hash_idx",
		pgErr.ConstraintName,
		"the duplicate hash must be what fails the insert",
	)

	appErr := apperror.FromError(err)

	require.Equal(t, http.StatusInternalServerError, appErr.Status)
	require.Equal(t, apperror.CodeInternal, appErr.Code)

	// No orphan. The sessions insert succeeded inside the transaction that then
	// failed, so a missing rollback would leave a second session row with no
	// token on it, and this count would be 2.
	sessionCount, err := db.CountAllSessionsForUser(ctx, userID)
	require.NoError(t, err)

	require.Equal(
		t,
		int64(1),
		sessionCount,
		"a failed CreateSession must not leave a session row behind",
	)

	// And the row that did survive is the one that should have: still owned by
	// the user, still active, and still the parent of the only token.
	survivingSession, err := db.GetSessionState(ctx, createdSession.ID)
	require.NoError(t, err)

	require.Equal(t, userID, survivingSession.UserID)
	require.False(t, survivingSession.RevokedAt.Valid)

	survivingTokens, err := db.GetRefreshTokensForSession(ctx, createdSession.ID)
	require.NoError(t, err)

	require.Len(t, survivingTokens, 1)
	require.Equal(t, createdRefreshToken.ID, survivingTokens[0].ID)
	require.False(t, survivingTokens[0].ReplacedBy.Valid)
}

// There is deliberately no unlocked refresh-token read to test. The tests below
// all go through RefreshToken, which is the only way a refresh token is looked
// up, and it holds a row lock on the token and its session for the whole
// decision. The join those tests rely on is asserted through them: a rotation
// cannot report the token's session unless the same join produced it.
//
// An unknown token is still covered, by TestSession_RefreshToken_InvalidToken.

func TestSession_RefreshToken(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "rotate@test.com",
		DisplayName: "Rotate Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(ctx, sessiondbtest.CreateTestSessionParams{
		UserID:   userID,
		Platform: "web",
		AbsoluteExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	})
	require.NoError(t, err)

	oldHash := "old-refresh-token-hash"
	newHash := "new-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	// Save the session state before refresh.
	oldLastActivityAt := sessionRecord.LastActivityAt.Time

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	currentToken, newToken, err := repository.RefreshToken(
		ctx,
		oldHash,
		newHash,
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)
	require.NoError(t, err)

	// Verify the token returned as the current token.
	assert.Equal(t, oldToken.ID, currentToken.ID)
	assert.Equal(t, sessionRecord.ID, currentToken.SessionID)
	assert.Equal(t, userID, currentToken.UserID)

	// Verify the new refresh token.
	assert.NotEqual(t, oldToken.ID, newToken.ID)
	assert.Equal(t, sessionRecord.ID, newToken.SessionID)
	assert.Equal(t, newHash, newToken.TokenHash)
	assert.True(t, newToken.IssuedAt.Valid)

	// Verify the old token state after the transaction committed.
	updatedOldToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.True(t, updatedOldToken.ReplacedBy.Valid)
	assert.Equal(
		t,
		newToken.ID,
		uuid.UUID(updatedOldToken.ReplacedBy.Bytes),
	)

	// Verify the session activity was updated.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.LastActivityAt.Valid)
	assert.True(
		t,
		updatedSession.LastActivityAt.Time.After(oldLastActivityAt),
	)
}

// A token hash that names no row is refused as an invalid refresh token. The
// mapping from "no such row" to that error now lives only on the RefreshToken
// path, so it is pinned here rather than through a separate unlocked read.
func TestSession_RefreshToken_InvalidToken(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err := repository.RefreshToken(
		ctx,
		"non-existent-token-hash",
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeRefreshTokenInvalid, appErr.Code)
}

func TestSession_RefreshToken_Revoked(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "revoked@test.com",
		DisplayName: "Revoked Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{
				Time:  time.Now(),
				Valid: true,
			},
		},
	)
	require.NoError(t, err)

	oldHash := "revoked-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionRevoked, appErr.Code)

	// Make sure no new refresh token was created.
	_, err = db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)
}

func TestSession_RefreshToken_Replaced(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "replaced@test.com",
		DisplayName: "Replaced Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	oldHash := "old-replaced-token-hash"
	replacedHash := "already-replaced-token-hash"

	replacedToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  replacedHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID: sessionRecord.ID,
			TokenHash: oldHash,
			ReplacedBy: pgtype.UUID{
				Bytes: replacedToken.ID,
				Valid: true,
			},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionRevoked, appErr.Code)

	currentOldToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.True(t, currentOldToken.ReplacedBy.Valid)
	assert.Equal(
		t,
		replacedToken.ID,
		uuid.UUID(currentOldToken.ReplacedBy.Bytes),
	)

	// The session is revoked. This is the whole point of the policy, and it is a
	// separate write from the one the refused request made, so it has to be read
	// back rather than inferred from the 401: returning SESSION_REVOKED commits
	// nothing here, and only the committed write ends the session. The
	// concurrent case in auth_session_concurrency_test.go asserts the same thing
	// for the racing path; this is the sequential one, which is what an attacker
	// replaying a stolen token actually exercises.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	require.True(
		t,
		updatedSession.RevokedAt.Valid,
		"reuse of a rotated token must leave the session revoked",
	)

	// The whole token chain, which is the only way to tell "revoked the session
	// and stopped" from "revoked the session and rotated anyway".
	//
	// Two rows, the same two that were seeded. A rejected reuse must not issue a
	// successor, and must not disturb the link the previous rotation wrote: the
	// token that was already replaced still names the token that replaced it, and
	// the token that was live is still live, so nothing was consumed.
	tokens, err := db.GetRefreshTokensForSession(ctx, sessionRecord.ID)
	require.NoError(t, err)

	require.Len(
		t,
		tokens,
		2,
		"a rejected reuse must not issue a refresh token",
	)

	tokenIDs := make(map[uuid.UUID]bool, len(tokens))
	for _, token := range tokens {
		tokenIDs[token.ID] = true
	}

	require.True(t, tokenIDs[oldToken.ID], "the rotated token must still be there")
	require.True(
		t,
		tokenIDs[replacedToken.ID],
		"the live token must still be there",
	)

	for _, token := range tokens {
		if token.ID == replacedToken.ID {
			require.False(
				t,
				token.ReplacedBy.Valid,
				"the live token must not be rotated by a refused reuse",
			)
		}
	}
}

func TestSession_RefreshToken_AbsoluteExpired(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "absolute-expired@test.com",
		DisplayName: "Absolute Expired Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(-time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	oldHash := "absolute-expired-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: 30 * 24 * time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionExpired, appErr.Code)

	// Verify the session was revoked.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.RevokedAt.Valid)

	// Verify the old refresh token was not replaced.
	updatedToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.False(t, updatedToken.ReplacedBy.Valid)
}

func TestSession_RefreshToken_IdleExpired(t *testing.T) {
	ctx := context.Background()

	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "idle-expired@test.com",
		DisplayName: "Idle Expired Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	lastActivityAt := time.Now().Add(-2 * time.Hour)

	require.NoError(
		t,
		db.UpdateTestSessionLastActivity(
			ctx,
			sessiondbtest.UpdateTestSessionLastActivityParams{
				LastActivityAt: pgtype.Timestamptz{
					Time:  lastActivityAt,
					Valid: true,
				},
				ID: sessionRecord.ID,
			},
		),
	)

	oldHash := "idle-expired-refresh-token-hash"

	oldToken, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  oldHash,
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	repository := session.NewRepository(
		sessiondb.New(testPool),
		testPool,
	)

	_, _, err = repository.RefreshToken(
		ctx,
		oldHash,
		"new-refresh-token-hash",
		session.RefreshTokenPolicy{
			IdleLifetime: time.Hour,
		},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	assert.Equal(t, http.StatusUnauthorized, appErr.Status)
	assert.Equal(t, session.CodeSessionExpired, appErr.Code)

	// Verify the session was revoked.
	updatedSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)

	assert.True(t, updatedSession.RevokedAt.Valid)

	// Verify the old refresh token was not replaced.
	updatedToken, err := db.GetRefreshTokenState(ctx, oldToken.ID)
	require.NoError(t, err)

	assert.False(t, updatedToken.ReplacedBy.Valid)
}

// newTestSessionToken mints an access token naming a specific session.
//
// newTestAccessToken picks a random one, which is right for tests that only need a
// caller to be authenticated. Logout acts on the session named by the token, so
// these tests need to choose it, including choosing a session that is not the
// caller's.
func newTestSessionToken(
	t *testing.T,
	userID uuid.UUID,
	sessionID uuid.UUID,
) string {
	t.Helper()

	token, err := security.NewAccessTokenGenerator(
		[]byte(testutil.TestAccessTokenSecret),
	).Generate(userID, sessionID, time.Hour)
	require.NoError(t, err)

	return token
}

// logoutOverHTTP calls POST /auth/logout with the given token and returns the
// status and body.
func logoutOverHTTP(t *testing.T, token string) (int, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

// seedUserWithActiveSession creates a user with a session that is not revoked and
// returns both ids, so a test can act on either side of an ownership boundary.
func seedUserWithActiveSession(
	t *testing.T,
	db *sessiondbtest.Queries,
	email string,
) (userID uuid.UUID, sessionID uuid.UUID) {
	t.Helper()

	ctx := context.Background()

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       email,
		DisplayName: "Logout Test User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  time.Now().Add(24 * time.Hour),
				Valid: true,
			},
			RevokedAt: pgtype.Timestamptz{},
		},
	)
	require.NoError(t, err)

	return userID, sessionRecord.ID
}

// Logout revokes the session the token names, and only if that session belongs to
// the token's user. Both identifiers come from the verified token, never from the
// request, but the ownership check still belongs in the query: the session id is
// data, and data is what the check is about.
//
// A caller whose token names somebody else's session must not be able to revoke
// it. The response is a success either way, because logout reports the caller's
// intent rather than what it hit, and a different response would disclose whether
// a session id exists.
func TestSession_Logout_CannotRevokeAnotherUsersSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	attackerID, _ := seedUserWithActiveSession(t, db, "logout-attacker@test.com")
	victimID, victimSessionID := seedUserWithActiveSession(t, db, "logout-victim@test.com")

	// The attacker presents a valid token of their own whose session claim names
	// the victim's session. Both halves are signed by this server, so this is not
	// a forgery: it is exactly what a session id taken from elsewhere would look
	// like reaching the handler.
	token := newTestSessionToken(t, attackerID, victimSessionID)

	status, body := logoutOverHTTP(t, token)

	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "logout successful")

	// The victim's session is untouched.
	victimSession, err := db.GetSessionState(ctx, victimSessionID)
	require.NoError(t, err)

	require.False(
		t,
		victimSession.RevokedAt.Valid,
		"one user's logout must not revoke another user's session",
	)
	require.Equal(t, victimID, victimSession.UserID)
}

// The counterpart: the owner's token does revoke their own session, so the guard
// above is not simply refusing everything.
func TestSession_Logout_RevokesOwnSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, sessionID := seedUserWithActiveSession(t, db, "logout-owner@test.com")

	token := newTestSessionToken(t, userID, sessionID)

	status, body := logoutOverHTTP(t, token)

	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "logout successful")

	updatedSession, err := db.GetSessionState(ctx, sessionID)
	require.NoError(t, err)

	require.True(t, updatedSession.RevokedAt.Valid)
}

// Logging out twice is still a success. The caller's intent — that they hold no
// usable session — already held, so the second call has nothing to do and nothing
// to report. The same holds for a session that was never theirs.
func TestSession_Logout_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, sessionID := seedUserWithActiveSession(t, db, "logout-twice@test.com")

	token := newTestSessionToken(t, userID, sessionID)

	firstStatus, _ := logoutOverHTTP(t, token)
	require.Equal(t, http.StatusOK, firstStatus)

	secondStatus, secondBody := logoutOverHTTP(t, token)
	require.Equal(t, http.StatusOK, secondStatus)
	require.Contains(t, secondBody, "logout successful")

	// A session id that names nothing at all is the same silence, so a caller
	// cannot learn whether an id exists by logging out of it.
	unknownToken := newTestSessionToken(t, userID, uuid.New())

	unknownStatus, unknownBody := logoutOverHTTP(t, unknownToken)
	require.Equal(t, http.StatusOK, unknownStatus)
	require.Contains(t, unknownBody, "logout successful")
}
