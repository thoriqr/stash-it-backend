package testutil

import (
	"context"
	"sync"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	saveditem "github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/api/search"
	"github.com/thoriqr/stash-it-backend/internal/api/user"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/email"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const testVerificationCodeSecret = "integration-test-verification-secret"

// TestAccessTokenSecret is the access token secret used by the integration test
// app. It is exported so tests can mint access tokens that the app accepts.
const TestAccessTokenSecret = "integration-test-access-token-secret"

// TestSessionCleanupSecret is the maintenance secret the integration test app is
// built with. It is exported so tests can present the header value the app
// accepts, and so a test that needs the endpoint closed can build an app
// without it.
const TestSessionCleanupSecret = "integration-test-session-cleanup-secret"

// testConfig is the Config every test app starts from unless it is deliberately
// exercising a different one.
//
// It carries the maintenance secret so a test can drive the cleanup route the
// way a scheduler would. The value is a placeholder that exists only in this
// file, which is why it is safer than anything that would have to be kept
// somewhere real: it cannot be mistaken for a deployment credential because it
// is not one.
func testConfig() config.Config {
	return config.Config{
		ClientIPSource:      config.ClientIPSourcePeer,
		SessionCleanupSecret: TestSessionCleanupSecret,
	}
}

type fakeGoogleTokenVerifier struct {
	identity login.GoogleIdentity
	err      error
}

func (f *fakeGoogleTokenVerifier) Verify(
	ctx context.Context,
	idToken string,
) (login.GoogleIdentity, error) {
	return f.identity, f.err
}

// FakeEmailSender records the messages it was asked to deliver.
//
// The append is guarded because a concurrent test drives several requests through
// the app at once, and an unguarded append is a genuine race rather than a merely
// untidy one. FakeEnricher below is guarded for the same reason. `Messages` stays
// an exported field so the tests that already read it do not have to change; those
// reads happen between requests, never during one.
type FakeEmailSender struct {
	mutex    sync.Mutex
	Messages []email.Message

	// err, when set, is returned instead of delivering. It exists so a test can
	// reach the paths that only a failed send takes — the delivery-failure
	// response, and the refund rule's two branches.
	err error
}

func (s *FakeEmailSender) Send(
	ctx context.Context,
	message email.Message,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.err != nil {
		return s.err
	}

	s.Messages = append(s.Messages, message)

	return nil
}

// Fail makes every subsequent send return err, and is how a test reaches a
// delivery that fails.
//
// It is a method rather than an exported field so that arming it and the handler
// reading it are ordered by the same mutex. Setting a bare field from the test
// goroutine and reading it inside the handler is a data race the detector would
// flag, and the ordering the test intends would not be guaranteed.
func (s *FakeEmailSender) Fail(err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.err = err
}

// FakeEnricher stands in for the real extraction layer.
//
// The real one is built around the guarded outbound HTTP client, and that client
// correctly refuses loopback, so a test could not reach an httptest server
// through it. Injecting a fake at the MetadataEnricher boundary is what lets the
// endpoint be tested end to end without a network, and it keeps the real
// enrichment package out of the assertion path entirely: what these tests verify
// is the application's behaviour, not the extractor's.
type FakeEnricher struct {
	// Metadata is returned for every call. A zero value is a page that exposed
	// nothing, which is a valid outcome rather than a failure.
	Metadata enrichmentcore.Metadata

	// Err, when set, is returned instead of Metadata, standing in for a page that
	// could not be fetched or read.
	Err error

	mutex sync.Mutex
	urls  []string
}

func (f *FakeEnricher) Enrich(
	_ context.Context,
	rawURL string,
) (enrichmentcore.Metadata, error) {
	f.mutex.Lock()
	f.urls = append(f.urls, rawURL)
	f.mutex.Unlock()

	if f.Err != nil {
		return enrichmentcore.Metadata{}, f.Err
	}

	return f.Metadata, nil
}

// RequestedURLs returns every URL the fake was asked to enrich, in order.
func (f *FakeEnricher) RequestedURLs() []string {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return append([]string(nil), f.urls...)
}

func NewApp(pool *pgxpool.Pool) (*fiber.App, *FakeEmailSender) {
	return NewAppWithLimiter(pool, NewPermissivePinRateLimiter())
}

// NewAppWithoutSessionCleanupSecret builds the app with no maintenance secret
// configured, which is the state of a deployment that has not set one up.
//
// It exists because "the route refuses every request" has to be provable from
// the same app that otherwise works. Building that state any other way — by
// sending a wrong secret to a configured app — would test a different branch
// and would leave the unconfigured one unproven, which is the branch that
// decides whether an unconfigured deployment is quietly exposed or quietly
// closed.
func NewAppWithoutSessionCleanupSecret(
	pool *pgxpool.Pool,
) (*fiber.App, *FakeEmailSender) {
	app, sender := newApp(
		pool,
		&FakeEnricher{},
		nil,
		NewPermissivePinRateLimiter(),
		NewPermissivePINEmailBudget(),
		NewPasswordWorkLimiter(),
		config.Config{ClientIPSource: config.ClientIPSourcePeer},
	)

	return app, sender
}

// NewAppWithLimiter builds the app with a caller-supplied PIN rate limiter.
//
// Rate limiting needs no Redis container for the suite as a whole, and a test that
// is about registration rather than about the limiter should not have to start
// one. This constructor lets a test that does care about limiting supply its own
// limiter: a fake that refuses after a set number of calls, one that fails, or the
// real Redis-backed one.
func NewAppWithLimiter(
	pool *pgxpool.Pool,
	pinRateLimiter auth.RateLimiter,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		&FakeEnricher{},
		nil,
		pinRateLimiter,
		NewPermissivePINEmailBudget(),
		NewPasswordWorkLimiter(),
		testConfig(),
	)
}

// NewAppWithLimiters builds the app with both limiters supplied by the caller.
//
// It exists for a test about password-work capacity, which has to hand the app a
// limiter whose slots are already taken: there is no other way to make a request
// find none, and waiting for a real derivation to occupy one would make the test
// depend on how long Argon2id takes on the machine running it.
func NewAppWithLimiters(
	pool *pgxpool.Pool,
	pinRateLimiter auth.RateLimiter,
	passwordWorkLimiter *security.PasswordWorkLimiter,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		&FakeEnricher{},
		nil,
		pinRateLimiter,
		NewPermissivePINEmailBudget(),
		passwordWorkLimiter,
		testConfig(),
	)
}

// NewAppWithTrustedProxy builds the app configured as it would be behind a
// reverse proxy, so a test can drive real client addresses through it.
//
// The allowlist is the unspecified address because that is the only peer
// `app.Test` can present: it serves requests from an in-memory connection with no
// address of its own. Naming it as the trusted proxy is what makes a forwarded
// header in the request the thing that decides the client address, which is the
// only way to test per-address behaviour without a real socket to vary.
//
// It is test scaffolding and says nothing about what a deployment should set. A
// deployment's allowlist has to come from its actual network.
func NewAppWithTrustedProxy(
	pool *pgxpool.Pool,
	pinRateLimiter auth.RateLimiter,
) (*fiber.App, *FakeEmailSender) {
	return NewAppWithTrustedProxyAndBudget(
		pool,
		pinRateLimiter,
		NewPermissivePINEmailBudget(),
	)
}

// NewAppWithTrustedProxyAndBudget builds the trusted-proxy app with a
// caller-supplied PIN email budget as well.
//
// It exists for the test that shows the per-client-address budget and the
// per-address email budget are separate counters: proving that needs both to be
// real, and the email budget is only real when it is the production component
// over a real Redis rather than a fake that admits everything.
func NewAppWithTrustedProxyAndBudget(
	pool *pgxpool.Pool,
	pinRateLimiter auth.RateLimiter,
	pinEmailBudget auth.PINEmailBudget,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		&FakeEnricher{},
		nil,
		pinRateLimiter,
		pinEmailBudget,
		NewPasswordWorkLimiter(),
		config.Config{
			ClientIPSource:      config.ClientIPSourceProxy,
			TrustedProxies:      []string{"0.0.0.0"},
			TrustedProxyHeader:  "X-Forwarded-For",
			SessionCleanupSecret: TestSessionCleanupSecret,
		},
	)
}

// NewAppWithEnricher builds the app with a caller-supplied enricher.
//
// NewApp delegates here with a fake that returns empty metadata, so the tests
// that never touch enrichment keep working unchanged.
func NewAppWithEnricher(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
) (*fiber.App, *FakeEmailSender) {
	return NewAppWithEnqueuer(pool, enricher, nil)
}

// NewAppWithEnqueuer builds the app with a caller-supplied enricher and enqueuer.
//
// Background enrichment is triggered by saving, so a test that cares about the
// queue needs to supply the thing the app enqueues into: a fake to inspect what
// was queued, or the real producer when the test wants the task to actually be
// picked up by a worker. The interface is taken rather than the fake so both are
// possible, and the caller keeps the reference it needs either way.
//
// A nil enqueuer is passed through to the module, which is the same as saying
// "this process schedules nothing", so the tests that never save are unaffected.
func NewAppWithEnqueuer(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
	enqueuer saveditem.SavedItemEnqueuer,
) (*fiber.App, *FakeEmailSender) {
	return NewAppWithEnqueuerAndLimiter(
		pool,
		enricher,
		enqueuer,
		NewPermissivePinRateLimiter(),
	)
}

// NewAppWithEnqueuerAndLimiter builds the app with a caller-supplied enricher,
// enqueuer and rate limiter.
//
// One limiter serves every budget in the process, exactly as the composition root
// does it, so a test that is about a saved-item budget can hand in a limiter that
// spends a real one without also arranging what the authentication routes do. A
// limiter is injected as one object rather than one per namespace because that is
// what production wires: the namespaces are what tell two budgets apart, and a
// limiter per namespace would make the isolation untestable.
func NewAppWithEnqueuerAndLimiter(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
	enqueuer saveditem.SavedItemEnqueuer,
	pinRateLimiter auth.RateLimiter,
) (*fiber.App, *FakeEmailSender) {
	return NewAppWithPINEmailBudget(
		pool,
		enricher,
		enqueuer,
		pinRateLimiter,
		NewPermissivePINEmailBudget(),
	)
}

// NewAppWithPINEmailBudget builds the app with a caller-supplied PIN email budget
// as well.
//
// The budget is injected as one object rather than one per feature for the same
// reason the limiter is: production hands registration and password reset the same
// instance, and that sharing is the property a test about shared budgets needs to
// be able to assert. A test that supplied two would be asserting a wiring
// production does not have.
//
// Every other constructor in this file defaults it to a permissive budget, so a
// suite about anything other than PIN email budgets does not have to stand up
// Redis to assert something else.
func NewAppWithPINEmailBudget(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
	enqueuer saveditem.SavedItemEnqueuer,
	pinRateLimiter auth.RateLimiter,
	pinEmailBudget auth.PINEmailBudget,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		enricher,
		enqueuer,
		pinRateLimiter,
		pinEmailBudget,
		NewPasswordWorkLimiter(),
		testConfig(),
	)
}

// newApp builds the app from every injectable dependency.
//
// The queue enqueuer and the PIN rate limiter are both passed through, including
// as nil, because "this process does that for nothing" is a thing a test is
// allowed to want.
//
// proxySettings is derived from the same config the production binary derives its
// own from, so a test exercises the resolution path the application actually
// configures rather than a hand-built approximation of it.
func newApp(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
	enqueuer saveditem.SavedItemEnqueuer,
	pinRateLimiter auth.RateLimiter,
	pinEmailBudget auth.PINEmailBudget,
	passwordWorkLimiter *security.PasswordWorkLimiter,
	cfg config.Config,
) (*fiber.App, *FakeEmailSender) {
	validate := validation.New()

	log, err := logger.New("development")
	if err != nil {
		panic(err)
	}

	// The budget policy is resolved from configuration in the real binary. A test
	// builds its Config literal field by field and would otherwise carry a
	// zero-valued budget, which validates as nothing at all — so the defaults are
	// filled in here, once, and every test app gets the same numbers production
	// gets when the environment says nothing.
	if cfg.PINEmailBudget == (config.PINEmailBudget{}) {
		cfg.PINEmailBudget = config.DefaultPINEmailBudget()
	}

	proxySettings := cfg.ProxySettings()

	app := fiber.New(fiber.Config{
		ErrorHandler:       httpx.NewErrorHandler(log),
		StructValidator:    validate,
		TrustProxy:         proxySettings.TrustProxy,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: proxySettings.Proxies},
		ProxyHeader:        proxySettings.ProxyHeader,
		EnableIPValidation: proxySettings.EnableIPValidation,
	})

	app.Use(recoverer.New())
	app.Use(requestid.New())

	cfg.AppEnv = "development"
	cfg.VerificationCodeSecret = testVerificationCodeSecret
	cfg.AccessTokenSecret = TestAccessTokenSecret

	emailSender := &FakeEmailSender{}

	googleTokenVerifier := &fakeGoogleTokenVerifier{
		identity: login.GoogleIdentity{
			Subject:       "google-subject-123",
			Email:         "google@example.com",
			EmailVerified: true,
			DisplayName:   "Google User",
		},
	}

	// Built once here for the same reason cmd/api/main.go builds it once: the
	// limiter it wraps is process-wide and now has two consumers, so two hashers
	// would be two allowances rather than two views of one.
	passwordHasher := security.NewPasswordHasher(passwordWorkLimiter)

	auth.RegisterModule(
		app,
		pool,
		cfg,
		log,
		emailSender,
		googleTokenVerifier,
		pinRateLimiter,
		pinEmailBudget,
		passwordHasher,
	)

	user.RegisterModule(
		app,
		pool,
		cfg,
		passwordHasher,
	)

	saveditem.RegisterModule(
		app,
		pool,
		cfg,
		enqueuer,
		pinRateLimiter,
		log,
	)

	collection.RegisterModule(
		app,
		pool,
		cfg,
	)

	search.RegisterModule(
		app,
		pool,
		cfg,
		pinRateLimiter,
	)

	enrichment.RegisterModule(
		app,
		pool,
		cfg,
		enricher,
		pinRateLimiter,
		log,
	)

	return app, emailSender
}
