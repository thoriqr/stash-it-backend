package integration_test

import (
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

// These tests drive POST /internal/maintenance/sessions/cleanup against real
// PostgreSQL.
//
// The things they are about cannot be proven with a mocked repository. Whether a
// revoked session is actually removed, whether the refresh_tokens cascade takes
// the whole replacement chain with it, whether an idle-expired session is
// distinguished from one merely old — all of those are statements the database
// makes, and a mock would only prove that the application asked.

const (
	cleanupPath       = "/internal/maintenance/sessions/cleanup"
	cleanupSecretName = "X-Cleanup-Secret"
)

// cleanupResponse mirrors what the endpoint promises: a deletion count and
// nothing else. Batch detail is deliberately absent from the response, so a test
// that wants to know how many batches ran has to infer it from the total.
type cleanupResponse struct {
	Data struct {
		DeletedSessions int64 `json:"deleted_sessions"`
	} `json:"data"`
	Message string `json:"message"`
}

type cleanupErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// cleanupRequest calls the cleanup route on the given app.
//
// secret is a pointer so the three cases a caller can be in stay distinguishable:
// nil sends no header at all, a pointer to "" sends an empty one. Collapsing them
// into a plain string would make "no header" and "empty header" the same test,
// which is exactly the distinction the guard refuses identically but for
// different reasons.
func cleanupRequest(
	t *testing.T,
	app *fiber.App,
	secret *string,
) (int, []byte) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, cleanupPath, nil)

	if secret != nil {
		req.Header.Set(cleanupSecretName, *secret)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, raw
}

// runCleanup calls the cleanup route with the correct secret and requires it to
// succeed, returning the number of sessions it reported deleting.
func runCleanup(t *testing.T) int64 {
	t.Helper()

	status, raw := cleanupRequest(
		t,
		testApp,
		secretPtr(testutil.TestSessionCleanupSecret),
	)
	require.Equal(t, http.StatusOK, status, string(raw))

	var body cleanupResponse
	require.NoError(t, json.Unmarshal(raw, &body))

	return body.Data.DeletedSessions
}

func secretPtr(secret string) *string {
	return &secret
}

// seedCleanupSession creates one session with the absolute expiry and revocation
// state the caller names, and returns its id.
func seedCleanupSession(
	t *testing.T,
	db *sessiondbtest.Queries,
	userID uuid.UUID,
	absoluteExpiresAt time.Time,
	revoked bool,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()

	revokedAt := pgtype.Timestamptz{}
	if revoked {
		revokedAt = pgtype.Timestamptz{
			Time:  time.Now().Add(-time.Hour),
			Valid: true,
		}
	}

	record, err := db.CreateTestSession(
		ctx,
		sessiondbtest.CreateTestSessionParams{
			UserID:   userID,
			Platform: "web",
			AbsoluteExpiresAt: pgtype.Timestamptz{
				Time:  absoluteExpiresAt,
				Valid: true,
			},
			RevokedAt: revokedAt,
		},
	)
	require.NoError(t, err)

	return record.ID
}

// ageSessionBy moves a session's last_activity_at into the past, which is how a
// test produces an idle-expired session without waiting thirty days.
func ageSessionBy(
	t *testing.T,
	db *sessiondbtest.Queries,
	sessionID uuid.UUID,
	ago time.Duration,
) {
	t.Helper()

	err := db.UpdateTestSessionLastActivity(
		context.Background(),
		sessiondbtest.UpdateTestSessionLastActivityParams{
			ID:              sessionID,
			LastActivityAt:  pgtype.Timestamptz{Time: time.Now().Add(-ago), Valid: true},
		},
	)
	require.NoError(t, err)
}

func seedCleanupToken(
	t *testing.T,
	db *sessiondbtest.Queries,
	sessionID uuid.UUID,
	replacedBy pgtype.UUID,
) uuid.UUID {
	t.Helper()

	record, err := db.CreateTestRefreshToken(
		context.Background(),
		sessiondbtest.CreateTestRefreshTokenParams{
			SessionID:  sessionID,
			TokenHash:  "cleanup-token-" + uuid.NewString(),
			ReplacedBy: replacedBy,
		},
	)
	require.NoError(t, err)

	return record.ID
}

func requireSessionAbsent(t *testing.T, db *sessiondbtest.Queries, sessionID uuid.UUID) {
	t.Helper()

	_, err := db.GetSessionState(context.Background(), sessionID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the session should have been deleted")
}

func requireSessionPresent(t *testing.T, db *sessiondbtest.Queries, sessionID uuid.UUID) {
	t.Helper()

	_, err := db.GetSessionState(context.Background(), sessionID)
	require.NoError(t, err, "the session should not have been deleted")
}

// An active session is the case where being wrong is most expensive. Every other
// test in this file proves that cleanup removes something; this one proves it
// leaves alone the thing it must never remove.
//
// The session is deliberately ordinary on every axis the predicate looks at: not
// revoked, absolute expiry in the future, last activity recent. If cleanup
// returned 0 here it would be right by accident rather than by rule.
func TestSessionCleanup_ActiveSessionAndItsTokensSurvive(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-active@test.com",
		DisplayName: "Cleanup Active",
	})
	require.NoError(t, err)

	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)

	tokenID := seedCleanupToken(t, db, sessionID, pgtype.UUID{})

	require.Equal(t, int64(0), runCleanup(t))

	requireSessionPresent(t, db, sessionID)

	tokens, err := db.GetRefreshTokensForSession(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, tokens, 1, "an active session keeps its refresh token")
	assert.Equal(t, tokenID, tokens[0].ID)
}

func TestSessionCleanup_RemovesRevokedSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-revoked@test.com",
		DisplayName: "Cleanup Revoked",
	})
	require.NoError(t, err)

	// Revoked, but otherwise entirely healthy: not expired, recently active. A
	// predicate that only looked at the time columns would miss it.
	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		true,
	)

	require.Equal(t, int64(1), runCleanup(t))

	requireSessionAbsent(t, db, sessionID)
}

func TestSessionCleanup_RemovesAbsoluteExpiredSession(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-absolute@test.com",
		DisplayName: "Cleanup Absolute",
	})
	require.NoError(t, err)

	// Expired in absolute terms while still being used: last activity is recent,
	// so the idle window has nothing to say about this session. Only
	// absolute_expires_at can catch it.
	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(-time.Hour),
		false,
	)

	require.Equal(t, int64(1), runCleanup(t))

	requireSessionAbsent(t, db, sessionID)
}

// The idle window is the one condition with a boundary, and getting it wrong in
// either direction is a real defect: too tight and a session that refresh would
// still honour disappears, too loose and dead sessions accumulate forever.
//
// So both sides are asserted. The elapsed session is well past the window, so
// nothing about timing decides it. The other is inside the window and must
// survive, which is what proves the comparison is not simply "old".
func TestSessionCleanup_RemovesIdleExpiredButKeepsOneInsideTheWindow(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-idle@test.com",
		DisplayName: "Cleanup Idle",
	})
	require.NoError(t, err)

	expiredID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)
	ageSessionBy(t, db, expiredID, session.SessionIdleLifetime+24*time.Hour)

	// Inside the window, but not by a hair: the window is thirty days and this
	// session was last active five days ago.
	activeID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)
	ageSessionBy(t, db, activeID, 5*24*time.Hour)

	require.Equal(t, int64(1), runCleanup(t))

	requireSessionAbsent(t, db, expiredID)
	requireSessionPresent(t, db, activeID)
}

// The cascade is the reason cleanup deletes the parent row and nothing else.
//
// replaced_by is ON DELETE SET NULL, so removing one token out of a chain would
// leave its predecessor looking unrotated — indistinguishable from a token that
// was never spent, which would quietly disable reuse detection for that chain.
// Removing the session takes the whole chain in one statement and cannot leave
// that state behind.
//
// The chain is built forwards because CreateTestRefreshToken returns the id it
// created: the successor has to exist before its predecessor can point at it.
func TestSessionCleanup_CascadesThroughTheWholeRefreshTokenChain(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-chain@test.com",
		DisplayName: "Cleanup Chain",
	})
	require.NoError(t, err)

	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)
	ageSessionBy(t, db, sessionID, session.SessionIdleLifetime+24*time.Hour)

	// Three rotations: token3 is the live one, token2 points at it, token1 at
	// token2. Four links would be more realistic; three is enough to make a
	// middle-of-chain token that must be deleted without orphaning anything.
	token3 := seedCleanupToken(t, db, sessionID, pgtype.UUID{})
	token2 := seedCleanupToken(t, db, sessionID, pgtype.UUID{
		Bytes: token3,
		Valid: true,
	})
	token1 := seedCleanupToken(t, db, sessionID, pgtype.UUID{
		Bytes: token2,
		Valid: true,
	})

	// The chain is real before cleanup runs, or this test would prove nothing
	// about the cascade.
	before, err := db.GetRefreshTokensForSession(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, before, 3)
	require.NotNil(t, token1)
	require.NotNil(t, token2)
	require.NotNil(t, token3)

	totalTokensBefore, err := db.CountAllRefreshTokens(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), totalTokensBefore)

	require.Equal(t, int64(1), runCleanup(t))

	requireSessionAbsent(t, db, sessionID)

	after, err := db.GetRefreshTokensForSession(ctx, sessionID)
	require.NoError(t, err)
	require.Empty(t, after, "every token in the chain must go with the session")

	totalTokensAfter, err := db.CountAllRefreshTokens(ctx)
	require.NoError(t, err)
	require.Equal(
		t,
		int64(0),
		totalTokensAfter,
		"the cascade must remove the tokens and nothing may be left pointing at a row that is gone",
	)
}

// Every way of failing the guard has to look the same from outside.
//
// This is the property that stops the route being a probe: an unauthenticated
// caller must not be able to learn whether a deployment configured a secret, and
// must not get a different answer for "no secret here" than for "wrong secret".
// Each case is also asserted to leave the database untouched, because a guard that
// refuses after doing work would be refusing too late.
//
// Note what is deliberately absent: a case for the correct secret with trailing
// whitespace. HTTP strips optional whitespace from field values before an
// application sees one, so over the wire that request carries the secret exactly,
// and a guard that refused it would be refusing the identical request. That is the
// protocol's behaviour, not this route's, and it cannot be asserted here.
func TestSessionCleanup_RejectsEveryInvalidSecretWithoutMutatingAnything(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-guard@test.com",
		DisplayName: "Cleanup Guard",
	})
	require.NoError(t, err)

	// A session that would be deleted if any of these requests reached the
	// service, so an unasserted side effect cannot hide.
	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		true,
	)
	seedCleanupToken(t, db, sessionID, pgtype.UUID{})

	unconfiguredApp, _ := testutil.NewAppWithoutSessionCleanupSecret(testPool)

	cases := []struct {
		name string
		app  *fiber.App
		// sent is nil when no header is attached at all, which is different from
		// attaching an empty one.
		sent *string
	}{
		{
			name: "no header at all",
			app:  testApp,
			sent: nil,
		},
		{
			name: "empty header",
			app:  testApp,
			sent: secretPtr(""),
		},
		{
			name: "incorrect secret",
			app:  testApp,
			sent: secretPtr("not-the-secret"),
		},
		{
			name: "a prefix of the real secret",
			app:  testApp,
			sent: secretPtr(
				testutil.TestSessionCleanupSecret[:8],
			),
		},
		{
			// Case matters. The comparison is a byte comparison and the secret is
			// used as given; normalising case here would widen what an
			// unauthenticated caller can guess at no cost to them.
			name: "the real secret, uppercased",
			app:  testApp,
			sent: secretPtr(strings.ToUpper(
				testutil.TestSessionCleanupSecret,
			)),
		},
		{
			name: "no secret configured, correct value sent anyway",
			app:  unconfiguredApp,
			sent: secretPtr(testutil.TestSessionCleanupSecret),
		},
		{
			name: "no secret configured, nothing sent",
			app:  unconfiguredApp,
			sent: nil,
		},
	}

	var (
		firstCode    string
		firstMessage string
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := cleanupRequest(t, tc.app, tc.sent)

			require.Equal(
				t,
				http.StatusUnauthorized,
				status,
				"every unusable secret is refused the same way: %s",
				string(raw),
			)

			var body cleanupErrorResponse
			require.NoError(t, json.Unmarshal(raw, &body))

			require.Equal(
				t,
				session.CodeCleanupSecretInvalid,
				body.Error.Code,
			)

			// The message must not say which of the four ways it failed.
			require.NotContains(t, body.Error.Message, "configured")
			require.NotContains(t, body.Error.Message, "expected")
			require.NotContains(
				t,
				string(raw),
				testutil.TestSessionCleanupSecret,
				"the response must never echo the secret",
			)

			if firstCode == "" {
				firstCode = body.Error.Code
				firstMessage = body.Error.Message

				return
			}

			require.Equal(
				t,
				firstCode,
				body.Error.Code,
				"a caller must not be able to tell these cases apart",
			)
			require.Equal(t, firstMessage, body.Error.Message)
		})
	}

	// Refused before the work, not after it.
	requireSessionPresent(t, db, sessionID)

	tokens, err := db.GetRefreshTokensForSession(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, tokens, 1, "a refused request must not touch the session's tokens")
}

// A run with nothing to delete is a real outcome, not a failure and not a
// silent one. The service logs it; what the caller sees is a success carrying a
// zero.
func TestSessionCleanup_SucceedsWhenThereIsNothingToDelete(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-empty@test.com",
		DisplayName: "Cleanup Empty",
	})
	require.NoError(t, err)

	sessionID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)

	require.Equal(t, int64(0), runCleanup(t))
	requireSessionPresent(t, db, sessionID)

	// And again, now with a live session present, to be sure the second run is
	// not simply the first run's answer repeated.
	require.Equal(t, int64(0), runCleanup(t))
	requireSessionPresent(t, db, sessionID)
}

// The scheduler calls this once a day and nothing stops it calling twice, so the
// second run has to find nothing and leave everything else exactly where it was.
//
// The live session is the assertion that matters. A second run that re-deleted
// something, or one that consumed the live session as collateral, would still
// report a plausible count.
func TestSessionCleanup_RepeatedExecutionIsSafe(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-repeat@test.com",
		DisplayName: "Cleanup Repeat",
	})
	require.NoError(t, err)

	deadID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		true,
	)
	seedCleanupToken(t, db, deadID, pgtype.UUID{})

	liveID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)
	seedCleanupToken(t, db, liveID, pgtype.UUID{})

	require.Equal(t, int64(1), runCleanup(t))
	require.Equal(t, int64(0), runCleanup(t))
	require.Equal(t, int64(0), runCleanup(t))

	requireSessionAbsent(t, db, deadID)
	requireSessionPresent(t, db, liveID)

	liveTokens, err := db.GetRefreshTokensForSession(ctx, liveID)
	require.NoError(t, err)
	require.Len(t, liveTokens, 1, "repeating cleanup must not touch a live session")
}

// The batch boundary is the thing that proves the loop exists.
//
// SessionCleanupBatchSize rows is the most a single statement may delete, so a
// total above it cannot come from one batch. The count is therefore the
// assertion: a run that deleted all of them ran more than one statement, and one
// that deleted the live session as well deleted something it must not.
func TestSessionCleanup_DeletesAcrossMoreThanOneBatch(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "cleanup-multibatch@test.com",
		DisplayName: "Cleanup MultiBatch",
	})
	require.NoError(t, err)

	// One more than a single batch can hold, so the first batch comes back full
	// and the loop has to run again rather than stopping after one pass.
	seeded := session.SessionCleanupBatchSize + 1

	err = db.SeedTestSessionsBulk(
		ctx,
		sessiondbtest.SeedTestSessionsBulkParams{
			UserID: userID,
			AbsoluteIn: pgtype.Interval{
				Microseconds: (24 * time.Hour).Microseconds(),
				Valid:        true,
			},
			IdleAgo: pgtype.Interval{
				Microseconds: (session.SessionIdleLifetime + 24*time.Hour).Microseconds(),
				Valid:        true,
			},
			SessionCount: int64(seeded),
		},
	)
	require.NoError(t, err)

	// A live session in the same table, so the total below is unambiguous: it is
	// the dead rows and nothing else.
	liveID := seedCleanupSession(
		t,
		db,
		userID,
		time.Now().Add(24*time.Hour),
		false,
	)

	require.Equal(t, int64(seeded), runCleanup(t))

	requireSessionPresent(t, db, liveID)

	remaining, err := db.CountAllSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(
		t,
		int64(1),
		remaining,
		"exactly the live session should be left",
	)

	// And the run is finished, not merely part-way: a second call finds nothing.
	require.Equal(t, int64(0), runCleanup(t))
}