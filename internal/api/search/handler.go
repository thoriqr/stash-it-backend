package search

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{
		service: service,
	}
}

// Search godoc
// @Summary Global Search
// @Description Search the current user's saved items and collections with one query.
// @Description Saved items are matched on title, domain and url. Collections are matched on name, and system collections are included, so a search for "unsorted" can find the Unsorted collection.
// @Description Matching prefers a substring match and falls back to fuzzy matching for typos, so "ca" finds "camera" and "camra" finds it too. A result that contains the query is always ranked above a result that only fuzzy-matches it. Recency is never a substitute for relevance: it only breaks ties between results that scored the same.
// @Description Results are capped rather than paged. Saved items return up to the internal maximum and collections up to a small fixed number. This endpoint does not accept a limit, page or cursor parameter.
// @Description Only the authenticated user's own saved items and collections are searched. Another user's matching data is never returned.
// @Description A search that matches nothing is a successful request with empty collections and saved_items arrays, not a 404.
// @Description The query must be at least 2 and at most 128 characters. Surrounding whitespace is trimmed.
// @Description Possible error codes:
// @Description - INVALID_SEARCH_QUERY
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Search
// @Produce json
// @Security BearerAuth
// @Param q query string true "Search query, at least 2 and at most 128 characters" minLength(2) maxLength(128) example(camera)
// @Success 200 {object} SearchAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Search query is blank, shorter than 2 characters or longer than 128 characters"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /search [get]
func (h *Handler) Search(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req SearchRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	// The limit is passed as zero, which the service resolves to its own default.
	// There is no limit parameter on this endpoint, so the handler has no value to
	// forward and cannot widen the service's bounds.
	result, err := h.service.Search(
		c.Context(),
		userID,
		req.Query,
		0,
	)
	if err != nil {
		return err
	}

	response := mapSearchResponse(result)

	// 200 even when nothing matched: an empty result is a successful search, not a
	// missing resource.
	return httpx.OK(
		c,
		"search completed successfully",
		&response,
	)
}
