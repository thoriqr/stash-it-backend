package apperror

import (
	"errors"
	"net/http"
)

const (
	CodeBadRequest    = "BAD_REQUEST"
	CodeUnauthorized  = "UNAUTHORIZED"
	CodeForbidden     = "FORBIDDEN"
	CodeNotFound      = "RESOURCE_NOT_FOUND"
	CodeConflict      = "CONFLICT"
	CodeInternal      = "INTERNAL_SERVER_ERROR"
	CodeValidation    = "VALIDATION_ERROR"
)

const (
	MessageBadRequest   = "bad request"
	MessageUnauthorized = "unauthorized"
	MessageForbidden    = "forbidden"
	MessageNotFound     = "resource not found"
	MessageConflict     = "conflict"
	MessageInternal     = "internal server error"
	MessageValidation   = "request validation failed"
)

type ErrorField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type AppError struct {
	Status  int
	Code    string
	Message string
	Fields  []ErrorField
	Err     error
}

func New(status int, code, message string, err error) *AppError {
	return &AppError{
		Status:  status,
		Code:    code,
		Message: message,
		Fields:  []ErrorField{},
		Err:     err,
	}
}

func NewWithFields(
	status int,
	code, message string,
	fields []ErrorField,
	err error,
) *AppError {
	if fields == nil {
		fields = []ErrorField{}
	}

	return &AppError{
		Status:  status,
		Code:    code,
		Message: message,
		Fields:  fields,
		Err:     err,
	}
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}

	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func FromError(err error) *AppError {
	if appErr, ok := errors.AsType[*AppError](err); ok {
		return appErr
	}

	return Internal(err)
}

// BadRequest returns a generic 400 Bad Request error.
func BadRequest(err error) *AppError {
	return New(
		http.StatusBadRequest,
		CodeBadRequest,
		MessageBadRequest,
		err,
	)
}

// BadRequestWith returns a 400 Bad Request error with custom code and message.
func BadRequestWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeBadRequest
	}

	if message == "" {
		message = MessageBadRequest
	}

	return New(
		http.StatusBadRequest,
		code,
		message,
		err,
	)
}

// Validation returns a 400 validation error with field-level errors.
func Validation(fields []ErrorField, err error) *AppError {
	return NewWithFields(
		http.StatusBadRequest,
		CodeValidation,
		MessageValidation,
		fields,
		err,
	)
}

// Unauthorized returns a generic 401 Unauthorized error.
func Unauthorized(err error) *AppError {
	return New(
		http.StatusUnauthorized,
		CodeUnauthorized,
		MessageUnauthorized,
		err,
	)
}

func UnauthorizedWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeUnauthorized
	}

	if message == "" {
		message = MessageUnauthorized
	}

	return New(
		http.StatusUnauthorized,
		code,
		message,
		err,
	)
}

// Forbidden returns a generic 403 Forbidden error.
func Forbidden(err error) *AppError {
	return New(
		http.StatusForbidden,
		CodeForbidden,
		MessageForbidden,
		err,
	)
}

func ForbiddenWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeForbidden
	}

	if message == "" {
		message = MessageForbidden
	}

	return New(
		http.StatusForbidden,
		code,
		message,
		err,
	)
}

// NotFound returns a generic 404 Not Found error.
func NotFound(err error) *AppError {
	return New(
		http.StatusNotFound,
		CodeNotFound,
		MessageNotFound,
		err,
	)
}

func NotFoundWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeNotFound
	}

	if message == "" {
		message = MessageNotFound
	}

	return New(
		http.StatusNotFound,
		code,
		message,
		err,
	)
}

// Conflict returns a generic 409 Conflict error.
func Conflict(err error) *AppError {
	return New(
		http.StatusConflict,
		CodeConflict,
		MessageConflict,
		err,
	)
}

func ConflictWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeConflict
	}

	if message == "" {
		message = MessageConflict
	}

	return New(
		http.StatusConflict,
		code,
		message,
		err,
	)
}

// Internal returns a generic 500 Internal Server Error.
func Internal(err error) *AppError {
	return New(
		http.StatusInternalServerError,
		CodeInternal,
		MessageInternal,
		err,
	)
}

func InternalWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeInternal
	}

	if message == "" {
		message = MessageInternal
	}

	return New(
		http.StatusInternalServerError,
		code,
		message,
		err,
	)
}