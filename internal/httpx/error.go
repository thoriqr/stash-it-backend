package httpx

import (
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
	appErr := apperror.FromError(err)

	return c.Status(appErr.Status).JSON(errorResponse{
		Error: errorBody{
			Code:    appErr.Code,
			Message: appErr.Message,
			Fields:  appErr.Fields,
		},
	})
}