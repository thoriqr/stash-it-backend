package collection

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

// PutSavedItem godoc
// @Summary Put a saved item into a collection
// @Description File one of the current user's saved items into one of their user collections, identified by display name.
// @Description The collection is created if it does not exist yet. A user collection is never created empty, so this call always ends with the saved item inside it.
// @Description Names are matched case-insensitively and with surrounding whitespace ignored, and the stored name is trimmed.
// @Description This operation is idempotent. If the saved item is already in the target collection nothing is written, the saved item's updated_at is left unchanged, and already_in_collection is returned true.
// @Description A name already held by a system collection, such as "Unsorted", is reserved. It returns COLLECTION_NAME_RESERVED rather than filing the saved item into a system collection.
// @Description The saved item must belong to the authenticated user. A saved item that does not exist and one owned by another user both return the same not found response, so this endpoint never discloses whether an ID exists.
// @Description Possible error codes:
// @Description - INVALID_COLLECTION_NAME
// @Description - COLLECTION_NAME_RESERVED
// @Description - BAD_REQUEST
// @Description - SAVED_ITEM_NOT_FOUND
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Collection
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Saved item ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Param request body PutSavedItemIntoCollectionRequest true "Put saved item into collection request"
// @Success 200 {object} PutSavedItemIntoCollectionAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid collection name or saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
// @Failure 409 {object} swagger.APIErrorResponse "Collection name is reserved"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /saved-items/{id}/collection [put]
func (h *Handler) PutSavedItem(c fiber.Ctx) error {
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

	var req PutSavedItemIntoCollectionRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.PutSavedItem(
		c.Context(),
		userID,
		savedItemID,
		req.CollectionName,
	)
	if err != nil {
		return err
	}

	response := mapPutSavedItemIntoCollectionResponse(result)

	// 200 rather than 201: the call succeeds whether or not it created anything,
	// and the response reports which happened through collection_created. A 201
	// would assert a creation that most repeat calls do not perform.
	return httpx.OK(
		c,
		"saved item moved into collection successfully",
		&response,
	)
}
