package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	"github.com/thoriqr/stash-it-backend/internal/health"
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

	r := chi.NewRouter()

	healthHandler := health.NewHandler()
	health.Routes(r, healthHandler)

	fmt.Println("Database connected")
	fmt.Println("Server running on http://localhost:8080")

	if err := http.ListenAndServe(":8080", r); err != nil {
		fmt.Println("Server error:", err)
	}
}