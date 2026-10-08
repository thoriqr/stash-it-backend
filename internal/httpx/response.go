package httpx

import "github.com/gofiber/fiber/v3"

type Response[T any] struct {
	Data    *T     `json:"data"`
	Message string `json:"message"`
	Meta    *Meta  `json:"meta,omitempty"`
}

type Meta struct {
	Pagination *Pagination `json:"pagination,omitempty"`

	// Cursor carries keyset pagination state. It is a pointer with omitempty so an
	// endpoint that does not paginate by cursor leaves the key out of its response
	// entirely, which is exactly what Pagination already does for endpoints that
	// report no pagination at all. Adding it here changes no existing response.
	Cursor *CursorPage `json:"cursor,omitempty"`
}

type Pagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// CursorPage reports whether more results follow a cursor-paginated response.
//
// NextCursor carries no omitempty on purpose. A nil pointer tagged omitempty is
// omitted from the JSON entirely, so a client reading next_cursor would find the key
// missing rather than present and null, and a missing key and a null are two
// different things to decode. Emitting an explicit null means a client can read one
// field and treat null as "there is no next page" without first having to ask
// whether the key exists.
type CursorPage struct {
	// NextCursor is the token for the following page, or nil on the final page and
	// on an empty result. It is nil whenever HasMore is false.
	NextCursor *string `json:"next_cursor"`

	// HasMore is computed from one extra row beyond the requested limit, so it is
	// exact rather than inferred from a count. There is deliberately no total: a
	// cursor-paginated response does not know how many results exist overall, and
	// counting them per request is the work keyset pagination exists to avoid.
	HasMore bool `json:"has_more"`
}

func Success[T any](message string, data *T) Response[T] {
	return Response[T]{
		Data:    data,
		Message: message,
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
			Meta:    &meta,
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
		},
	)
}
