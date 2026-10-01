package main

import (
	"context"
	"fmt"

	swaggo "github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"

	_ "github.com/thoriqr/stash-it-backend/docs"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/health"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/validation"
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
		ErrorHandler:  httpx.NewErrorHandler(log),
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

	fmt.Println("Database connected")
	fmt.Println("Server running on http://localhost:8080")

	if err := app.Listen(":8080"); err != nil {
		fmt.Println("Server error:", err)
	}
}