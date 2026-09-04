package auth

import "github.com/gofiber/fiber/v3"

func Routes(app *fiber.App, h *Handler) {
	app.Post("/users", h.CreateUser)
}