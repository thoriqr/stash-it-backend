package search

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	searchdb "github.com/thoriqr/stash-it-backend/internal/api/search/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RegisterModule wires the search feature by hand, bottom up, with no container.
//
// searchdb.Queries -> Repository -> Service -> Handler -> GET /search
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
) {
	searchQueries := searchdb.New(pool)

	searchRepository := NewRepository(
		searchQueries,
	)

	searchService := NewService(
		searchRepository,
	)

	searchHandler := NewHandler(
		searchService,
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	Routes(
		app.Group("/search"),
		searchHandler,
		accessTokenVerifier,
	)
}
