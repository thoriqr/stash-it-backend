package health

import "github.com/gofiber/fiber/v3"

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) Check(c fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": "ok",
	})
}