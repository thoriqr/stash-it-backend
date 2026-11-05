package saved_item

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Handler struct {
	service SavedItemService
}

func NewHandler(service SavedItemService) *Handler {
	return &Handler{
		service: service,
	}
}

// Create godoc
// @Summary Save a URL
// @Description Save a URL to the authenticated user's inbox.
// @Description The saved item is returned with an initial enrichment_status of "pending". Metadata enrichment is handled asynchronously.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_URL
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
// @Param request body CreateSavedItemRequest true "Save URL request"
// @Success 201 {object} CreateSavedItemAPIResponse "The saved item's id, url, collection_id and enrichment_status"
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error or invalid URL"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 429 {object} swagger.APIErrorResponse "Rate limit exceeded for this user"
// @Failure 503 {object} swagger.APIErrorResponse "Rate limiting is unavailable, so the save is refused"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items [post]
func (h *Handler) Create(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req CreateSavedItemRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.Create(
		c.Context(),
		userID,
		req.URL,
	)
	if err != nil {
		return err
	}

	response := mapCreateSavedItemResponse(result)

	return httpx.Created(
		c,
		"saved item created successfully",
		&response,
	)
}

// List godoc
// @Summary List recent saved items
// @Description Retrieve the authenticated user's saved items across all collections, ordered by newest first.
// @Description Saved items are returned regardless of their collection or enrichment status. Each item includes its associated collection.
// @Description enrichment_status indicates the enrichment state of an item: pending, completed, or failed. A completed status does not guarantee that all metadata fields are populated.
// @Description Pass meta.cursor.next_cursor from the previous response to retrieve the next page. A null next_cursor indicates the final page.
// @Description Possible error codes:
// @Description - BAD_REQUEST (unparsable limit or cursor length)
// @Description - INVALID_CURSOR
// @Description - VALIDATION_ERROR (limit outside 1..50)
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Produce json
// @Security BearerAuth
// @Param limit query int false "Number of saved items per page. Defaults to 20, capped at 50." minimum(1) maximum(50) default(20) example(20)
// @Param cursor query string false "Opaque cursor from meta.cursor.next_cursor of the previous page. Omit to start at the newest item." maxLength(512) example(eyJ2IjoxLCJ2YWwiOiIyMDI2LTEwLTAyVDEwOjMwOjAwWiJ9)
// @Success 200 {object} ListSavedItemsAPIResponse "A page of recent saved items, with meta.cursor.next_cursor null when there is no next page"
// @Failure 400 {object} swagger.ValidationErrorResponse "Invalid limit or cursor"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items [get]
func (h *Handler) List(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req ListSavedItemsRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.List(
		c.Context(),
		userID,
		req.Limit,
		req.Cursor,
	)
	if err != nil {
		return err
	}

	response := mapListSavedItemsResponse(result)

	meta := httpx.Meta{
		Cursor: &httpx.CursorPage{
			NextCursor: result.NextCursor,
			HasMore:    result.HasMore,
		},
	}

	return httpx.OKWithMeta(
		c,
		"saved items retrieved successfully",
		&response,
		meta,
	)
}

// Get godoc
// @Summary Get a saved item
// @Description Retrieve a saved item by ID, including the collection it belongs to.
// @Description The saved item is returned regardless of its enrichment status. A completed enrichment does not guarantee that all metadata fields are populated.
// @Description Possible error codes:
// @Description - BAD_REQUEST (non-UUID id)
// @Description - RESOURCE_NOT_FOUND
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Produce json
// @Security BearerAuth
// @Param id path string true "Saved item ID" format(uuid) example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Success 200 {object} GetSavedItemAPIResponse "The complete saved item and the id and name of the collection it is in"
// @Failure 400 {object} swagger.APIErrorResponse "Invalid saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found, or owned by another user"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items/{id} [get]
func (h *Handler) Get(c fiber.Ctx) error {
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

	result, err := h.service.Get(
		c.Context(),
		userID,
		savedItemID,
	)
	if err != nil {
		return err
	}

	response := mapGetSavedItemResponse(result)

	return httpx.OK(
		c,
		"saved item retrieved successfully",
		&response,
	)
}

// Delete godoc
// @Summary Delete a saved item
// @Description Delete a saved item by ID.
// @Description Deleting a saved item does not delete its collection. The response indicates whether the collection is now empty and whether it can be deleted.
// @Description The collection deletion eligibility is advisory and may change if the collection is modified by another request.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - RESOURCE_NOT_FOUND
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Produce json
// @Security BearerAuth
// @Param id path string true "Saved item ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Success 200 {object} DeleteSavedItemAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items/{id} [delete]
func (h *Handler) Delete(c fiber.Ctx) error {
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

	result, err := h.service.Delete(
		c.Context(),
		userID,
		savedItemID,
	)
	if err != nil {
		return err
	}

	response := mapDeleteSavedItemResponse(result)

	return httpx.OK(
		c,
		"saved item deleted successfully",
		&response,
	)
}
