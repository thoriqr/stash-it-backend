package registration

import "github.com/gofiber/fiber/v3"

func Routes(router fiber.Router, h *Handler) {
	router.Post("/register/manual", h.RegisterManual)

	router.Get(
		"/register/verification/:verification_id",
		h.GetVerification,
	)

	router.Post(
		"/register/verification/:verification_id/pin",
		h.CreatePIN,
	)

	router.Post(
		"/register/verification/:verification_id/resend",
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