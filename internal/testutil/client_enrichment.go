package testutil

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	saveditem "github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/config"
)

// NewAppWithClientEnrichmentHosts builds the app with a host allowlist, which is
// what makes a save of one of those hosts skip the enrichment queue and the
// enrich endpoint accept client metadata instead of fetching.
//
// The allowlist is configuration, not a dependency, so it is offered as a
// constructor rather than threaded through every other one: a suite about
// anything else keeps testing the server-enrichment path.
func NewAppWithClientEnrichmentHosts(
	pool *pgxpool.Pool,
	enricher enrichment.MetadataEnricher,
	enqueuer saveditem.SavedItemEnqueuer,
	hosts map[string]string,
) (*fiber.App, *FakeEmailSender) {
	cfg := testConfig()
	cfg.ClientEnrichmentHosts = hosts

	return newApp(
		pool,
		enricher,
		enqueuer,
		NewPermissivePinRateLimiter(),
		NewPermissivePINEmailBudget(),
		NewPasswordWorkLimiter(),
		cfg,
	)
}

// TestConfigWithClientEnrichmentHosts exposes the same configuration for a caller
// that assembles its own app.
func TestConfigWithClientEnrichmentHosts(hosts map[string]string) config.Config {
	cfg := testConfig()
	cfg.ClientEnrichmentHosts = hosts

	return cfg
}
