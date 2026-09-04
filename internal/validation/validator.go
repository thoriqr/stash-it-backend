package validation

import (
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

type StructValidator struct {
	validate *validator.Validate
}

func (v *StructValidator) Validate(out any) error {
	return v.validate.Struct(out)
}

func New() *StructValidator {
	validate := validator.New(
		validator.WithRequiredStructEnabled(),
	)

	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]

		if name == "-" {
			return ""
		}

		if name == "" {
			return field.Name
		}

		return name
	})

	return &StructValidator{
		validate: validate,
	}
}