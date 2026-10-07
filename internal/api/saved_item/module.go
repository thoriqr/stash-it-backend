package saved_item

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RegisterModule wires the saved item feature.
//
// enqueuer schedules background enrichment for items this module creates. It is
// injected rather than built here because the producer is queue infrastructure and
// this module's job is to decide that a task is due, not how a task is queued; the
// composition root owns the connection it enqueues on. It may be nil, which is
// how a process that does not schedule anything is expressed — saving still
// works, it just does not arrange for the metadata to arrive on its own.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	enqueuer SavedItemEnqueuer,
	log *zap.Logger,
) {
	savedItemQueries := saveditemdb.New(pool)

	savedItemRepository := NewRepository(
		savedItemQueries,
	)

	savedItemService := NewService(
		savedItemRepository,
		enqueuer,
		log,
	)

	savedItemHandler := NewHandler(
		savedItemService,
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	Routes(
		app.Group("/saved-items"),
		savedItemHandler,
		accessTokenVerifier,
	)
}
