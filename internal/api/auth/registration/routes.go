package registration

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

func Routes(
	router fiber.Router,
	h *Handler,
	pinRateLimiter PinRateLimiter,
) {
	router.Post("/register/manual", h.RegisterManual)

	router.Get(
		"/register/verification/:verification_id",
		h.GetVerification,
	)

	// One handler instance mounted on both routes, so both endpoints spend the
	// same budget without either being able to name the other's. A middleware
	// handler holds no per-request state, so sharing one is safe and is what
	// makes the sharing obvious at the call site rather than something a reader
	// has to infer from two identical arguments.
	//
	// It runs before the handler, and that is deliberate: the budget is the
	// client's, so every request that reaches these routes counts against it.
	// Placing it inside the service would count only the requests that named a
	// verification this process could find, which is exactly the set an attacker
	// probing for ids would avoid producing.
	pinIPLimit := middleware.IPRateLimit(
		pinRateLimiter,
		pinIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    PinIPRateLimitMax,
			Window: PinIPRateLimitWindow,
		},
	)

	router.Post(
		"/register/verification/:verification_id/pin",
		pinIPLimit,
		h.CreatePIN,
	)

	router.Post(
		"/register/verification/:verification_id/resend",
		pinIPLimit,
		h.ResendVerification,
	)

	router.Post(
		"/register/verification/:verification_id/verify",
		h.VerifyRegistration,
	)

	router.Get(
		"/register/continuation",
		h.GetRegistrationContinuation,
	)

	router.Post(
		"/register/finalize/manual",
		h.FinalizeManualRegistration,
	)

	router.Post(
		"/register/finalize/social",
		h.FinalizeSocialRegistration,
	)
}
