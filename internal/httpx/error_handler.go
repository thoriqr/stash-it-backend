package httpx

import (
	"errors"
	"strconv"

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

		// A rate-limited caller is told when to come back, and only that caller.
		//
		// The value is written here rather than inside the service because this is
		// the single place a response is written: a value carried on the error and
		// never emitted would leave a client guessing when its own window reopens,
		// which is exactly the behaviour a rate limit exists to make predictable.
		//
		// It is gated on 429 rather than on the field alone. A 503 from an
		// unreachable counter has no recovery time anyone could name, so
		// fabricating one would be a lie told to a client that is already being
		// told to try again shortly.
		if appErr.Status == fiber.StatusTooManyRequests {
			if retryAfter := appErr.RetryAfterSeconds(); retryAfter != nil {
				c.Set(
					fiber.HeaderRetryAfter,
					strconv.Itoa(*retryAfter),
				)
			}
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
