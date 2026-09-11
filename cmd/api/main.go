package main

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"

	"github.com/thoriqr/stash-it-backend/internal/auth"
	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	"github.com/thoriqr/stash-it-backend/internal/health"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/security"
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

	validate := validation.New(
)


	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler,
		StructValidator: validate,
	})

	app.Use(recoverer.New())

	
	// Health
	healthHandler := health.NewHandler()
	health.Routes(app, healthHandler)

	// Auth
	authQueries := authdb.New(pool)

	registrationRepository := auth.NewRegistrationRepository(
		pool,
		authQueries,
	)

	verificationCodeHasher := security.NewVerificationCodeHasher(
		[]byte(cfg.VerificationCodeSecret),
	)

	passwordHasher := security.NewPasswordHasher()

	authService := auth.NewRegistrationService(
		registrationRepository,
		passwordHasher,
		verificationCodeHasher,
	)

	authHandler := auth.NewHandler(authService)

	auth.Routes(app, authHandler)


	fmt.Println("Database connected")
	fmt.Println("Server running on http://localhost:8080")

	if err := app.Listen(":8080"); err != nil {
		fmt.Println("Server error:", err)
	}
}