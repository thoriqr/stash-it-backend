package enrichment

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	enrichmentdb "github.com/thoriqr/stash-it-backend/internal/api/enrichment/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RegisterModule wires the enrichment feature.
//
// enricher is injected rather than built here, because the real one is
// constructed around the guarded outbound HTTP client and that client is a
// security boundary, not this module's to configure. The caller supplies an
// enrichment.Enricher backed by security.NewGuardedHTTPClient, and tests supply
// a stand-in. Neither reaches this module's other wiring.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	enricher MetadataEnricher,
	log *zap.Logger,
) {
	enrichmentQueries := enrichmentdb.New(pool)

	enrichmentRepository := NewRepository(
		enrichmentQueries,
	)

	enrichmentService := NewService(
		enrichmentRepository,
		enricher,
		log,
	)

	enrichmentHandler := NewHandler(
		enrichmentService,
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	// Mounted under the same prefix as saved_item and collection, because the
	// endpoint acts on a saved item. Fiber groups are additive, so this adds
	// POST /saved-items/:id/enrich without touching the routes the other features
	// already register.
	Routes(
		app.Group("/saved-items"),
		enrichmentHandler,
		accessTokenVerifier,
	)
}
