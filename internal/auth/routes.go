package auth

import "github.com/gofiber/fiber/v3"

func Routes(app *fiber.App, h *RegistrationHandler) {
	app.Post("/auth/register/manual", h.RegisterManual)

	app.Get(
		"/auth/register/verification/:verification_id",
		h.GetVerification,
	)

	app.Post(
		"/auth/register/verification/:verification_id/resend",
		h.ResendVerification,
	)

	app.Post(
		"/auth/register/verification/:verification_id/verify",
		h.VerifyRegistration,
	)

	app.Get(
		"/auth/register/continuation",
		h.GetRegistrationContinuation,
	)

	app.Post(
    "/auth/register/finalize/manual",
    h.FinalizeManualRegistration,
	)
}