package client_release

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	clientreleasedb "github.com/thoriqr/stash-it-backend/internal/api/client_release/generated"
)

// RegisterModule wires the client release check by hand, bottom up, with no
// container.
//
// clientreleasedb.Queries -> Repository -> Service -> Handler -> GET /client-releases/latest
//
// It needs no config, no Redis and no auth: the endpoint is a single read that
// must remain reachable while the rest of the application is being diagnosed.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	log *zap.Logger,
) {
	clientReleaseQueries := clientreleasedb.New(pool)

	clientReleaseRepository := NewRepository(
		clientReleaseQueries,
	)

	clientReleaseService := NewService(
		clientReleaseRepository,
		log,
	)

	clientReleaseHandler := NewHandler(
		clientReleaseService,
	)

	Routes(
		app.Group("/client-releases"),
		clientReleaseHandler,
	)
}
