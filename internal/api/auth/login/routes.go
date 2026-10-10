package login

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

func Routes(
	router fiber.Router,
	h *Handler,
	rateLimiter middleware.RateLimiter,
) {
	// Mounted in front of the handler rather than inside the service, and that
	// placement is the whole reason it is here. A check inside the service would
	// never see a request that failed body binding, one carrying headers the
	// session metadata extractor rejects, or one naming an address that does not
	// exist — which is exactly the set an attacker enumerating addresses sends,
	// and exactly the set that costs almost nothing to send.
	//
	// Mounting before the handler makes the count cover the attempts rather than
	// only the attempts that were well-formed.
	//
	// It applies to manual login only. The Google routes are untouched: that flow
	// verifies a token against the provider and has no password to guess, so
	// spending an authentication budget on it would penalise a user for
	// something they did not do wrong.
	loginIPLimit := middleware.IPRateLimit(
		rateLimiter,
		loginIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    LoginIPRateLimitMax,
			Window: LoginIPRateLimitWindow,
		},
	)

	router.Post("/login", loginIPLimit, h.LoginManual)

	router.Post("/login/google", h.LoginGoogle)

	router.Get(
		"/login/google/account-link/:confirmation_id",
		h.GetAccountLinkConfirmation,
	)

	router.Post(
		"/login/google/account-link/:confirmation_id/confirm",
		h.ConfirmAccountLink,
	)
}
