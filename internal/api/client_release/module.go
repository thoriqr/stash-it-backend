package client_release

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	clientreleasedb "github.com/thoriqr/stash-it-backend/internal/api/client_release/generated"
)

// RegisterModule wires both client-release surfaces by hand, bottom up, with no
// container.
//
// The public check is clientreleasedb.Queries -> Repository -> Service ->
// Handler -> GET /client-releases/latest. It needs no config, no Redis and no
// auth: it is a single read that must remain reachable while the rest of the
// application is being diagnosed.
//
// The maintenance routes add the admin repository, whose transactions need the
// pool, and are guarded by releaseSecret. The guard runs before any database
// access, so an unauthenticated caller never starts the work.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	log *zap.Logger,
	releaseSecret string,
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

	adminRepository := NewAdminRepository(
		clientReleaseQueries,
		pool,
	)

	adminService := NewAdminService(
		adminRepository,
		log,
	)

	adminHandler := NewAdminHandler(
		adminService,
	)

	MaintenanceRoutes(
		app.Group("/internal/maintenance"),
		adminHandler,
		releaseSecret,
	)
}
