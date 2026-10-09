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
// @Description Save a URL to the current user's inbox. The domain is derived server-side from the submitted URL by lowercasing the host and removing a single leading "www."; the remote page is never fetched and no metadata extraction is performed. Platform and title are left null and are populated later by background enrichment.
// @Description The response is deliberately minimal: data.saved_item carries only id, url, collection_id and enrichment_status. A save commits before enrichment has run, so at this point the only metadata column with a value is domain, which was derived locally rather than read off the page. Reporting title, platform, description, image_url or last_enriched_at here would be a set of nulls presented as though they had been looked up, which reads as "this page has nothing" rather than "this page has not been read yet".
// @Description enrichment_status is the value the row was stored with, which on a fresh save is always "pending". Background enrichment is queued by the time this returns but has not run, so this endpoint never reports "completed". Read GET /saved-items/{id} for the complete item once enrichment has had a chance to finish.
// @Description The save itself is unchanged: a single INSERT that commits before enrichment is queued, and no response field here is obtained by an extra query.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_URL
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body CreateSavedItemRequest true "Save URL request"
// @Success 201 {object} CreateSavedItemAPIResponse "The saved item's id, url, collection_id and enrichment_status"
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error or invalid URL"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
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

// Get godoc
// @Summary Get a saved item
// @Description Get one of the current user's saved items by ID, with the collection it is filed in. A saved item that does not exist and one owned by another user both return the same not found response, so this endpoint never discloses whether an ID exists.
// @Description The response body is {"data": {...}, "message": "..."}, where data holds the item and its collection: {"saved_item": {...}, "collection": {"id": "...", "name": "..."}}. The collection object carries only id and name; type and system_key belong to GET /collections.
// @Description data.saved_item is the complete saved item, with the same fields as the listing in GET /collections/{id}/saved-items: id, url, domain, platform, title, description, image_url, collection_id, enrichment_status, last_enriched_at, created_at and updated_at. saved_item.collection_id always equals collection.id.
// @Description title, description, image_url, domain, platform and last_enriched_at are nullable and are reported as null rather than omitted. enrichment_status is pending, completed or failed and describes whether the enrichment process ran rather than whether every field is populated, so a completed item whose page exposed no title is reported completed with a null title. last_enriched_at is null until an enrichment has succeeded at least once, so a pending or failed item reports null.
// @Description An item whose enrichment has not run, or failed, is still returned in full. It exists and belongs to the caller, and "this page has not been read yet" is a fact about an item rather than a reason to withhold it.
// @Description This is a read. It starts no enrichment, changes nothing, and does not move the item between collections. The collection is read in the same statement as the item, so naming it costs no additional query.
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
// @Description Delete one of the current user's saved items by ID. A saved item
// that does not exist and one owned by another user both return the same not
// found response, so this endpoint never discloses whether an ID exists.
// @Description Deleting a saved item never deletes its collection. The collection
// the item was in is left exactly as it is, and the response reports what that
// delete left behind so the caller can decide what to do next.
// @Description The response returns the collection_id of the deleted saved item,
// whether that collection is now empty (collection_empty), and whether the user may
// delete it now (collection_deletable, which is collection_empty and the collection
// not being Unsorted).
// @Description These are advisory values, not a guarantee. Another request may add
// or remove a saved item immediately afterwards. Deleting a collection is a
// separate, explicit operation that re-reads the database and validates again.
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
