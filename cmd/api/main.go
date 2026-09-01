package main

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/thoriqr/stash-it-backend/internal/health"
)

func main() {
	r := chi.NewRouter()

	healthHandler := health.NewHandler()
	health.Routes(r, healthHandler)

	fmt.Println("Server running on http://localhost:8080")

	err := http.ListenAndServe(":8080", r)
	if err != nil {
		fmt.Println("Server error:", err)
	}
}