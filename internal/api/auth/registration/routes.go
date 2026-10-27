package registration

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// Routes mounts the registration flow.
//
// The limiter parameter is the per-client-address budget, and it is
// middleware.RateLimiter rather than anything this package declares: the service
// no longer spends a limiter at all, because the per-address PIN budget is a
// sliding window owned by internal/email and shared with the password reset
// feature. What remains here is the request-rate dimension, which is the
// middleware's own concern, and passwordreset.Routes takes the same type for the
// same reason.
func Routes(
	router fiber.Router,
	h *Handler,
	pinRateLimiter middleware.RateLimiter,
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

	// Recovery initiation starts the same PIN flow as the two issuance routes
	// and spends the same per-client-address budget. It sends no message
	// itself, so the per-address email budget is charged when /pin is called.
	router.Post(
		"/register/recovery/manual",
		pinIPLimit,
		h.InitiateManualRecovery,
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

	// Verification is bounded separately from issuance, and for a different
	// reason. Issuing sends a message and is bounded by a per-address budget as
	// well as this one; verifying sends nothing, but it does cost a verification
	// lookup, a code lookup, an HMAC comparison and a write on failure. Nothing
	// else bounds that, so this route carries its own budget.
	//
	// It is mounted in front of the handler for the same reason as everywhere
	// else: a verification id that is not a UUID never becomes a request the
	// handler sees, and that is the cheapest request to send.
	verifyIPLimit := middleware.IPRateLimit(
		pinRateLimiter,
		verifyIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    VerifyIPRateLimitMax,
			Window: VerifyIPRateLimitWindow,
		},
	)

	// Finalization is bounded separately again, and this is the one route here
	// that performs an Argon2id derivation. The shared PasswordWorkLimiter bounds
	// how many derivations run at once across the process, which protects the
	// process but does not stop one caller from holding every slot indefinitely;
	// this bounds how often a caller may ask.
	//
	// Mounted in front of the handler so a request naming no continuation token,
	// or carrying one that is already consumed, still costs the client a budget
	// rather than being free.
	finalizeIPLimit := middleware.IPRateLimit(
		pinRateLimiter,
		finalizeIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    FinalizeIPRateLimitMax,
			Window: FinalizeIPRateLimitWindow,
		},
	)

	router.Post(
		"/register/verification/:verification_id/verify",
		verifyIPLimit,
		h.VerifyRegistration,
	)

	router.Get(
		"/register/continuation",
		h.GetRegistrationContinuation,
	)

	router.Post(
		"/register/finalize/manual",
		finalizeIPLimit,
		h.FinalizeManualRegistration,
	)

	router.Post(
		"/register/finalize/social",
		h.FinalizeSocialRegistration,
	)
}
