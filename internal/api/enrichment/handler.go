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
// @Description Fetch metadata from the saved item's URL.
// @Description Enrichment may return partial metadata when some fields are unavailable. A failed enrichment does not invalidate or remove the saved item.
// @Description The enrichment result is reported through enrichment_status in the response.
// @Description Enrichment never changes the saved item's collection, whether the server fetched the page or the client supplied the metadata. Automatic organization belongs to the save path.
// @Description For a host in CLIENT_ENRICHMENT_HOSTS the server does not fetch the page: an optional metadata object carries what the client extracted. At least one field completes the item; none marks it failed. The body is ignored for every other host.
// @Description The metadata object carries title, description and image_url only. The platform is derived from the URL's host by the server, so a client must not send it.
// @Description Possible error codes:
// @Description - SAVED_ITEM_NOT_FOUND
// @Description - BAD_REQUEST
// @Description - VALIDATION_ERROR
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - USER_RATE_LIMIT_EXCEEDED
// @Description - USER_RATE_LIMIT_UNAVAILABLE
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Saved item ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Param body body EnrichSavedItemRequest false "Client-extracted metadata, only read for a client-enrichment host"
// @Success 200 {object} EnrichSavedItemAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid saved item ID or client metadata"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
// @Failure 429 {object} swagger.APIErrorResponse "Rate limit exceeded for this user"
// @Failure 503 {object} swagger.APIErrorResponse "Rate limiting is unavailable, so the fetch is not attempted"
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

	// The body is optional. An older client, or any caller enriching a host the
	// server fetches itself, sends nothing and the request behaves exactly as it
	// always has.
	var request EnrichSavedItemRequest
	if len(c.Body()) > 0 {
		if err := httpx.BindBody(c, &request); err != nil {
			return err
		}
	}

	result, err := h.service.Enrich(
		c.Context(),
		userID,
		savedItemID,
		request.Metadata,
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
