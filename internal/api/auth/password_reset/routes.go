package password_reset

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// Routes mounts the password reset flow.
//
// Three of these routes carry a per-address budget; three do not, and the
// difference is deliberate rather than incomplete.
//
// The three that do are the ones that either reveal something about an address
// or cost real money to serve. Each gets its own namespace and its own policy,
// because they are different acts: opening a reset, reading one, and mailing a
// code do not cost the same thing and a flood of one must not spend another's
// allowance.
//
// The three that do not — /verify, /continuation and /finalize — are outside
// the scope of this change and are **pending their own security review**. They
// are mounted exactly as they were, with no middleware, and nothing about them
// should be read as having been assessed. In particular /finalize performs the
// one Argon2id derivation in this flow and is currently unbounded, which is a
// known gap rather than a decision.
func Routes(
	router fiber.Router,
	h *Handler,
	rateLimiter middleware.RateLimiter,
) {
	// Mounted in front of the handler, as everywhere else. A service never sees a
	// request whose body failed to bind or whose address does not exist, and
	// probing addresses that do not exist is the cheapest request to send — so a
	// limit enforced inside the service would count exactly the set an attacker
	// probing accounts would avoid producing.
	requestIPLimit := middleware.IPRateLimit(
		rateLimiter,
		requestIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    RequestIPRateLimitMax,
			Window: RequestIPRateLimitWindow,
		},
	)

	statusIPLimit := middleware.IPRateLimit(
		rateLimiter,
		statusIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    StatusIPRateLimitMax,
			Window: StatusIPRateLimitWindow,
		},
	)

	// One handler instance mounted on both issuance routes, so the whole flow
	// spends a single budget. A middleware handler holds no per-request state, so
	// sharing one is safe, and it is what makes the sharing obvious at the call
	// site rather than something a reader has to infer from two identical
	// arguments. A caller who has been refused on /pin is not someone who should
	// be handed a fresh allowance by moving to /resend.
	//
	// This is the same reasoning registration uses for the same pair of routes.
	pinIPLimit := middleware.IPRateLimit(
		rateLimiter,
		pinIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    PinIPRateLimitMax,
			Window: PinIPRateLimitWindow,
		},
	)

	router.Post("/password-reset", requestIPLimit, h.RequestPasswordReset)

	router.Get(
		"/password-reset/verification/:verification_id",
		statusIPLimit,
		h.GetVerification,
	)

	router.Post(
		"/password-reset/verification/:verification_id/pin",
		pinIPLimit,
		h.CreatePIN,
	)

	router.Post(
		"/password-reset/verification/:verification_id/resend",
		pinIPLimit,
		h.ResendVerification,
	)

	// Not rate limited, and not yet reviewed. See the note on Routes.
	router.Post(
		"/password-reset/verification/:verification_id/verify",
		h.VerifyPasswordReset,
	)

	router.Get(
		"/password-reset/continuation",
		h.GetPasswordResetContinuation,
	)

	router.Post(
		"/password-reset/finalize",
		h.FinalizePasswordReset,
	)
}
