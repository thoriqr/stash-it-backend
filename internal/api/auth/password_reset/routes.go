package password_reset

import (
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// Routes mounts the password reset flow.
//
// Five of these routes carry a per-address budget; one does not, and the
// difference is deliberate rather than incomplete.
//
// The five that do are the ones that either reveal something about an address,
// cost real money to serve, or do expensive work. Each gets its own namespace and
// its own policy, because they are different acts: opening a reset, reading one,
// mailing a code, verifying one and hashing a password do not cost the same thing
// and a flood of one must not spend another's allowance.
//
// The one that does not — the continuation lookup — is a pure read that is
// deliberately left unlimited, on the same reasoning registration applies to its
// own: limiting it would cost a polling client its budget without bounding any
// expensive work. See the note on the route below.
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

	// Verification is bounded separately from issuance, and for a different
	// reason. Issuing sends a message and is expensive; verifying sends nothing,
	// but it does cost a verification lookup, a code lookup, an HMAC comparison
	// and a write on failure. Nothing else bounds that, so this route carries its
	// own budget.
	//
	// Mounted in front of the handler so a verification id that is not a UUID
	// never becomes a request the handler sees, and so a refused request spends
	// no code attempt.
	verifyIPLimit := middleware.IPRateLimit(
		rateLimiter,
		verifyIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    VerifyIPRateLimitMax,
			Window: VerifyIPRateLimitWindow,
		},
	)

	// Finalization is bounded separately again, and it is the only route here that
	// performs an Argon2id derivation. The shared PasswordWorkLimiter bounds how
	// many derivations run at once across the whole process, which protects the
	// process but does not stop one caller from holding every slot indefinitely;
	// this bounds how often a caller may ask.
	//
	// Mounted in front of the handler so a request carrying no continuation token,
	// or one that is already consumed, still costs the client a budget rather than
	// being free — and, more to the point, so a refused request never reaches the
	// hashing step at all.
	finalizeIPLimit := middleware.IPRateLimit(
		rateLimiter,
		finalizeIPRateLimitNamespace,
		ratelimit.Policy{
			Max:    FinalizeIPRateLimitMax,
			Window: FinalizeIPRateLimitWindow,
		},
	)

	router.Post(
		"/password-reset/verification/:verification_id/verify",
		verifyIPLimit,
		h.VerifyPasswordReset,
	)

	// Deliberately unlimited. It is a pure read — one indexed lookup that consumes
	// nothing and writes nothing — and a client that watches its own reset's state
	// while waiting out the sixty-second cooldown would spend a budget without
	// bounding any work. Registration reaches the same conclusion for the same
	// reason on its own continuation route.
	router.Get(
		"/password-reset/continuation",
		h.GetPasswordResetContinuation,
	)

	router.Post(
		"/password-reset/finalize",
		finalizeIPLimit,
		h.FinalizePasswordReset,
	)
}
