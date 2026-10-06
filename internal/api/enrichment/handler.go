package enrichment

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

// Enrich godoc
// @Summary Enrich one saved item's metadata
// @Description Fetches the page at the saved item's URL and stores the metadata it exposes, such as title, platform, description and preview image.
// @Description This acts on exactly one saved item. It is not a batch operation and never touches other saved items.
// @Description A page is not required to expose every field. Whatever is found is stored and the rest stay null, so an item with only a title is still enriched successfully.
// @Description If the page cannot be fetched or read, the enrichment is recorded as failed and the saved item is returned unchanged. The saved item itself stays valid: a URL that could not be enriched is still a saved item.
// @Description The saved item must belong to the authenticated user. A saved item that does not exist and one owned by another user both return the same not found response, so this endpoint never discloses whether an ID exists.
// @Description Enrichment never changes which collection the item is in.
// @Description Possible error codes:
// @Description - SAVED_ITEM_NOT_FOUND
// @Description - BAD_REQUEST
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Produce json
// @Security BearerAuth
// @Param id path string true "Saved item ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Success 200 {object} EnrichSavedItemAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items/{id}/enrich [post]
func (h *Handler) Enrich(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	savedItemID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid saved item id",
			err,
		)
	}

	result, err := h.service.Enrich(
		c.Context(),
		userID,
		savedItemID,
	)
	if err != nil {
		return err
	}

	response := mapEnrichSavedItemResponse(result)

	// 200 whether or not the enrichment succeeded. The attempt either completed
	// or failed, and the saved item is valid and usable in both cases, so the
	// outcome belongs in enrichment_status rather than in the status code. A
	// 5xx here would tell the client the saved item is broken, which it is not.
	//
	// The message is the same either way for the same reason: the request did
	// what was asked.
	return httpx.OK(
		c,
		"saved item enriched successfully",
		&response,
	)
}
