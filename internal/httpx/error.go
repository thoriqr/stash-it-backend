package httpx

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string                `json:"code"`
	Message string                `json:"message"`
	Fields  []apperror.ErrorField `json:"fields"`
}

func ErrorHandler(c fiber.Ctx, err error) error {
	var fiberErr *fiber.Error

	if errors.As(err, &fiberErr) {
		return fiber.DefaultErrorHandler(c, fiberErr)
	}

	appErr := apperror.FromError(err)

	return c.Status(appErr.Status).JSON(errorResponse{
		Error: errorBody{
			Code:    appErr.Code,
			Message: appErr.Message,
			Fields:  appErr.Fields,
		},
	})
}