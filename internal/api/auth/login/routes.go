package login

import "github.com/gofiber/fiber/v3"

func Routes(router fiber.Router, h *Handler) {
	router.Post("/login", h.LoginManual)

	router.Post("/login/google", h.LoginGoogle)
}
