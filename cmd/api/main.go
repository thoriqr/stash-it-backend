package main

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	"github.com/thoriqr/stash-it-backend/internal/health"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/validation"
)

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("Config error:", err)
		return
	}

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Println("Database error:", err)
		return
	}
	defer pool.Close()

	validate := validation.New()

	app := fiber.New(fiber.Config{
		ErrorHandler:   httpx.ErrorHandler,
		StructValidator: validate,
	})

	app.Use(recoverer.New())

	healthHandler := health.NewHandler()
	health.Routes(app, healthHandler)

	googleTokenVerifier := login.NewGoogleTokenVerifier(
    cfg.GoogleClientID,
	)

	auth.RegisterModule(
    app,
    pool,
    cfg,
    googleTokenVerifier,
	)

	fmt.Println("Database connected")
	fmt.Println("Server running on http://localhost:8080")

	if err := app.Listen(":8080"); err != nil {
		fmt.Println("Server error:", err)
	}
}