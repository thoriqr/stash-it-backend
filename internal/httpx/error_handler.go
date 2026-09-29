package httpx

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"go.uber.org/zap"

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

func NewErrorHandler(log *zap.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		var fiberErr *fiber.Error

		if errors.As(err, &fiberErr) {
			return fiber.DefaultErrorHandler(c, fiberErr)
		}

		appErr := apperror.FromError(err)

		requestID := requestid.FromContext(c)

		fields := []zap.Field{
			zap.String("request_id", requestID),
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", appErr.Status),
			zap.String("code", appErr.Code),
			zap.Error(err),
		}

		if appErr.Status >= 500 {
			log.Error("request failed", fields...)
		} else {
			log.Warn("request failed", fields...)
		}

		return c.Status(appErr.Status).JSON(errorResponse{
			Error: errorBody{
				Code:    appErr.Code,
				Message: appErr.Message,
				Fields:  appErr.Fields,
			},
		})
	}
}