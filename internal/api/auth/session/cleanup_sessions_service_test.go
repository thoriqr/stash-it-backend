package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
)

// These tests cover the batch loop and what the service records about it.
//
// The integration tests prove what the statement does to the database. What they
// cannot reach is the loop's own control flow — how many batches it runs, when it
// stops, and what it says when a batch fails partway through — because none of
// that is observable from the outside once a run ends. Driving the repository
// mock is the way to make the loop hand back a chosen sequence of batch results.

const cleanupBatchSize = session.SessionCleanupBatchSize

// observedLogger returns a logger and the recorded entries, so a test can assert
// on what was logged rather than only on what was returned.
//
// The failure path is the one that has to be asserted this way: it returns an
// error and no result, so the committed deletion count exists only in the log.
func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)

	return zap.New(core), logs
}

func loggedFields(logs *observer.ObservedLogs, message string) map[string]any {
	entries := logs.FilterMessage(message).All()

	if len(entries) == 0 {
		return nil
	}

	return entries[0].ContextMap()
}

// The loop must stop as soon as a batch comes back short. Running one more
// statement after the eligible set is exhausted would be a wasted round trip on
// every ordinary daily run, and it would report a batch that deleted nothing as
// though it had done work.
func TestService_CleanupSessions_StopsWhenABatchComesBackShort(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, _ := observedLogger()
	svc := session.NewService(repo, nil, log)

	// A full batch, then a short one. The full batch is what forces a second
	// pass; the short one is what ends the loop.
	gomock.InOrder(
		repo.EXPECT().
			DeleteCleanupEligibleSessions(
				gomock.Any(),
				session.SessionIdleLifetime,
				int32(cleanupBatchSize),
			).
			Return(int64(cleanupBatchSize), nil),
		repo.EXPECT().
			DeleteCleanupEligibleSessions(
				gomock.Any(),
				session.SessionIdleLifetime,
				int32(cleanupBatchSize),
			).
			Return(int64(3), nil),
	)

	result, err := svc.CleanupSessions(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(cleanupBatchSize)+3, result.DeletedSessions)
	require.Equal(t, 2, result.Batches)
	require.False(
		t,
		result.BacklogCapped,
		"stopping because there was nothing left is not the same as hitting the cap",
	)
}

// The idle window reaching the statement must be SessionIdleLifetime itself, not
// a duration the service decided on. The integration tests compare behaviour;
// this is what proves the value that produces that behaviour is the shared
// constant rather than a second copy of it.
func TestService_CleanupSessions_PassesTheSharedIdleWindow(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, _ := observedLogger()
	svc := session.NewService(repo, nil, log)

	repo.EXPECT().
		DeleteCleanupEligibleSessions(
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).
		DoAndReturn(
			func(
				_ context.Context,
				idleWindow time.Duration,
				batchLimit int32,
			) (int64, error) {
				require.Equal(
					t,
					session.SessionIdleLifetime,
					idleWindow,
					"the idle window must be the shared constant, not a copy",
				)
				require.Equal(
					t,
					int32(cleanupBatchSize),
					batchLimit,
					"the batch size must not be caller-controlled",
				)

				return int64(0), nil
			},
		)

	_, err := svc.CleanupSessions(context.Background())

	require.NoError(t, err)
}

// Hitting the cap is the one outcome where cleanup stops without having finished,
// and it is the only thing that tells an operator a larger drain is needed. It is
// not a backlog count and the log must not present it as one.
func TestService_CleanupSessions_ReportsTheBatchCapAsReached(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, _ := observedLogger()
	svc := session.NewService(repo, nil, log)

	// Every batch full, forever: the only thing that can stop this loop is the
	// cap.
	repo.EXPECT().
		DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(cleanupBatchSize), nil).
		Times(session.SessionCleanupMaxBatches)

	result, err := svc.CleanupSessions(context.Background())

	require.NoError(t, err)
	require.Equal(t, session.SessionCleanupMaxBatches, result.Batches)
	require.True(t, result.BacklogCapped)
	require.Equal(
		t,
		int64(cleanupBatchSize*session.SessionCleanupMaxBatches),
		result.DeletedSessions,
	)
}

// The failure path is where a wrong number would be a lie about durable state.
//
// Each batch is its own statement and therefore its own commit, so the first
// batch's deletions survive the second batch's failure. The log has to say 500
// committed rows, not zero: zero would describe a rollback that did not happen.
func TestService_CleanupSessions_ReportsCommittedDeletionsWhenALaterBatchFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, logs := observedLogger()
	svc := session.NewService(repo, nil, log)

	failure := errors.New("batch failed")

	gomock.InOrder(
		repo.EXPECT().
			DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(int64(cleanupBatchSize), nil),
		repo.EXPECT().
			DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(int64(0), failure),
	)

	result, err := svc.CleanupSessions(context.Background())

	require.ErrorIs(t, err, failure)
	require.Equal(
		t,
		session.CleanupSessionsResult{},
		result,
		"a failed run reports no result, so the only record of the first batch's deletions is the log",
	)

	fields := loggedFields(logs, "session cleanup failed")
	require.NotNil(t, fields, "a failure must be logged")

	require.EqualValues(
		t,
		cleanupBatchSize,
		fields["deleted_sessions"],
		"the count must be what earlier batches already committed, not zero",
	)
	require.EqualValues(
		t,
		1,
		fields["batches_completed"],
	)
	require.EqualValues(t, false, fields["backlog_capped"])
	require.NotNil(t, fields["error"])

	entries := logs.FilterMessage("session cleanup failed").All()
	require.Equal(t, zap.ErrorLevel, entries[0].Level)
}

// A run that deleted nothing is still a run, and has to leave the same trace a
// run that deleted something did. Without this, a scheduler that quietly stopped
// calling would be indistinguishable in the logs from a table that happened to be
// clean.
func TestService_CleanupSessions_LogsASuccessfulRunThatDeletedNothing(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, logs := observedLogger()
	svc := session.NewService(repo, nil, log)

	repo.EXPECT().
		DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(0), nil)

	result, err := svc.CleanupSessions(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(0), result.DeletedSessions)

	entries := logs.FilterMessage("session cleanup completed").All()
	require.Len(t, entries, 1, "a zero-deletion run is still logged")

	require.Equal(t, zap.InfoLevel, entries[0].Level)

	fields := entries[0].ContextMap()
	require.EqualValues(t, 0, fields["deleted_sessions"])
	require.EqualValues(t, 1, fields["batches"])
	require.EqualValues(t, cleanupBatchSize, fields["batch_size"])
	require.EqualValues(t, false, fields["backlog_capped"])
	require.NotNil(t, fields["duration"])
}

// The success record has to carry the same fields every time, whether the run
// deleted one row or fifty thousand, because that is what makes the log line
// something an operator can read without first finding out which kind of run
// produced it.
func TestService_CleanupSessions_SuccessLogCarriesTheBatchFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := sessionmocks.NewMockRepository(ctrl)

	log, logs := observedLogger()
	svc := session.NewService(repo, nil, log)

	repo.EXPECT().
		DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(cleanupBatchSize), nil)
	repo.EXPECT().
		DeleteCleanupEligibleSessions(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(0), nil)

	_, err := svc.CleanupSessions(context.Background())

	require.NoError(t, err)

	fields := loggedFields(logs, "session cleanup completed")
	require.NotNil(t, fields)
	require.EqualValues(t, cleanupBatchSize, fields["deleted_sessions"])
	require.EqualValues(t, 2, fields["batches"])
	require.EqualValues(t, cleanupBatchSize, fields["batch_size"])
	require.NotNil(t, fields["duration"])
	require.EqualValues(t, false, fields["backlog_capped"])
}