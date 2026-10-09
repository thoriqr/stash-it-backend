package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

// The tests in this file exercise the real service, repository and PostgreSQL
// together. What they are about cannot be proven with a mock repository: every
// assertion below is about which transaction wins a row lock, and about what the
// database holds afterwards.
//
// Synchronization is a barrier, never a sleep. Each participant parks on a
// channel that is closed once every participant is ready, so they contend inside
// the same window instead of being issued one after another. Context deadlines
// exist only so a blocked lock surfaces as a failure rather than as a hung test.

// concurrencyTimeout bounds a contended operation. It is generous because the
// point is to fail a deadlock, not to race it.
const concurrencyTimeout = 30 * time.Second

// seedSessionWithToken creates a user, an active session and one active refresh
// token, and returns the service, the session id, the token row id and the raw
// token a client would hold.
//
// The raw token is what the service is given, and the row stores its hash, which
// is the arrangement CreateSession produces. Seeding the raw value instead would
// make the lookup miss and the test would assert nothing.
func seedSessionWithToken(
	t *testing.T,
	email string,
	rawToken string,
) (session.SessionService, uuid.UUID, uuid.UUID, string) {
	t.Helper()

	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       email,
		DisplayName: "Concurrency Test User",
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

	token, err := db.CreateTestRefreshToken(
		ctx,
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionRecord.ID,
			TokenHash:  security.HashToken(rawToken),
			ReplacedBy: pgtype.UUID{},
		},
	)
	require.NoError(t, err)

	service := session.NewService(
		session.NewRepository(sessiondb.New(testPool), testPool),
		security.NewAccessTokenGenerator([]byte("concurrency-test-secret")),
	)

	return service, sessionRecord.ID, token.ID, rawToken
}

// runConcurrently releases every participant at once and waits for all of them,
// with a deadline so a lock that never resolves fails the test instead of
// hanging it.
func runConcurrently(t *testing.T, participants int, body func(index int)) {
	t.Helper()

	start := make(chan struct{})

	var ready sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(participants)
	done.Add(participants)

	for i := range participants {
		go func() {
			defer done.Done()

			ready.Done()
			<-start

			body(i)
		}()
	}

	ready.Wait()
	close(start)

	finished := make(chan struct{})

	go func() {
		done.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(concurrencyTimeout):
		t.Fatal("contended operations did not finish within the timeout")
	}
}

// Two refreshes carrying the same token is either a replay or a client retry,
// and the implementation cannot tell them apart. It treats both as a replay.
//
// That is deliberate and it is the reason this test exists. Rotation takes a row
// lock on the token and the session, so whichever request arrives second blocks,
// and when it unblocks PostgreSQL re-evaluates its WHERE clause against the row
// version the first request committed. The token hash has not changed, so the row
// still matches and the second request reads it as already rotated. Reusing a
// rotated token revokes the whole session, so the session is revoked here too.
//
// The cost is that a legitimate client which fires two refreshes at once loses the
// session and has to log in again. That is the intended trade: the alternative is a
// replayed token that cannot be told apart from a racing retry, which would leave
// reuse detection unenforced.
func TestSession_RefreshToken_ConcurrentSameTokenRevokesSession(t *testing.T) {
	const rawToken = "concurrent-refresh-token"

	service, sessionID, tokenID, token := seedSessionWithToken(
		t,
		"concurrent-refresh@test.com",
		rawToken,
	)

	db := sessiondbtest.New(testPool)

	type result struct {
		err error
	}

	results := make([]result, 2)

	ctx, cancel := context.WithTimeout(context.Background(), concurrencyTimeout)
	defer cancel()

	runConcurrently(t, len(results), func(index int) {
		_, err := service.RefreshToken(ctx, token)
		results[index].err = err
	})

	var succeeded int
	var revoked int

	for _, outcome := range results {
		if outcome.err == nil {
			succeeded++

			continue
		}

		appErr := apperror.FromError(outcome.err)
		require.Equal(t, 401, appErr.Status)
		require.Equal(t, session.CodeSessionRevoked, appErr.Code)

		revoked++
	}

	// Exactly one rotation may have been issued. Two would mean both requests
	// extended the session from one token.
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, revoked)

	// The persisted state, which is what the next request will see.
	updatedSession, err := db.GetSessionState(context.Background(), sessionID)
	require.NoError(t, err)

	require.True(
		t,
		updatedSession.RevokedAt.Valid,
		"reuse of a rotated token must leave the session revoked",
	)

	updatedToken, err := db.GetRefreshTokenState(context.Background(), tokenID)
	require.NoError(t, err)

	require.True(t, updatedToken.ReplacedBy.Valid, "the token must be marked rotated")

	// The rotation is real, not merely reported: the old token names a successor.
	successor, err := db.GetRefreshTokenState(
		context.Background(),
		uuid.UUID(updatedToken.ReplacedBy.Bytes),
	)
	require.NoError(t, err)
	require.Equal(t, sessionID, successor.SessionID)
}

// A refresh and a revocation of the same session may be serialized either way, and
// the outcome must not depend on which arrives first.
//
// If the revocation commits first, the refresh finds the session revoked and
// issues nothing. If the refresh commits first, it issues a token and the
// revocation then applies to the same session. Either way the session ends up
// revoked, which is what makes the second case safe: a token issued moments before
// a revocation cannot be used, because the refresh path re-reads the session under
// the same lock and refuses it.
func TestSession_RefreshToken_RacingRevocationCannotExtendSession(t *testing.T) {
	const rawToken = "racing-revocation-token"

	service, sessionID, _, token := seedSessionWithToken(
		t,
		"racing-revocation@test.com",
		rawToken,
	)

	db := sessiondbtest.New(testPool)

	var refreshResult session.RefreshTokenResult
	var refreshErr error
	var logoutErr error

	ctx, cancel := context.WithTimeout(context.Background(), concurrencyTimeout)
	defer cancel()

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			refreshResult, refreshErr = service.RefreshToken(ctx, token)

			return
		}

		// The error is recorded rather than asserted here: a participant runs on
		// its own goroutine, and a failed assertion has to be made on the test's.
		logoutErr = service.Logout(ctx, sessionID)
	})

	require.NoError(t, logoutErr)

	// The refresh may have been refused, or may have completed just before the
	// revocation. Both are legal serializations, so neither is asserted as the
	// only outcome; what matters is what they left behind.
	if refreshErr != nil {
		appErr := apperror.FromError(refreshErr)
		require.Equal(t, session.CodeSessionRevoked, appErr.Code)
	}

	finalSession, err := db.GetSessionState(context.Background(), sessionID)
	require.NoError(t, err)

	require.True(
		t,
		finalSession.RevokedAt.Valid,
		"the session must be revoked once the race settles",
	)

	tokens, err := db.GetRefreshTokensForSession(context.Background(), sessionID)
	require.NoError(t, err)

	if refreshErr != nil {
		// The revocation won: nothing was issued, so the token that was already
		// there is the only one and it has not been rotated.
		require.Len(t, tokens, 1)
		require.False(t, tokens[0].ReplacedBy.Valid)

		return
	}

	// The refresh won: it issued a successor before the revocation landed. That
	// successor is the dangerous case, because it was minted by a request that
	// succeeded. It still belongs to a session that is now revoked, and the
	// refresh path re-reads the session under the lock it already holds, so it
	// cannot be used to extend the session any further.
	require.Len(t, tokens, 2)
	require.True(t, tokens[0].ReplacedBy.Valid)

	_, err = service.RefreshToken(
		context.Background(),
		refreshResult.RefreshToken,
	)

	require.Error(
		t,
		err,
		"a token issued during the race must not extend a revoked session",
	)
	require.Equal(
		t,
		session.CodeSessionRevoked,
		apperror.FromError(err).Code,
	)
}
