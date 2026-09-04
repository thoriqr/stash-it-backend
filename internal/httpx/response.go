package httpx

import "github.com/gofiber/fiber/v3"

type Response[T any] struct {
	Data    *T   `json:"data"`
	Message string `json:"message"`
	Meta    Meta `json:"meta"`
}

type Meta struct{}

func Success[T any](message string, data *T) Response[T] {
	return Response[T]{
		Data:    data,
		Message: message,
		Meta:    Meta{},
	}
}

func OK[T any](c fiber.Ctx, message string, data *T) error {
	return c.Status(fiber.StatusOK).JSON(
		Success(message, data),
	)
}

func Created[T any](c fiber.Ctx, message string, data *T) error {
	return c.Status(fiber.StatusCreated).JSON(
		Success(message, data),
	)
}