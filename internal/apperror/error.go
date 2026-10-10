package apperror

import (
	"errors"
	"math"
	"net/http"
	"time"
)

const (
	CodeBadRequest   = "BAD_REQUEST"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeForbidden    = "FORBIDDEN"
	CodeNotFound     = "RESOURCE_NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeInternal     = "INTERNAL_SERVER_ERROR"
	CodeValidation   = "VALIDATION_ERROR"
	CodeTooMany      = "TOO_MANY_REQUESTS"
	CodeUnavailable  = "SERVICE_UNAVAILABLE"
)

const (
	MessageBadRequest   = "bad request"
	MessageUnauthorized = "unauthorized"
	MessageForbidden    = "forbidden"
	MessageNotFound     = "resource not found"
	MessageConflict     = "conflict"
	MessageInternal     = "internal server error"
	MessageValidation   = "request validation failed"
	MessageTooMany      = "too many requests"
	MessageUnavailable  = "service unavailable"
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

	// retryAfterSeconds, when set, is written to the response as a `Retry-After`
	// header.
	//
	// It is nil unless an error was built by a constructor that was given a wait,
	// which is what keeps a response from ever carrying a header nobody chose.
	// Only TooManyRequestsWithRetryAfter and ServiceUnavailableWithRetryAfter set
	// it today.
	//
	// It is unexported and read through RetryAfterSeconds because the error
	// handler is the only thing that should decide how it reaches the wire, and
	// because a duration that has not been converted yet is not a header value.
	retryAfterSeconds *int
}

// RetryAfterSeconds reports the wait a rate-limited caller was given, and
// whether there was one at all.
//
// The second return is the point: a 429 the server cannot say anything useful
// about recovery has no value, and a response that carried a guessed one would be
// telling a client to come back at a time nobody chose.
func (e *AppError) RetryAfterSeconds() *int {
	return e.retryAfterSeconds
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

// TooManyRequests returns a generic 429 Too Many Requests error.
func TooManyRequests(err error) *AppError {
	return New(
		http.StatusTooManyRequests,
		CodeTooMany,
		MessageTooMany,
		err,
	)
}

// TooManyRequestsWith returns a 429 Too Many Requests error with custom code and
// message.
func TooManyRequestsWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeTooMany
	}

	if message == "" {
		message = MessageTooMany
	}

	return New(
		http.StatusTooManyRequests,
		code,
		message,
		err,
	)
}

// RetryAfterSeconds converts a wait into the whole number of seconds a
// `Retry-After` header carries.
//
// It rounds up, because the header's contract is "not before", and rounding down
// would name a moment the window is still closed. A zero or negative wait becomes
// zero, which says retry immediately rather than saying something impossible.
func RetryAfterSeconds(wait time.Duration) *int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 0 {
		seconds = 0
	}

	return &seconds
}

// TooManyRequestsWithRetryAfter returns a 429 carrying a `Retry-After` header
// value derived from wait.
//
// The wait is the time until the rate-limited window resets, which the caller
// reads from the counter itself rather than assuming from the configured window.
// Reporting the configured window instead would tell a client to wait out time
// that had already passed every time the request arrived late in a window.
func TooManyRequestsWithRetryAfter(
	code, message string,
	wait time.Duration,
) *AppError {
	appErr := TooManyRequestsWith(code, message, nil)
	appErr.retryAfterSeconds = RetryAfterSeconds(wait)

	return appErr
}

// ServiceUnavailable returns a generic 503 Service Unavailable error.
func ServiceUnavailable(err error) *AppError {
	return New(
		http.StatusServiceUnavailable,
		CodeUnavailable,
		MessageUnavailable,
		err,
	)
}

// ServiceUnavailableWith returns a 503 Service Unavailable error with custom code
// and message.
func ServiceUnavailableWith(code, message string, err error) *AppError {
	if code == "" {
		code = CodeUnavailable
	}

	if message == "" {
		message = MessageUnavailable
	}

	return New(
		http.StatusServiceUnavailable,
		code,
		message,
		err,
	)
}

// ServiceUnavailableWithRetryAfter returns a 503 carrying a `Retry-After` header
// value derived from wait.
//
// This is the one kind of 503 whose recovery time somebody actually knows.
// ServiceUnavailable alone carries no wait, because a limiter that cannot answer
// has no window to name and inventing one would tell a client to come back at a
// moment nobody chose. A server refusing work because it is temporarily out of
// capacity is a different thing: it can say roughly when a slot frees up, and
// saying so is what stops a client from retrying straight back into the same
// refusal.
//
// The underlying error is retained here where TooManyRequestsWithRetryAfter does
// not take one. A caller refused by a rate limit is being told something the
// response already says, whereas refusing work is a fault worth logging with
// whatever caused it.
func ServiceUnavailableWithRetryAfter(
	code, message string,
	wait time.Duration,
	err error,
) *AppError {
	appErr := ServiceUnavailableWith(code, message, err)
	appErr.retryAfterSeconds = RetryAfterSeconds(wait)

	return appErr
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
