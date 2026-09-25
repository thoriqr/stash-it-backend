package login

import "github.com/gofiber/fiber/v3"

func Routes(router fiber.Router, h *Handler) {
	router.Post("/login", h.LoginManual)

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
