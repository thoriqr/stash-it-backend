package login

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Service struct {
	repository           Repository
	sessionService       session.SessionCreator
	registrationService  registration.SocialRegistrationService
	googleTokenVerifier  GoogleTokenVerifier
	passwordHasher       *security.PasswordHasher
	accessTokenGenerator *security.AccessTokenGenerator

	// rateLimiter guards manual login only. Google login does not read it: that
	// flow has no password to guess and its credential is verified against the
	// provider, so applying the manual-login policy to it would limit a user for
	// something they did not do wrong.
	rateLimiter LoginRateLimiter
}

func NewService(
	repository Repository,
	sessionService session.SessionCreator,
	registrationService registration.SocialRegistrationService,
	googleTokenVerifier GoogleTokenVerifier,
	passwordHasher *security.PasswordHasher,
	accessTokenGenerator *security.AccessTokenGenerator,
	rateLimiter LoginRateLimiter,
) *Service {
	return &Service{
		repository:           repository,
		sessionService:       sessionService,
		registrationService:  registrationService,
		googleTokenVerifier:  googleTokenVerifier,
		passwordHasher:       passwordHasher,
		accessTokenGenerator: accessTokenGenerator,
		rateLimiter:          rateLimiter,
	}
}

type LoginResult struct {
	User         logindb.GetUserForLoginRow
	Session      sessiondb.Session
	AccessToken  string
	RefreshToken string
}

func (s *Service) LoginManual(
	ctx context.Context,
	email string,
	password string,
	metadata session.SessionMetadata,
) (LoginResult, error) {
	// The same normalization registration stored the address with. Without it a
	// user who registered as Alice@Example.com would be told their password was
	// wrong when they typed the address they registered with.
	//
	// Normalized once and used for both the budget and the lookup, so the two can
	// never disagree about which account is being limited.
	normalizedEmail := registration.NormalizeEmail(email)

	// Charged before anything else happens, including the database read.
	//
	// The budget is the email address's, and the address only exists in the
	// request body, so this cannot be a route concern the way the per-address one
	// is. It runs before the lookup so an address that has already spent its
	// budget is refused without a query and without an Argon2id derivation, which
	// is the entire cost of this endpoint.
	//
	// Charging first rather than counting after the failure is deliberate. A
	// check-then-count arrangement would let a burst of concurrent requests all
	// read "within budget", all start a derivation, and only be refused
	// afterwards: the limit would then bound how many requests are ultimately
	// refused while bounding none of the work that costs anything. Charging first
	// bounds the work. The charge is handed back below when this is not a failure.
	charged, err := s.chargeEmailFailure(ctx, normalizedEmail)
	if err != nil {
		return LoginResult{}, err
	}

	if !charged.Allowed {
		return LoginResult{}, apperror.TooManyRequestsWithRetryAfter(
			CodeLoginRateLimitExceeded,
			"too many login attempts, please try again later",
			charged.RetryAfter,
		)
	}

	user, err := s.repository.GetUserForLogin(ctx, normalizedEmail)
	if err != nil {
		// The repository reports an address with no account and an account with
		// no password credential identically, because the lookup joins the
		// credential table. Both are failed authentications as far as this
		// budget is concerned, so neither releases the charge, and the caller
		// receives the same generic response either way.
		//
		// The one thing done before returning is the dummy verification. Leaving
		// immediately would return before any password work, while a wrong
		// password for a real account pays for a full derivation — a gap large
		// enough to tell an observer which addresses exist, and one that also
		// makes enumerating them cheap.
		//
		// A failure here is reported instead of the repository's error, because
		// it means something different: the password was never checked. Reporting
		// invalid credentials for a request that never looked at one would be a
		// lie the caller would act on, and it would be the same lie whichever
		// kind of account they had, so it leaks nothing either way.
		if dummyErr := s.passwordHasher.DummyVerify(ctx, password); dummyErr != nil {
			return LoginResult{}, passwordWorkError(
				dummyErr,
				s.passwordHasher.Wait(),
			)
		}

		return LoginResult{}, err
	}

	result, err := s.passwordHasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		if errors.Is(err, security.ErrPasswordWorkCapacityTimeout) {
			// The password was never checked, which is a different answer from the
			// one below and has to reach the caller as one.
			return LoginResult{}, passwordWorkError(
				err,
				s.passwordHasher.Wait(),
			)
		}

		// A stored hash this build cannot parse. The charge stands, because the
		// caller did not authenticate, and the fault is reported as it always has
		// been rather than folded into the generic response below. See the note
		// in PROGRESS.md: making this a 401 as well was considered and left as a
		// separate decision.
		return LoginResult{}, apperror.Internal(err)
	}

	if !result.Match {
		// A failed authentication. The charge is deliberately left in place: this
		// is the occurrence the budget exists to count.
		return LoginResult{}, apperror.UnauthorizedWith(
			CodeInvalidCredentials,
			"invalid email or password",
			nil,
		)
	}

	// Authenticated. The charge was taken optimistically, before it was known
	// whether this would fail, so it is handed back now and the successful login
	// spends nothing.
	s.releaseEmailFailure(ctx, normalizedEmail)

	sessionResult, err := s.sessionService.CreateSession(
		ctx,
		user.ID,
		metadata,
	)
	if err != nil {
		return LoginResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		user.ID,
		sessionResult.Session.ID,
		session.AccessTokenLifetime,
	)
	if err != nil {
		return LoginResult{}, apperror.Internal(err)
	}

	return LoginResult{
		User:         user,
		Session:      sessionResult.Session,
		AccessToken:  accessToken,
		RefreshToken: sessionResult.RefreshToken,
	}, nil
}

// chargeEmailFailure spends one unit of an address's failure budget.
func (s *Service) chargeEmailFailure(
	ctx context.Context,
	normalizedEmail string,
) (ratelimit.Result, error) {
	result, err := s.rateLimiter.Allow(
		ctx,
		loginEmailFailureNamespace,
		normalizedEmail,
		ratelimit.Policy{
			Max:    LoginEmailFailureLimit,
			Window: LoginEmailFailureWindow,
		},
	)
	if err != nil {
		// Fail closed. A limiter that cannot answer has not said the caller is
		// within budget, it has said nothing, and letting the request through
		// would hand anyone the ability to remove the ceiling by causing an
		// outage.
		//
		// The error names the namespace only, so nothing derived from the address
		// reaches a log line built from it.
		return ratelimit.Result{}, apperror.ServiceUnavailableWith(
			CodeLoginRateLimitUnavailable,
			"login is temporarily unavailable, please try again shortly",
			err,
		)
	}

	return result, nil
}

// releaseEmailFailure hands back the charge taken by chargeEmailFailure.
//
// It deliberately does not fail the login, and does not return an error to be
// acted on. The budget was enforced when it was charged — that is the step that
// mattered, and it is the step that fails closed. A charge that is not handed
// back makes this address marginally stricter for the remainder of its window and
// then expires with it, which is the safe direction. Refusing an authentication
// that has already succeeded would instead tell a legitimate user their login
// failed while a session row was on its way into the database, and they would
// retry and create more.
//
// The condition is self-limiting and is already observable: an unreachable Redis
// makes the *next* login fail closed with CodeLoginRateLimitUnavailable, so an
// outage that caused unreleased charges is visible without this reporting its
// own.
func (s *Service) releaseEmailFailure(ctx context.Context, normalizedEmail string) {
	_, _ = s.rateLimiter.Release(
		ctx,
		loginEmailFailureNamespace,
		normalizedEmail,
	)
}

// passwordWorkError maps a refusal to do password work at all onto the response
// the caller receives.
//
// Only a capacity timeout becomes this. A cancelled context stays a context
// error and a hash this build cannot parse stays a fault, because the three ask
// the caller to do different things: this one means come back shortly, a
// cancellation means stop, and a parse fault means nothing the caller can act
// on. Folding the first two together would tell a caller that went away that it
// was merely busy, and would send it back to retry a request nobody is waiting
// for.
//
// The wait is passed rather than read from a package constant because it is the
// deployment's configured value, and the header it produces has to be the same
// number the caller was actually kept waiting for.
func passwordWorkError(err error, wait time.Duration) error {
	if !errors.Is(err, security.ErrPasswordWorkCapacityTimeout) {
		return err
	}

	return apperror.ServiceUnavailableWithRetryAfter(
		CodePasswordWorkUnavailable,
		"the server is busy, please try again shortly",
		wait,
		err,
	)
}

type LoginGoogleResult struct {
	Outcome LoginOutcome

	VerificationID uuid.UUID
	ConfirmationID uuid.UUID

	User         *logindb.GetUserForLoginByIDRow
	Session      *sessiondb.Session
	AccessToken  string
	RefreshToken string
}

func (s *Service) LoginGoogle(
	ctx context.Context,
	idToken string,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	identity, err := s.googleTokenVerifier.Verify(
		ctx,
		idToken,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	// Normalized once, here, because every use below compares against or writes
	// the same column: the existing-user lookup, the link confirmation's snapshot
	// and the registration handed to the registration feature. Normalizing only
	// the lookup would leave the rest of the flow carrying a form of the address
	// that no exact comparison will ever match again.
	email := registration.NormalizeEmail(identity.Email)

	authIdentity, err := s.repository.GetAuthIdentity(
		ctx,
		"google",
		identity.Subject,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	if authIdentity.ID != uuid.Nil {
		return s.loginWithUser(
			ctx,
			authIdentity.UserID,
			metadata,
		)
	}

	user, err := s.repository.GetUserByEmail(
		ctx,
		email,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	if user.ID != uuid.Nil {
		expiresAt := time.Now().Add(
			AccountLinkConfirmationLifetime,
		)

		confirmation, err := s.repository.CreateAccountLinkConfirmation(
			ctx,
			logindb.CreateAccountLinkConfirmationParams{
				UserID:          user.ID,
				Provider:        "google",
				ProviderSubject: identity.Subject,
				EmailSnapshot: pgtype.Text{
					String: email,
					Valid:  true,
				},
				DisplayNameSnapshot: pgtype.Text{
					String: identity.DisplayName,
					Valid:  identity.DisplayName != "",
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  expiresAt,
					Valid: true,
				},
			},
		)
		if err != nil {
			return LoginGoogleResult{}, err
		}

		return LoginGoogleResult{
			Outcome:        LoginOutcomeAccountLinkRequired,
			ConfirmationID: confirmation.ID,
		}, nil
	}

	verificationID, err := s.registrationService.CreateSocialRegistration(
		ctx,
		registration.CreateSocialRegistrationInput{
			Email:           email,
			Provider:        "google",
			ProviderSubject: identity.Subject,
			EmailSnapshot: pgtype.Text{
				String: email,
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: identity.DisplayName,
				Valid:  identity.DisplayName != "",
			},
		},
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	return LoginGoogleResult{
		Outcome:        LoginOutcomeRegistrationRequired,
		VerificationID: verificationID,
	}, nil
}

type GetAccountLinkConfirmationResult struct {
	ID                  uuid.UUID
	Provider            string
	EmailSnapshot       string
	DisplayNameSnapshot string
	UserEmail           string
	UserDisplayName     string
}

func (s *Service) GetAccountLinkConfirmation(
	ctx context.Context,
	id uuid.UUID,
) (GetAccountLinkConfirmationResult, error) {
	confirmation, err := s.repository.GetActiveAccountLinkConfirmation(ctx, id)
	if err != nil {
		return GetAccountLinkConfirmationResult{}, err
	}

	return GetAccountLinkConfirmationResult{
		ID:                  confirmation.ID,
		Provider:            confirmation.Provider,
		EmailSnapshot:       confirmation.EmailSnapshot.String,
		DisplayNameSnapshot: confirmation.DisplayNameSnapshot.String,
		UserEmail:           confirmation.UserEmail,
		UserDisplayName:     confirmation.UserDisplayName,
	}, nil
}

func (s *Service) ConfirmAccountLink(
	ctx context.Context,
	confirmationID uuid.UUID,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	identity, err := s.repository.ConfirmAccountLink(
		ctx,
		confirmationID,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	return s.loginWithUser(
		ctx,
		identity.UserID,
		metadata,
	)
}

func (s *Service) loginWithUser(
	ctx context.Context,
	userID uuid.UUID,
	metadata session.SessionMetadata,
) (LoginGoogleResult, error) {
	user, err := s.repository.GetUserForLoginByID(
		ctx,
		userID,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	sessionResult, err := s.sessionService.CreateSession(
		ctx,
		user.ID,
		metadata,
	)
	if err != nil {
		return LoginGoogleResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
		user.ID,
		sessionResult.Session.ID,
		session.AccessTokenLifetime,
	)
	if err != nil {
		return LoginGoogleResult{}, apperror.Internal(err)
	}

	return LoginGoogleResult{
		Outcome:      LoginOutcomeAuthenticated,
		User:         &user,
		Session:      &sessionResult.Session,
		AccessToken:  accessToken,
		RefreshToken: sessionResult.RefreshToken,
	}, nil
}
