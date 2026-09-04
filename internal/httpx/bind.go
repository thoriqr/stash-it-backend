package httpx

import (
	"errors"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func BindBody(c fiber.Ctx, out any) error {
	if err := c.Bind().Body(out); err != nil {
		var validationErrors validator.ValidationErrors

		if errors.As(err, &validationErrors) {
			fields := make([]apperror.ErrorField, 0, len(validationErrors))

			for _, validationErr := range validationErrors {
				fields = append(fields, apperror.ErrorField{
					Field:   validationErr.Field(),
					Message: validationMessage(validationErr),
				})
			}

			return apperror.Validation(fields, err)
		}

		return apperror.BadRequest(err)
	}

	return nil
}

func validationMessage(err validator.FieldError) string {
	switch err.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "max":
		return "is too long"
	default:
		return "is invalid"
	}
}