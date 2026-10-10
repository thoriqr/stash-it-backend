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

		// A caller that was told to come back is told when, and only when
		// something actually said so.
		//
		// The value is written here rather than inside the service because this is
		// the single place a response is written: a value carried on the error and
		// never emitted would leave a client guessing when its own window reopens,
		// which is exactly the behaviour a limit exists to make predictable.
		//
		// The gate is the value's presence, not the status. A header is written only
		// when the error was built carrying one, and nothing here invents one: a 503
		// from an unreachable counter has no recovery time anyone could name, so it
		// carries none and the response stays bare. What must never happen is a
		// fabricated value, not a configured one reaching the wire — so gating on
		// 429 alone would have made a deliberately configured wait unrepresentable
		// rather than keeping anyone honest.
		if retryAfter := appErr.RetryAfterSeconds(); retryAfter != nil {
			c.Set(
				fiber.HeaderRetryAfter,
				strconv.Itoa(*retryAfter),
			)
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
