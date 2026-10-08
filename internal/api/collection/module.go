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

	// Mounted under the same prefix as saved_item, because these endpoints act on a
	// saved item. Fiber groups are additive, so this adds
	// PUT /saved-items/:id/collection without touching the routes saved_item
	// already registers.
	Routes(
		app.Group("/saved-items"),
		collectionHandler,
		accessTokenVerifier,
	)

	// Mounted under its own prefix, because deleting a collection acts on a
	// collection. It is deliberately not /saved-items/:id: that path is the saved
	// item delete, which is a different resource and a different operation. The two
	// stay separate endpoints rather than one calling the other, so a saved item
	// delete can never remove a collection as a side effect.
	CollectionRoutes(
		app.Group("/collections"),
		collectionHandler,
		accessTokenVerifier,
	)
}
