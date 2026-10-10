package login_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	loginmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/login/mocks"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The failure budget guarding manual login.
//
// Every assertion here is about accounting, so the limiter under test is the
// recording one. What that cannot prove is the real limiter's atomicity, which is
// a property of the Lua script rather than of any caller; that is pinned against
// a real Redis in internal/integration.

const wrongPassword = "definitely-not-the-password"

// expectLoginableUser arranges for `email` to be a real account with a credential
// derived from `password`, so a login either fails on the password or succeeds
// and writes a session.
//
// Both expectations are unbounded because the tests that use this care about what
// is spent, not how many times a request arrives.
func expectLoginableUser(
	t *testing.T,
	repository *loginmocks.MockRepository,
	sessionCreator *sessionmocks.MockSessionCreator,
	passwordHasher *security.PasswordHasher,
	email string,
	password string,
) {
	t.Helper()

	passwordHash, err := passwordHasher.Hash(context.Background(), password)
	require.NoError(t, err)

	userID := uuid.New()

	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{
			ID:           userID,
			Email:        email,
			DisplayName:  "Alice",
			PasswordHash: passwordHash,
		}, nil).
		AnyTimes()

	sessionCreator.EXPECT().
		CreateSession(gomock.Any(), userID, gomock.Any()).
		Return(session.CreateSessionResult{
			Session: sessiondb.Session{
				ID:     uuid.New(),
				UserID: userID,
			},
			RefreshToken: "refresh-token",
		}, nil).
		AnyTimes()
}

// A failure spends the address's budget, in the address's canonical form, under
// the policy the feature declares. Nothing here would hold if the budget were
// keyed on the raw submitted string, because then two spellings of one address
// would be given two budgets.
func TestService_LoginManual_FailureBudgetIsKeyedByTheNormalizedAddress(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	service, repository, sessionCreator, _, _, passwordHasher :=
		newTestServiceWithLimiter(t, limiter)

	const email = "alice@example.com"

	expectLoginableUser(
		t,
		repository,
		sessionCreator,
		passwordHasher,
		email,
		"correct-password",
	)

	_, err := service.LoginManual(
		context.Background(),
		"  Alice@Example.COM  ",
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)
	require.Equal(t, login.CodeInvalidCredentials, apperror.FromError(err).Code)

	require.Equal(
		t,
		[]string{email},
		limiter.SubjectsIn(testutil.LoginEmailNamespace),
		"the budget must be the canonical address, not the submitted spelling",
	)

	require.Equal(
		t,
		[]ratelimit.Policy{{
			Max:    login.LoginEmailFailureLimit,
			Window: login.LoginEmailFailureWindow,
		}},
		limiter.Policies,
	)

	// The per-address budget is spent in front of the handler, not here. Charging
	// both from the service would let a request that never reached this code —
	// one that failed body binding, say — cost nothing.
	require.Equal(
		t,
		[]string{testutil.LoginEmailNamespace},
		limiter.Namespaces,
	)
}

// unknownEmail is an address held by no account, and is what the enumeration
// mitigation below is measured against.
const unknownEmail = "nobody@example.com"

// A wrong password is the occurrence the budget exists to count, so its charge
// stays, and the caller still gets the generic response.
func TestService_LoginManual_AWrongPasswordKeepsItsCharge(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	service, repository, sessionCreator, _, _, passwordHasher :=
		newTestServiceWithLimiter(t, limiter)

	const email = "alice@example.com"

	expectLoginableUser(
		t,
		repository,
		sessionCreator,
		passwordHasher,
		email,
		"correct-password",
	)

	_, err := service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)
	require.Equal(t, http.StatusUnauthorized, appErr.Status)
	require.Equal(t, login.CodeInvalidCredentials, appErr.Code)

	require.Zero(
		t,
		limiter.ReleasesIn(testutil.LoginEmailNamespace),
		"a failed authentication is exactly what the budget counts",
	)
	require.Equal(t, int64(1), limiter.ChargeFor(testutil.LoginEmailNamespace))
}

// A successful login hands back its own charge and nothing else. Without the
// release, every login would spend the failure budget of the address it
// authenticated, and a legitimate user who signed in a few times would then be
// unable to sign in at all.
func TestService_LoginManual_ASuccessfulLoginReleasesOnlyItsOwnCharge(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	service, repository, sessionCreator, _, _, passwordHasher :=
		newTestServiceWithLimiter(t, limiter)

	const email = "alice@example.com"
	const password = "correct-password"

	expectLoginableUser(t, repository, sessionCreator, passwordHasher, email, password)

	_, err := service.LoginManual(
		context.Background(),
		email,
		password,
		session.SessionMetadata{},
	)

	require.NoError(t, err)

	require.Equal(t, 1, limiter.ReleasesIn(testutil.LoginEmailNamespace))
	require.Equal(
		t,
		int64(0),
		limiter.ChargeFor(testutil.LoginEmailNamespace),
		"a successful login must leave the budget where it started",
	)

	// The address has its whole allowance again, so a single further failure
	// costs exactly one of it rather than two.
	_, err = service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)
	require.Equal(t, int64(1), limiter.ChargeFor(testutil.LoginEmailNamespace))
}

// An address with no account and an account with no password credential are the
// same failed authentication as far as this budget is concerned, and the
// repository already reports them identically because the lookup joins the
// credential table. Neither may give a charge back, or an attacker could spend
// someone's budget at leisure by probing addresses that do not exist.
func TestService_LoginManual_AnAccountThatCannotBeAuthenticatedKeepsItsCharge(t *testing.T) {
	// What the INNER JOIN on the credential table reports for a Google-only
	// account, and what it reports for an address nobody holds: the same thing.
	lookupErr := apperror.UnauthorizedWith(
		login.CodeInvalidCredentials,
		"invalid email or password",
		nil,
	)

	limiter := testutil.NewCountingPinRateLimiter()

	service, repository, _, _, _, _ := newTestServiceWithLimiter(t, limiter)

	const email = "alice@example.com"

	// No session expectation: nothing is authenticated, and gomock fails the test
	// if one is created.
	repository.EXPECT().
		GetUserForLogin(gomock.Any(), email).
		Return(logindb.GetUserForLoginRow{}, lookupErr)

	_, err := service.LoginManual(
		context.Background(),
		email,
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)
	require.Equal(
		t,
		http.StatusUnauthorized,
		appErr.Status,
		"the mitigation must not change what an address that cannot be "+
			"authenticated is told",
	)
	require.Equal(t, login.CodeInvalidCredentials, appErr.Code)
	require.Equal(t, "invalid email or password", appErr.Message)

	require.Zero(
		t,
		limiter.ReleasesIn(testutil.LoginEmailNamespace),
		"nothing was authenticated, so nothing may be given back",
	)
	require.Equal(t, int64(1), limiter.ChargeFor(testutil.LoginEmailNamespace))
}

// The budget is charged before the lookup, so a spent budget refuses without a
// query and without an Argon2id derivation — which between them are the entire
// cost of this endpoint. There is no GetUserForLogin expectation below, so
// gomock fails this test if the lookup is reached at all: the assertion is not
// "the response looks right" but "none of the expensive work happened".
func TestService_LoginManual_ASpentBudgetStopsBeforeTheLookup(t *testing.T) {
	service, _, _, _, _, _ := newTestServiceWithLimiter(
		t,
		testutil.ExceededPinRateLimiter(),
	)

	_, err := service.LoginManual(
		context.Background(),
		"alice@example.com",
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)
	require.Equal(t, http.StatusTooManyRequests, appErr.Status)
	require.Equal(t, login.CodeLoginRateLimitExceeded, appErr.Code)

	// A refusal has to carry a wait, or a client cannot tell when to come back.
	require.NotNil(t, appErr.RetryAfterSeconds())
	require.Positive(t, *appErr.RetryAfterSeconds())
	require.LessOrEqual(
		t,
		*appErr.RetryAfterSeconds(),
		int(login.LoginEmailFailureWindow.Seconds()),
		"the wait must never exceed the window it came from",
	)
}

// A limiter that cannot answer has not said the caller is within budget. Allowing
// the request would hand anyone the ability to remove the ceiling by causing an
// outage, and the credentials would never have been tried.
func TestService_LoginManual_AnUnusableBudgetFailsClosed(t *testing.T) {
	service, _, _, _, _, _ := newTestServiceWithLimiter(
		t,
		testutil.UnavailablePinRateLimiter(
			errors.New("dial tcp: connection refused"),
		),
	)

	// No lookup expectation: the request is refused before the database is
	// touched, because nothing about the credentials was ever established.
	_, err := service.LoginManual(
		context.Background(),
		"alice@example.com",
		wrongPassword,
		session.SessionMetadata{},
	)

	require.Error(t, err)

	appErr := apperror.FromError(err)

	require.Equal(
		t,
		http.StatusServiceUnavailable,
		appErr.Status,
		"an unusable limiter must not be treated as permission",
	)
	require.Equal(t, login.CodeLoginRateLimitUnavailable, appErr.Code)

	// The two failures ask the caller to do opposite things: one means wait out a
	// window, the other means the server could not tell and their credentials were
	// never tried. Neither carries a Retry-After, because there is no window.
	require.Nil(t, appErr.RetryAfterSeconds(), "an outage has no window to wait out")
}

// A release that fails must not turn an authentication that already succeeded
// into a reported failure. The budget was enforced when it was charged — that is
// the step that bounds the work — and a charge that is not handed back makes the
// address marginally stricter for the rest of its window and then expires with
// it. Refusing the login instead would tell a legitimate user their password was
// wrong while a session row was on its way into the database, and they would
// retry and create more.
func TestService_LoginManual_AReleaseFailureDoesNotFailTheLogin(t *testing.T) {
	limiter := testutil.NewReleaseFailingPinRateLimiter(
		errors.New("dial tcp: connection refused"),
	)

	service, repository, sessionCreator, _, _, passwordHasher :=
		newTestServiceWithLimiter(t, limiter)

	const email = "alice@example.com"
	const password = "correct-password"

	expectLoginableUser(t, repository, sessionCreator, passwordHasher, email, password)

	result, err := service.LoginManual(
		context.Background(),
		email,
		password,
		session.SessionMetadata{},
	)

	require.NoError(t, err)
	require.NotEmpty(t, result.AccessToken, "the session must still be issued")
	require.Equal(t, 1, limiter.Releases, "the release must actually have been attempted")
}

// The dummy verification is the mitigation for the fact that an unknown address
// returns without doing any password work while a wrong password pays for a full
// Argon2id derivation. There is no seam to observe — the hasher is a concrete type
// with no interface in front of it — so this measures cost instead.
//
// It is deliberately a floor rather than an equality. A floor can only fail when
// the work is absent; a slow or a loaded machine makes it pass more readily, never
// less. Both paths run the same lookup against the same table, and the comparison
// is on the fastest of several runs so a single scheduling hiccup cannot decide
// it.
func TestService_LoginManual_AnUnknownAddressPaysForAPasswordDerivation(t *testing.T) {
	service, repository, sessionCreator, _, _, passwordHasher := newTestService(t)

	const email = "alice@example.com"
	const password = "correct-password"

	expectLoginableUser(t, repository, sessionCreator, passwordHasher, email, password)

	// The other half of the comparison is an address nobody holds. What the
	// repository reports for it is what an INNER JOIN on the credential table
	// reports when it matches nothing, and the timing below is the whole
	// assertion: this path must cost about what the known-account path costs.
	repository.EXPECT().
		GetUserForLogin(gomock.Any(), unknownEmail).
		Return(
			logindb.GetUserForLoginRow{},
			apperror.UnauthorizedWith(
				login.CodeInvalidCredentials,
				"invalid email or password",
				nil,
			),
		).
		AnyTimes()

	// One derivation at the active parameters is tens of milliseconds, and the
	// gap being guarded is measured in tens of milliseconds too. Asking for a
	// third of the known path separates "no derivation" from "a derivation" by an
	// order of magnitude on either side, and the shared test database round trip
	// is smaller than that margin.
	const minimumShare = 3

	const runs = 5

	known, unknown := measure(runs, func(email string) {
		_, err := service.LoginManual(
			context.Background(),
			email,
			wrongPassword,
			session.SessionMetadata{},
		)

		require.Error(t, err)
		require.Equal(
			t,
			login.CodeInvalidCredentials,
			apperror.FromError(err).Code,
			"both paths must report the same failure",
		)
	}, email, unknownEmail)

	require.NotZero(t, known, "the known-account path did no measurable work")

	require.GreaterOrEqual(
		t,
		unknown,
		known/minimumShare,
		"an unknown address returned without doing password work, so which "+
			"addresses exist can be read off how long the answer takes",
	)
}

// measure runs call once per address, timing each, and returns the fastest run
// for each.
//
// The minimum is the right statistic: every source of noise in a shared test
// machine makes a run slower and never faster, so the shortest of several runs
// compares the floor of each path rather than the luck of the scheduler.
func measure(
	runs int,
	call func(email string),
	first string,
	second string,
) (time.Duration, time.Duration) {
	fastest := func(email string) time.Duration {
		shortest := time.Duration(0)

		for range runs {
			start := time.Now()

			call(email)

			if elapsed := time.Since(start); shortest == 0 || elapsed < shortest {
				shortest = elapsed
			}
		}

		return shortest
	}

	return fastest(first), fastest(second)
}
