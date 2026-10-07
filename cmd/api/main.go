package main

import (
	"context"
	"fmt"

	swaggo "github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/redis/go-redis/v9"

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
	ctx := context.Background()

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

	app := fiber.New(fiber.Config{
		ErrorHandler:    httpx.NewErrorHandler(log),
		StructValidator: validate,
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

	auth.RegisterModule(
		app,
		pool,
		cfg,
		log,
		emailSender,
		googleTokenVerifier,
	)

	// The queue client the API uses for one thing only: putting a saved item id on
	// the enrichment queue once the row is committed. It shares the Redis
	// connection below rather than opening its own, so the process has one pool and
	// one Close.
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

	fmt.Println("Database connected")
	fmt.Println("Server running on http://localhost:8080")

	if err := app.Listen(":8080"); err != nil {
		fmt.Println("Server error:", err)
	}
}
