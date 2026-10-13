package integration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// The invariant these tests pin is a database one:
//
//	For a given email, at most one row may have status = 'pending' at a time.
//	Any number of 'completed' and 'expired' rows may coexist with it, and with
//	each other.
//
// It is enforced by one partial unique index, created by migration
// 000015_add_pending_password_resets_email_pending_idx and mirrored in
// internal/database/baseline/schema.sql:
//
//	CREATE UNIQUE INDEX pending_password_resets_email_pending_idx
//	    ON pending_password_resets (email)
//	    WHERE status = 'pending';
//
// The predicate is the whole invariant, and it is deliberately narrower than
// registration's equivalent, pending_registrations_email_idx, which covers
// 'pending' AND 'completed'. A password reset is a repeatable operation: once one
// completes, the same person must be able to start another. A registration is
// not: once one completes the address is taken and must stay taken. Widening the
// reset index to include 'completed' would lock a user out of their own account
// recovery permanently, after a single successful reset.
//
// Two guards protect different things and neither replaces the other. The
// reconciliation in createPasswordResetOnce repairs stale state — an expired
// pending row found by the locking SELECT is flipped to 'expired' so the insert
// below it can proceed. The unique index protects what reconciliation cannot see:
// two requests that both found nothing, because there was no row to lock.
//
// The concurrent HTTP-level behaviour is covered separately in
// auth_password_reset_concurrent_creation_test.go.

// pendingPasswordResetEmailPendingIndex is the partial unique index that enforces
// the invariant.
//
// It is repeated here rather than imported because it is unexported in its
// package, and because a test asserting on the constraint has to name it rather
// than derive it: the bounded retry in CreatePasswordReset fires on exactly this
// ConstraintName, so a silent rename would disarm the retry with every
// request-level test still green.
const pendingPasswordResetEmailPendingIndex = "pending_password_resets_email_pending_idx"

// resetStatusCounts is how many rows exist for one address, split by status.
//
// A single total would not do: the invariant is that "one pending" and "many
// historical" are simultaneously true, and only a per-status count can say so.
type resetStatusCounts struct {
	pending   int64
	completed int64
	expired   int64
}

// countResetStatuses reads the per-status row counts straight from the table.
//
// Inline SQL rather than the generated test target, for the same reason
// auth_register_concurrency_test.go does it: this is an assertion helper with one
// caller, not a feature's persistence surface. It is scoped by email, so a row
// left by another test could neither satisfy nor fail it.
func countResetStatuses(
	t *testing.T,
	emailAddress string,
) resetStatusCounts {
	t.Helper()

	var counts resetStatusCounts

	err := testPool.QueryRow(
		context.Background(),
		`SELECT
		    COUNT(*) FILTER (WHERE status = 'pending')::bigint,
		    COUNT(*) FILTER (WHERE status = 'completed')::bigint,
		    COUNT(*) FILTER (WHERE status = 'expired')::bigint
		 FROM pending_password_resets
		 WHERE email = $1`,
		emailAddress,
	).Scan(&counts.pending, &counts.completed, &counts.expired)
	require.NoError(t, err)

	return counts
}

// insertResetRow writes one row directly, bypassing the service.
//
// It exists so the index can be exercised without first having to win a race.
// Every caller asserts on the outcome, so an unexpected error never passes
// silently.
func insertResetRow(t *testing.T, emailAddress string, status string) error {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		`INSERT INTO pending_password_resets (email, status, expires_at)
		 VALUES ($1, $2::text, NOW() + INTERVAL '15 minutes')`,
		emailAddress,
		status,
	)

	return err
}

// The index is what refuses a second pending row, and it refuses it under a
// constraint whose name the retry keys on.
//
// Asserting the name is the point, not an implementation detail. A test that only
// asserted "some error" would keep passing if the index were renamed and the
// bounded retry silently stopped firing.
func TestPendingPasswordReset_SecondPendingRowIsRejectedByTheIndex(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "index-second-pending@example.com"

	require.NoError(t, insertResetRow(t, emailAddress, "pending"))

	err := insertResetRow(t, emailAddress, "pending")
	require.Error(t, err, "a second pending row for one address must be refused")

	var pgErr *pgconn.PgError

	require.True(
		t,
		errors.As(err, &pgErr),
		"the refusal must come from PostgreSQL as a constraint violation, got %T",
		err,
	)

	require.Equal(
		t,
		pendingPasswordResetEmailPendingIndex,
		pgErr.ConstraintName,
		"the retry in CreatePasswordReset fires on this name only",
	)
	require.Equal(t, "23505", pgErr.Code, "a unique violation")

	// The refused row did not land.
	counts := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), counts.pending)
	require.Equal(t, int64(0), counts.completed)
	require.Equal(t, int64(0), counts.expired)
}

// A completed reset must not block the next one. This is the asymmetry with
// registration, and it is why 'completed' is absent from the predicate.
//
// Asserting it against the index makes it a statement about the schema rather
// than about the service. If the predicate ever widened to include 'completed',
// this fails — while every request-level test could still pass, because the
// service would simply hand back the completed reset's id.
func TestPendingPasswordReset_CompletedRowDoesNotBlockANewPendingRow(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "index-completed-then-pending@example.com"

	require.NoError(t, insertResetRow(t, emailAddress, "pending"))

	_, err := testPool.Exec(
		ctx,
		`UPDATE pending_password_resets
		 SET status = 'completed'
		 WHERE email = $1 AND status = 'pending'`,
		emailAddress,
	)
	require.NoError(t, err)

	require.Equal(t, int64(1), countResetStatuses(t, emailAddress).completed)

	// The whole point of the predicate: this must succeed.
	require.NoError(
		t,
		insertResetRow(t, emailAddress, "pending"),
		"a completed reset must not block a new one; if it does, the index "+
			"predicate covers 'completed' and the user is locked out of recovery",
	)

	counts := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), counts.pending)
	require.Equal(t, int64(1), counts.completed)
}

// An expired row is excluded from the predicate too, and for the same reason: a
// stale reset must not hold the address.
//
// 'expired' is the state reconciliation moves rows into, so it is the state an
// address accumulates in practice.
func TestPendingPasswordReset_ExpiredRowDoesNotBlockANewPendingRow(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "index-expired-then-pending@example.com"

	require.NoError(t, insertResetRow(t, emailAddress, "pending"))

	_, err := testPool.Exec(
		ctx,
		`UPDATE pending_password_resets
		 SET status = 'expired'
		 WHERE email = $1 AND status = 'pending'`,
		emailAddress,
	)
	require.NoError(t, err)

	require.NoError(
		t,
		insertResetRow(t, emailAddress, "pending"),
		"an expired reset must not block a new one",
	)

	counts := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), counts.pending)
	require.Equal(t, int64(1), counts.expired)
}

// Historical rows and one pending row coexist, and their presence does not
// weaken the one-pending rule.
//
// The last assertion is the important one: with completed and expired rows already
// present, the index still refuses a second pending row.
func TestPendingPasswordReset_HistoryCoexistsWithExactlyOnePendingRow(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "index-history-and-pending@example.com"

	// Written without touching a pending row at all, so none of them can collide
	// — which is the property under test.
	for range 2 {
		require.NoError(t, insertResetRow(t, emailAddress, "completed"))
		require.NoError(t, insertResetRow(t, emailAddress, "expired"))
	}

	require.NoError(t, insertResetRow(t, emailAddress, "pending"))

	counts := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), counts.pending)
	require.Equal(t, int64(2), counts.completed)
	require.Equal(t, int64(2), counts.expired)

	err := insertResetRow(t, emailAddress, "pending")
	require.Error(t, err, "history must not weaken the one-pending-row rule")

	var pgErr *pgconn.PgError

	require.True(t, errors.As(err, &pgErr))
	require.Equal(
		t,
		pendingPasswordResetEmailPendingIndex,
		pgErr.ConstraintName,
	)

	require.Equal(t, int64(1), countResetStatuses(t, emailAddress).pending)
}

// The same rule through the real API: finish a reset, then start another.
//
// This is the user-visible half of the index test. The index tests say the schema
// permits it; this says the flow actually does it end to end, twice, and that the
// second password is the one left in force — so it is a repeatable operation and
// not merely a row that was allowed to exist.
func TestRequestPasswordReset_ACompletedResetDoesNotBlockTheNextOne(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const (
		emailAddress   = "repeat-reset@example.com"
		firstPassword  = "the-first-password"
		secondPassword = "the-second-password"
	)

	// Run the whole flow once, to completion. startPasswordResetWithPIN creates
	// the account, so the user id for the credential comes back with it.
	userID, firstVerificationID, issuedPIN := startPasswordResetWithPIN(
		t,
		emailAddress,
	)

	seedCredentialAndSession(t, userID, firstPassword)

	firstMinted := verifyPasswordResetPIN(firstVerificationID, issuedPIN)
	require.NoError(t, firstMinted.err)
	require.Equal(t, http.StatusOK, firstMinted.status, firstMinted.code)

	firstFinalized := postResetFinalizeFrom(
		t,
		testApp,
		firstMinted.token,
		firstPassword,
		"",
	)
	require.Equal(t, http.StatusOK, firstFinalized.status, firstFinalized.code)

	afterFirst := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), afterFirst.completed)
	require.Equal(t, int64(0), afterFirst.pending)

	// Start again. If 'completed' were covered by the index this is where a user
	// would be permanently locked out of their own recovery.
	second := postPasswordResetFrom(t, "198.51.100.64", emailAddress)
	require.Equal(t, http.StatusCreated, second.status, second.code)
	require.NotEmpty(t, second.verificationID)

	require.NotEqual(
		t,
		firstVerificationID,
		second.verificationID,
		"a new reset must not be handed the spent verification id",
	)

	// One pending and one completed exist at once, which is precisely the state the
	// partial index exists to allow.
	afterSecond := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(1), afterSecond.pending)
	require.Equal(t, int64(1), afterSecond.completed)
	require.Equal(t, int64(0), afterSecond.expired)

	// The new reset is a usable flow, not merely a row.
	secondVerificationID := uuid.MustParse(second.verificationID)
	secondPIN := issuePINWithKnownCode(t, secondVerificationID, "123456")

	secondMinted := verifyPasswordResetPIN(secondVerificationID, secondPIN)
	require.NoError(t, secondMinted.err)
	require.Equal(t, http.StatusOK, secondMinted.status, secondMinted.code)

	secondFinalized := postResetFinalizeFrom(
		t,
		testApp,
		secondMinted.token,
		secondPassword,
		"",
	)
	require.Equal(t, http.StatusOK, secondFinalized.status, secondFinalized.code)

	finalCounts := countResetStatuses(t, emailAddress)
	require.Equal(t, int64(0), finalCounts.pending)
	require.Equal(t, int64(2), finalCounts.completed)

	// Two completed resets now coexist for one address, which registration's
	// index would have refused outright.
	require.GreaterOrEqual(t, finalCounts.completed, int64(2))

	// The second password is in force, so the second reset really did replace the
	// credential rather than being a no-op that reported success.
	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)

	verified, err := testutil.NewPasswordHasher().Verify(
		ctx,
		secondPassword,
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.True(
		t,
		verified.Match,
		"the second reset must leave the new password in force",
	)

	firstStill, err := testutil.NewPasswordHasher().Verify(
		ctx,
		firstPassword,
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.False(
		t,
		firstStill.Match,
		"the replaced password must no longer verify",
	)
}

// issuePINWithKnownCode issues a code for a verification and pins its hash, so a
// caller can verify with a PIN it chose.
//
// The issued code is six random digits and the mail is the only place the
// plaintext exists; pinning is what lets the rest of a test stay deterministic.
// It uses the same secret as the integration app.
func issuePINWithKnownCode(
	t *testing.T,
	verificationID uuid.UUID,
	pin string,
) string {
	t.Helper()

	db := passwordresetdbtest.New(testPool)

	require.Equal(
		t,
		http.StatusCreated,
		resetCall(
			t,
			testApp,
			http.MethodPost,
			"/auth/password-reset/verification/"+verificationID.String()+"/pin",
			"",
			"",
		).status,
	)

	require.NoError(
		t,
		db.SetVerificationCodeHash(
			context.Background(),
			passwordresetdbtest.SetVerificationCodeHashParams{
				CodeHash: security.NewVerificationCodeHasher(
					[]byte("integration-test-verification-secret"),
				).Hash(pin),
				VerificationRequestID: verificationID,
			},
		),
	)

	return pin
}