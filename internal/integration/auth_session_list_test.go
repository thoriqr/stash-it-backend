package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/security"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

// seedSession creates one session for a user with an explicit last_activity_at
// and returns its id.
//
// The timestamp is a parameter rather than left to NOW() on purpose. The listing
// orders by last_activity_at, and a test that asserts ordering should not depend
// on whether two statements land in the same microsecond: a tie would still
// satisfy "descending" but would make which row lands on which page arbitrary.
func seedSession(
	t *testing.T,
	db *sessiondbtest.Queries,
	userID uuid.UUID,
	lastActivityAt time.Time,
	revoked bool,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()

	revokedAt := pgtype.Timestamptz{}
	if revoked {
		revokedAt = pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		}
	}

	created, err := db.CreateTestSession(ctx, sessiondbtest.CreateTestSessionParams{
		UserID:   userID,
		Platform: "web",
		AbsoluteExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
		RevokedAt: revokedAt,
	})
	require.NoError(t, err)

	require.NoError(
		t,
		db.UpdateTestSessionLastActivity(
			ctx,
			sessiondbtest.UpdateTestSessionLastActivityParams{
				LastActivityAt: pgtype.Timestamptz{
					Time:  lastActivityAt,
					Valid: true,
				},
				ID: created.ID,
			},
		),
	)

	return created.ID
}

// sessionIDSet is the set of ids on a page, so a test can compare membership
// without caring what order the rows arrived in.
func sessionIDSet(sessions []sessiondb.Session) map[uuid.UUID]bool {
	ids := make(map[uuid.UUID]bool, len(sessions))

	for _, s := range sessions {
		ids[s.ID] = true
	}

	return ids
}

// The listing is owner-scoped and filters revoked sessions in SQL. Neither is
// observable through a mock, because both are the query: a missing user_id
// predicate or a dropped revoked_at IS NULL would still satisfy every service
// test and would only show up as another user's device in a response.
//
// Two users, because a filter that only excludes revoked rows would pass with one.
func TestSession_ListSessions_ReturnsOnlyCallersActiveSessions(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	ownerID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "list-owner@test.com",
		DisplayName: "List Owner",
	})
	require.NoError(t, err)

	otherID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "list-other@test.com",
		DisplayName: "List Other",
	})
	require.NoError(t, err)

	now := time.Now()

	// Three active, oldest first, so the expected page order is known.
	oldest := seedSession(t, db, ownerID, now.Add(-3*time.Hour), false)
	middle := seedSession(t, db, ownerID, now.Add(-2*time.Hour), false)
	newest := seedSession(t, db, ownerID, now.Add(-1*time.Hour), false)

	// Revoked, and deliberately the two most recent, so a listing that ignored
	// revoked_at would put them at the head of page one rather than merely
	// inflating the total.
	seedSession(t, db, ownerID, now.Add(-30*time.Minute), true)
	seedSession(t, db, ownerID, now.Add(-10*time.Minute), true)

	// Somebody else's active session, and the most recent row in the table. Only
	// the user_id predicate keeps this out.
	otherSession := seedSession(t, db, otherID, now, false)

	service := session.NewService(
		session.NewRepository(sessiondb.New(testPool), testPool),
		security.NewAccessTokenGenerator([]byte("list-test-secret")),
	)

	t.Run("returns only the caller's unrevoked sessions", func(t *testing.T) {
		result, err := service.ListSessions(ctx, ownerID, 1, 20)
		require.NoError(t, err)

		require.Len(t, result.Sessions, 3)
		require.Equal(t, int64(3), result.Total)
		require.Equal(t, 1, result.TotalPages)

		ids := sessionIDSet(result.Sessions)

		require.True(t, ids[oldest])
		require.True(t, ids[middle])
		require.True(t, ids[newest])
		require.False(
			t,
			ids[otherSession],
			"another user's session must not be listed",
		)

		// Most recent first, which is the contract the query states.
		require.Equal(t, newest, result.Sessions[0].ID)
		require.Equal(t, middle, result.Sessions[1].ID)
		require.Equal(t, oldest, result.Sessions[2].ID)
	})

	t.Run("another user sees only their own", func(t *testing.T) {
		result, err := service.ListSessions(ctx, otherID, 1, 20)
		require.NoError(t, err)

		require.Len(t, result.Sessions, 1)
		require.Equal(t, int64(1), result.Total)
		require.Equal(t, otherSession, result.Sessions[0].ID)
	})

	t.Run("a user with no sessions gets an empty page, not an error", func(t *testing.T) {
		emptyID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
			Email:       "list-empty@test.com",
			DisplayName: "List Empty",
		})
		require.NoError(t, err)

		result, err := service.ListSessions(ctx, emptyID, 1, 20)
		require.NoError(t, err)

		require.Empty(t, result.Sessions)
		require.Equal(t, int64(0), result.Total)
		require.Equal(t, 0, result.TotalPages)
	})

	// Two pages of two over three rows. The page boundary has to partition the
	// set: every active session on exactly one page, none on both.
	t.Run("pagination covers every active session exactly once", func(t *testing.T) {
		first, err := service.ListSessions(ctx, ownerID, 1, 2)
		require.NoError(t, err)

		require.Len(t, first.Sessions, 2)
		require.Equal(t, int64(3), first.Total)
		require.Equal(t, 2, first.TotalPages)

		second, err := service.ListSessions(ctx, ownerID, 2, 2)
		require.NoError(t, err)

		require.Len(t, second.Sessions, 1)
		require.Equal(t, int64(3), second.Total)
		require.Equal(t, 2, second.TotalPages)

		seen := sessionIDSet(first.Sessions)

		for _, s := range second.Sessions {
			require.False(
				t,
				seen[s.ID],
				"a session must not appear on two pages",
			)
			seen[s.ID] = true
		}

		require.Len(t, seen, 3)
		require.True(t, seen[oldest])
		require.True(t, seen[middle])
		require.True(t, seen[newest])
	})

	// Past the end is an empty page rather than an error, so a client that
	// walks pages does not have to treat the end as a failure.
	t.Run("a page past the end is empty", func(t *testing.T) {
		result, err := service.ListSessions(ctx, ownerID, 5, 2)
		require.NoError(t, err)

		require.Empty(t, result.Sessions)
	})
}

// compareUUIDs orders ids the way PostgreSQL orders a uuid column, by byte value.
//
// uuid.UUID is a [16]byte underneath, so a byte comparison is the same ordering
// the database applies rather than an approximation of it. Building the expected
// page order with this keeps the test about the query's ORDER BY: it never has to
// assume anything about which row was inserted first.
func compareUUIDs(a, b uuid.UUID) int {
	return bytes.Compare(a[:], b[:])
}

// Ordering is only reproducible if every row has a distinct position, which is
// what the id DESC tie-breaker is for. The previous ordering was by
// last_activity_at alone, and that column is not unique: it defaults to NOW(),
// which is constant within a transaction, and refresh writes NOW() to it too. A
// tied group was therefore free to come back in any order.
//
// The fixture below is deliberately large. That is not decoration: with only a
// handful of tied rows, PostgreSQL satisfies a small LIMIT from
// sessions_user_active_idx, and that index walk happens to emit the tied rows in
// descending id order — so a small fixture passes with or without the
// tie-breaker and proves nothing. The group here is big enough that the planner
// has to sort, and a sort over ties is arbitrary. Measured against the query
// without the tie-breaker, limits at or below 9 agree with id DESC by accident
// and limits from 10 upward do not.
//
// That also means the assertion is one-directional. If a future plan ever
// satisfies a given limit from the index again, this test stops detecting the
// regression; it will not start failing on a correct query.
func TestSession_ListSessions_OrdersTiedSessionsDeterministically(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "list-tie@test.com",
		DisplayName: "List Tie",
	})
	require.NoError(t, err)

	tied := time.Now().Add(-time.Hour)

	// The newest row, created before everything else. sessions.id is uuidv7(), so
	// this session has the lowest id in the table — the property the collection
	// listing's tie-breaker already depends on. It still has to sort first,
	// because last_activity_at outranks id and id only breaks ties.
	newest := seedSession(t, db, userID, tied.Add(time.Hour), false)

	// The tied group. Every row below carries one identical last_activity_at, so
	// nothing but id can decide their order.
	const tiedCount = 200

	tiedIDs := make([]uuid.UUID, 0, tiedCount)

	for range tiedCount {
		tiedIDs = append(tiedIDs, seedSession(t, db, userID, tied, false))
	}

	// Created last, so it has the highest id, and given the oldest timestamp. It
	// has to sort last for the same reason newest sorts first.
	oldest := seedSession(t, db, userID, tied.Add(-time.Hour), false)

	// The fixture has to produce a genuine tie. Timestamps round-trip through a
	// microsecond column, so this compares the rows against each other rather
	// than against the value the fixture passed in.
	first, err := db.GetSessionState(ctx, tiedIDs[0])
	require.NoError(t, err)

	for _, id := range tiedIDs[1:] {
		stored, err := db.GetSessionState(ctx, id)
		require.NoError(t, err)

		require.True(
			t,
			stored.LastActivityAt.Time.Equal(first.LastActivityAt.Time),
			"the fixture must produce a real tie for id to decide",
		)
	}

	service := session.NewService(
		session.NewRepository(sessiondb.New(testPool), testPool),
		security.NewAccessTokenGenerator([]byte("pagination-test-secret")),
	)

	expectedTied := slices.Clone(tiedIDs)
	slices.SortFunc(expectedTied, compareUUIDs)
	slices.Reverse(expectedTied)

	expected := make([]uuid.UUID, 0, len(expectedTied)+2)
	expected = append(expected, newest)
	expected = append(expected, expectedTied...)
	expected = append(expected, oldest)

	total := int64(len(expected))

	// Several page sizes, because a tie only shows up where the planner sorts.
	// The last two are the ones that break the un-tie-broken query; the small ones
	// are kept so a change that made small limits sort would be caught too.
	for _, limit := range []int{5, 10, 25, 50} {
		pageCount := (len(expected) + limit - 1) / limit

		paged := make([]uuid.UUID, 0, len(expected))

		for page := 1; page <= pageCount; page++ {
			result, err := service.ListSessions(ctx, userID, page, limit)
			require.NoError(t, err)

			require.LessOrEqual(
				t,
				len(result.Sessions),
				limit,
				"limit %d page %d returned more rows than was asked for",
				limit,
				page,
			)
			require.Equal(
				t,
				total,
				result.Total,
				"limit %d: the total must not drift while paging an unchanged dataset",
				limit,
			)

			for _, s := range result.Sessions {
				paged = append(paged, s.ID)
			}
		}

		require.Equal(
			t,
			expected,
			paged,
			"limit %d: consecutive pages must concatenate into "+
				"last_activity_at DESC, id DESC",
			limit,
		)

		// Duplicates and omissions are what a broken tie actually caused, so they
		// are asserted on their own rather than left to the ordering comparison
		// above — a failure should say which of the two broke.
		seen := make(map[uuid.UUID]bool, len(paged))

		for _, id := range paged {
			require.False(
				t,
				seen[id],
				"limit %d: session %s appeared on two pages",
				limit,
				id,
			)
			seen[id] = true
		}

		for _, id := range expected {
			require.True(
				t,
				seen[id],
				"limit %d: session %s was skipped",
				limit,
				id,
			)
		}

		require.Len(t, seen, len(expected))
	}

	// A page holding everything the service will return has to agree with the
	// pages. If the two disagree, no amount of paging can be relied on.
	all, err := service.ListSessions(ctx, userID, 1, session.SessionListMaxLimit)
	require.NoError(t, err)

	require.Len(t, all.Sessions, session.SessionListMaxLimit)

	allIDs := make([]uuid.UUID, 0, len(all.Sessions))

	for _, s := range all.Sessions {
		allIDs = append(allIDs, s.ID)
	}

	require.Equal(t, expected[:session.SessionListMaxLimit], allIDs)
}

// The same filter, read through the endpoint, because the handler builds the
// response from what the query returned and the caller-scoped ownership claim is
// made there too.
func TestSession_ListSessions_OverHTTPReturnsOnlyCallersActiveSessions(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	callerID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "list-http-caller@test.com",
		DisplayName: "List HTTP Caller",
	})
	require.NoError(t, err)

	otherID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       "list-http-other@test.com",
		DisplayName: "List HTTP Other",
	})
	require.NoError(t, err)

	now := time.Now()

	current := seedSession(t, db, callerID, now.Add(-1*time.Hour), false)
	other := seedSession(t, db, callerID, now.Add(-2*time.Hour), false)
	seedSession(t, db, callerID, now, true)
	otherUserSession := seedSession(t, db, otherID, now.Add(-1*time.Minute), false)

	token := newTestSessionToken(t, callerID, current)

	resp := listSessionsOverHTTP(t, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var payload struct {
		Data struct {
			Sessions []struct {
				ID         uuid.UUID `json:"id"`
				IsCurrent  bool      `json:"is_current"`
				Platform   string    `json:"platform"`
				LastActive time.Time `json:"last_activity_at"`
			} `json:"sessions"`
		} `json:"data"`
		Meta struct {
			Pagination struct {
				Page       int   `json:"page"`
				Limit      int   `json:"limit"`
				Total      int64 `json:"total"`
				TotalPages int   `json:"total_pages"`
			} `json:"pagination"`
		} `json:"meta"`
	}

	require.NoError(t, json.Unmarshal(body, &payload))

	require.Len(t, payload.Data.Sessions, 2)

	ids := make(map[uuid.UUID]bool, len(payload.Data.Sessions))

	for _, s := range payload.Data.Sessions {
		ids[s.ID] = true
	}

	require.True(t, ids[current])
	require.True(t, ids[other])
	require.False(
		t,
		ids[otherUserSession],
		"another user's session must not be listed",
	)

	// The revoked one is seeded but never named here, so its absence is what the
	// length above already proved: three sessions were created for this user and
	// two came back.
	for _, s := range payload.Data.Sessions {
		require.False(
			t,
			s.LastActive.After(now),
			"a revoked session is the most recent row and must not be listed",
		)
	}

	// is_current is the caller's own session and no other. It is derived from the
	// token's claim compared against the returned rows, so a leak would show up
	// as two rows claiming to be current or none.
	currentCount := 0

	for _, s := range payload.Data.Sessions {
		if s.IsCurrent {
			currentCount++
			require.Equal(t, current, s.ID)
		}
	}

	require.Equal(
		t,
		1,
		currentCount,
		"exactly the caller's own session is current",
	)

	require.Equal(t, 1, payload.Meta.Pagination.Page)
	require.Equal(t, session.SessionListDefaultLimit, payload.Meta.Pagination.Limit)
	require.Equal(t, int64(2), payload.Meta.Pagination.Total)
	require.Equal(t, 1, payload.Meta.Pagination.TotalPages)
}

func listSessionsOverHTTP(t *testing.T, token string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/auth/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}
