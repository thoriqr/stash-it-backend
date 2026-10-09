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
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	saveditem "github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/api/search"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/email"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

const testVerificationCodeSecret = "integration-test-verification-secret"

// TestAccessTokenSecret is the access token secret used by the integration test
// app. It is exported so tests can mint access tokens that the app accepts.
const TestAccessTokenSecret = "integration-test-access-token-secret"

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

type FakeEmailSender struct {
	Messages []email.Message
}

func (s *FakeEmailSender) Send(
	ctx context.Context,
	message email.Message,
) error {
	s.Messages = append(s.Messages, message)
	return nil
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

// NewAppWithLimiter builds the app with a caller-supplied PIN rate limiter.
//
// Rate limiting needs no Redis container for the suite as a whole, and a test that
// is about registration rather than about the limiter should not have to start
// one. This constructor lets a test that does care about limiting supply its own
// limiter: a fake that refuses after a set number of calls, one that fails, or the
// real Redis-backed one.
func NewAppWithLimiter(
	pool *pgxpool.Pool,
	pinRateLimiter registration.PinRateLimiter,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		&FakeEnricher{},
		nil,
		pinRateLimiter,
		config.Config{ClientIPSource: config.ClientIPSourcePeer},
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
	pinRateLimiter registration.PinRateLimiter,
) (*fiber.App, *FakeEmailSender) {
	return newApp(
		pool,
		&FakeEnricher{},
		nil,
		pinRateLimiter,
		config.Config{
			ClientIPSource:     config.ClientIPSourceProxy,
			TrustedProxies:     []string{"0.0.0.0"},
			TrustedProxyHeader: "X-Forwarded-For",
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
	return newApp(
		pool,
		enricher,
		enqueuer,
		NewPermissivePinRateLimiter(),
		config.Config{ClientIPSource: config.ClientIPSourcePeer},
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
	pinRateLimiter registration.PinRateLimiter,
	cfg config.Config,
) (*fiber.App, *FakeEmailSender) {
	validate := validation.New()

	log, err := logger.New("development")
	if err != nil {
		panic(err)
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

	auth.RegisterModule(
		app,
		pool,
		cfg,
		log,
		emailSender,
		googleTokenVerifier,
		pinRateLimiter,
	)

	saveditem.RegisterModule(
		app,
		pool,
		cfg,
		enqueuer,
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
	)

	enrichment.RegisterModule(
		app,
		pool,
		cfg,
		enricher,
		log,
	)

	return app, emailSender
}
