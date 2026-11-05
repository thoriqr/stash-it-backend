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
// @Summary Search saved items and collections
// @Description Search the authenticated user's saved items and collections.
// @Description Saved items are matched by title, domain, and URL. Collections are matched by name, including system collections.
// @Description Search supports partial matches and typo tolerance. Results are ranked by relevance, with recency used to break ties.
// @Description Results are limited and are not paginated.
// @Description Saved item results include their associated collection and enrichment status.
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
