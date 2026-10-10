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
	// It applies to manual login only, because it is an authentication budget:
	// the manual-login flow has a password to guess, and this is what bounds how
	// often a secret may be tried. The Google routes carry their own budget
	// below, and the two are separate counters under separate namespaces, so
	// neither flow's traffic can lock the other out.
	loginIPLimit := middleware.IPRateLimit(
		rateLimiter,
		loginIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    LoginIPRateLimitMax,
			Window: LoginIPRateLimitWindow,
		},
	)

	// The same placement argument applies to the Google routes, for a different
	// reason. They have no secret to guess, so nothing here stops a credential
	// being brute-forced; what it stops is the cost of serving the route. A
	// request that never reaches a valid Google token still verifies one against
	// Google's published keys, and a caller holding one valid token can otherwise
	// make the application write sessions, account link confirmations and pending
	// registrations for as long as it keeps asking.
	//
	// Mounted in front of the handler, so the count covers the attempts rather
	// than the ones that were well-formed: a body that does not parse, an id that
	// is not a UUID and a token that does not verify are all counted, and all
	// three are the cheapest requests to send.
	//
	// One handler instance mounted on all three routes, so the whole Google flow
	// spends a single budget. A middleware handler holds no per-request state, so
	// sharing one is safe, and it is what makes the sharing obvious at the call
	// site rather than something a reader has to infer from three identical
	// arguments. A caller who has been refused on Google login is not someone who
	// should be handed a fresh allowance by moving to the confirmation route.
	googleAuthIPLimit := middleware.IPRateLimit(
		rateLimiter,
		googleAuthIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    GoogleAuthIPRateLimitMax,
			Window: GoogleAuthIPRateLimitWindow,
		},
	)

	router.Post("/login", loginIPLimit, h.LoginManual)

	router.Post("/login/google", googleAuthIPLimit, h.LoginGoogle)

	router.Get(
		"/login/google/account-link/:confirmation_id",
		googleAuthIPLimit,
		h.GetAccountLinkConfirmation,
	)

	router.Post(
		"/login/google/account-link/:confirmation_id/confirm",
		googleAuthIPLimit,
		h.ConfirmAccountLink,
	)
}
