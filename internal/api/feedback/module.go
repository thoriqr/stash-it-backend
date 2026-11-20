package feedback

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	feedbackdb "github.com/thoriqr/stash-it-backend/internal/api/feedback/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RegisterModule wires the feedback feature.
//
// Only the user-facing route is registered. Triage is not part of this API: it is the
// operator page, a separate listener bound to loopback and wired in cmd/api/main.go,
// which reads the same table through its own queries. Nothing here exposes a way to
// read, change or delete a report over HTTP.
//
// That split is the reason this module needs no secret. A maintenance route guarded by
// one would still be a public route — reachable by anyone who found the secret — while
// the page it would duplicate is reachable only by someone already holding an SSH
// session to the machine. SSH is the authentication, and it is the stronger of the two.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	userRateLimiter middleware.RateLimiter,
	log *zap.Logger,
) {
	feedbackQueries := feedbackdb.New(pool)

	feedbackRepository := NewRepository(feedbackQueries)

	feedbackService := NewService(feedbackRepository)

	feedbackHandler := NewHandler(feedbackService)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	Routes(
		app.Group("/feedback"),
		feedbackHandler,
		accessTokenVerifier,
		userRateLimiter,
	)

	log.Info(
		"feedback module registered",
		zap.Int("create_user_limit_max", CreateUserRateLimitMax),
		zap.Duration("create_user_limit_window", CreateUserRateLimitWindow),
	)
}
