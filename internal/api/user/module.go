package user

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// RegisterModule wires the user feature by hand, bottom up, with no container.
//
// userdb.Queries -> Repository -> Service -> Handler -> /users
//
// passwordHasher is passed in rather than built here, and that is the one thing
// about this signature worth reading twice.
//
// security.NewPasswordHasher takes the process-wide *security.PasswordWorkLimiter
// and bounds Hash, Verify and DummyVerify with it, so a hasher built from a
// second limiter would not be a second copy of the same protection — it would be
// a second allowance, and the total derivations running at once would be the sum
// of the two rather than the configured maximum. The hasher is therefore
// constructed once, in cmd/api/main.go, from the single limiter built there, and
// handed to this module and to auth.RegisterModule as the same value.
//
// The module is a sibling of auth rather than a child of it, which is what makes
// that sharing necessary rather than merely tidy: auth.RegisterModule owns no
// process-wide resource it could lend, and reaching inside it for one would make
// the profile endpoints depend on a module they have nothing else to do with.
//
// userRateLimiter is the process's existing limiter, passed for the same reason
// rather than built here. It is the same Redis connection and the same script
// behind every other budget in the application, so this feature spends one pool
// and closes one client.
func RegisterModule(
	app *fiber.App,
	pool *pgxpool.Pool,
	cfg config.Config,
	passwordHasher *security.PasswordHasher,
	userRateLimiter middleware.RateLimiter,
) {
	userQueries := userdb.New(pool)

	userRepository := NewRepository(
		userQueries,
		pool,
	)

	userService := NewService(
		userRepository,
		passwordHasher,
	)

	userHandler := NewHandler(
		userService,
	)

	accessTokenVerifier := security.NewAccessTokenVerifier(
		[]byte(cfg.AccessTokenSecret),
	)

	Routes(
		app.Group("/users"),
		userHandler,
		accessTokenVerifier,
		userRateLimiter,
	)
}
