package collection

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
) {
	collectionQueries := collectiondb.New(pool)

	collectionRepository := NewRepository(
		pool,
		collectionQueries,
	)

	collectionService := NewService(
		collectionRepository,
	)

	collectionHandler := NewHandler(
		collectionService,
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	// Mounted under the same prefix as saved_item, because the endpoint acts on a
	// saved item. Fiber groups are additive, so this adds
	// PUT /saved-items/:id/collection without touching the routes saved_item
	// already registers.
	Routes(
		app.Group("/saved-items"),
		collectionHandler,
		accessTokenVerifier,
	)
}
