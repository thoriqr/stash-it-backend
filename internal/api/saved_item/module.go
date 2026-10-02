package saved_item

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
) {
	savedItemQueries := saveditemdb.New(pool)

	savedItemRepository := NewRepository(
		savedItemQueries,
	)

	savedItemService := NewService(
		savedItemRepository,
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
