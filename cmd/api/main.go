package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	swaggo "github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	_ "github.com/thoriqr/stash-it-backend/docs"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	saveditem "github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/api/search"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	"github.com/thoriqr/stash-it-backend/internal/email"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/health"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/validation"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// @title Stash It API
// @version 1.0
// @description REST API for Stash It.
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Enter your access token using the Bearer scheme. Example: "Bearer {token}"
func main() {
	// The signal context is created before anything else, for the same reason
	// cmd/worker/main.go creates its own first: a SIGTERM arriving during startup
	// then unwinds through the deferred closes below instead of being resolved by
	// the platform's default disposition, which terminates the process outright and
	// runs none of them.
	//
	// Registering a handler at all is what changes the outcome on Windows. Go's
	// console handler returns to the OS "unhandled" when nothing is subscribed to
	// the signal, and the OS then ends the process with STATUS_CONTROL_C_EXIT. That
	// is the 0xc000013a an interrupted run reports, and no defer in main can run
	// against it because it is an OS termination rather than a return from main.
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("Config error:", err)
		return
	}

	log, err := logger.New(cfg.AppEnv)
	if err != nil {
		fmt.Println("Logger error:", err)
		return
	}
	defer func() {
		_ = log.Sync()
	}()

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Println("Database error:", err)
		return
	}
	defer pool.Close()

	validate := validation.New()

	// Client address resolution is configured from one declared source, and every
	// part of it is derived rather than set separately here. That is what makes it
	// impossible to end up reading a forwarding header without a verified
	// allowlist beside it: there is only one place the two are decided, and the
	// framework settings are its output.
	//
	// In the default peer mode all four settings are zero, so `c.IP()` is the
	// socket's own address and no header in the request can influence it however
	// it is spelled. Proxy mode is the only way to change that, and it cannot be
	// reached without naming the proxies that are allowed to set the header.
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

	app.Get("/docs/*", swaggo.New(swaggo.Config{
		DefaultModelsExpandDepth: -1,
	}))

	healthHandler := health.NewHandler()
	health.Routes(app, healthHandler)

	googleTokenVerifier := login.NewGoogleTokenVerifier(
		cfg.GoogleClientID,
	)

	var emailSender email.Sender

	if cfg.AppEnv == "development" {
		emailSender = email.NewDevSender(log)
	} else {
		emailSender = email.NewUnconfiguredSender()
	}

	// The queue client the API uses for one thing only: putting a saved item id on
	// the enrichment queue once the row is committed. It shares the Redis
	// connection below rather than opening its own, so the process has one pool and
	// one Close.
	//
	// It is built before any module is registered because the auth module needs
	// the same connection for PIN rate limiting, and there is no reason for two
	// features to hold separate pools against one Redis.
	//
	// ParseURL keeps the whole Redis configuration in one value and accepts
	// redis:// as well as rediss://, so the API and the worker read the same
	// variable and neither needs to know whether the endpoint is local Docker,
	// Cloud Run or a VPS.
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		fmt.Println("Redis url error:", err)
		return
	}

	redisClient := redis.NewClient(redisOptions)
	defer func() {
		_ = redisClient.Close()
	}()

	enrichmentProducer := queue.NewProducer(redisClient)

	// Rate limiting reuses the connection above rather than opening another, so
	// the process still has one pool and one Close. The limiter refuses to answer
	// when Redis is unreachable, which is why Check runs here: a feature that
	// guards sending email treats an unreachable counter as a refusal, and this
	// line is where that wiring mistake would otherwise be discovered by a user
	// rather than at startup.
	//
	// The secret keys the subject hash and is the same one verification codes are
	// HMAC'd with, which is already required at startup and already a secret this
	// process holds.
	pinRateLimiter := ratelimit.New(
		redisClient,
		[]byte(cfg.VerificationCodeSecret),
	)

	if err := pinRateLimiter.Check(ctx); err != nil {
		log.Error("rate limiter unavailable at startup", zap.Error(err))

		return
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

	// The enqueuer is injected rather than built inside saved_item so that the save
	// flow depends on a one-method interface and knows nothing about Asynq, Redis
	// or the task format.
	saveditem.RegisterModule(
		app,
		pool,
		cfg,
		enrichmentProducer,
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

	// Enrichment fetches a URL the user supplied, so it goes through the guarded
	// outbound HTTP client. The client is the SSRF boundary and the policy is
	// passed alongside it because the response body limit is applied by
	// enrichment, which is the component that reads the body.
	outboundFetchPolicy := security.DefaultOutboundFetchPolicy()

	metadataEnricher := enrichmentcore.NewEnricher(
		security.NewGuardedHTTPClient(outboundFetchPolicy),
		outboundFetchPolicy,
	)

	enrichment.RegisterModule(
		app,
		pool,
		cfg,
		metadataEnricher,
		log,
	)

	log.Info(
		"api started",
		zap.String("app_env", cfg.AppEnv),
		zap.String("addr", listenAddr),
		// Recorded because it is what any IP-based rate limit keys on, and a
		// deployment that resolved it differently from what it expected would
		// otherwise leave no trace at all. The startup line is where the
		// deployment's actual client-address configuration becomes part of the
		// record rather than something a reader has to infer from the config.
		zap.String("client_ip_source", string(cfg.ClientIPSource)),
		zap.Strings("trusted_proxies", proxySettings.Proxies),
		zap.String("proxy_header", proxySettings.ProxyHeader),
		// Only the parsed connection endpoint, never cfg.RedisURL itself. A Redis
		// URL may carry a password in its userinfo section, and logging it would
		// write a live credential into the log stream. The address and the database
		// are what make a startup line diagnosable; ownership of the connection is
		// unaffected.
		zap.String("redis_addr", redisOptions.Addr),
		zap.Int("redis_db", redisOptions.DB),
	)

	// Listen blocks for as long as the server runs, so it cannot also be the thing
	// that waits for a signal. It runs on its own goroutine and reports the result
	// back on a buffered channel: buffered so the goroutine always completes its
	// send even after main has taken the signal branch and is on its way out, which
	// is what keeps this from leaking a blocked goroutine on shutdown.
	serverErr := make(chan error, 1)

	go func() {
		serverErr <- app.Listen(listenAddr)
	}()

	select {
	case err := <-serverErr:
		// The listener stopped on its own. That is a real failure, not a shutdown:
		// an address already in use, an unbindable port. It is reported exactly as
		// it was before this select existed, and the deferred closes still run
		// because this branch returns from main rather than falling through.
		//
		// A graceful shutdown never arrives here. Fiber's Listen returns nil once
		// ShutdownWithTimeout has closed the listener, so the signal branch below is
		// the only path a normal stop can take.
		if err != nil {
			fmt.Println("Server error:", err)
		}

		return

	case <-ctx.Done():
		log.Info("api shutting down")
	}

	// Graceful stop, and deliberately bounded. Fiber's own Shutdown is
	// ShutdownWithContext(context.Background()), which waits for every connection to
	// go idle with no ceiling, so a client holding a keepalive connection could keep
	// the process up indefinitely and turn a graceful stop back into an abrupt one.
	//
	// Exceeding the timeout is not silent: Fiber returns the context error, which is
	// logged below, because requests being cut off is the one outcome of a shutdown
	// an operator needs to see.
	if err := app.ShutdownWithTimeout(shutdownTimeout); err != nil {
		log.Error("api shutdown error", zap.Error(err))
	}

	log.Info("api stopped")

	// Falling off the end of main runs the deferred closes in the order they were
	// registered: the Redis client, then the database pool, then the log flush. That
	// ordering is the point. The server has already drained, so no handler is left
	// holding a connection out of either pool when it closes, and the log survives
	// long enough to record that it happened.
}
