package password_reset

import "github.com/gofiber/fiber/v3"

func Routes(router fiber.Router, h *Handler) {
	router.Post("/password-reset", h.RequestPasswordReset)

	router.Get(
		"/password-reset/verification/:verification_id",
		h.GetVerification,
	)

	router.Post(
		"/password-reset/verification/:verification_id/resend",
		h.ResendVerification,
	)

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