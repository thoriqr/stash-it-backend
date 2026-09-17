package httpx

import "github.com/gofiber/fiber/v3"

type Response[T any] struct {
	Data    *T   `json:"data"`
	Message string `json:"message"`
	Meta    Meta `json:"meta"`
}

type Meta struct {
	Pagination *Pagination `json:"pagination,omitempty"`
}

type Pagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

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

func OKWithMeta[T any](
	c fiber.Ctx,
	message string,
	data *T,
	meta Meta,
) error {
	return c.Status(fiber.StatusOK).JSON(
		Response[T]{
			Data:    data,
			Message: message,
			Meta:    meta,
		},
	)
}

func Created[T any](c fiber.Ctx, message string, data *T) error {
	return c.Status(fiber.StatusCreated).JSON(
		Success(message, data),
	)
}

func OKMessage(c fiber.Ctx, message string) error {
	return c.Status(fiber.StatusOK).JSON(
		Response[any]{
			Data:    nil,
			Message: message,
			Meta:    Meta{},
		},
	)
}