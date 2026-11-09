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
// @Description Add a saved item to one of the user's collections.
// @Description Any collection owned by the user can be used as a destination, including system and automatically created collections.
// @Description collection_intent determines how the collection is selected. "existing" selects a collection by name or creates one if it does not exist. "create" creates a new collection and returns a conflict if the name is already in use.
// @Description Possible error codes:
// @Description - INVALID_COLLECTION_NAME
// @Description - INVALID_COLLECTION_INTENT
// @Description - BAD_REQUEST
// @Description - SAVED_ITEM_NOT_FOUND
// @Description - COLLECTION_NAME_ALREADY_EXISTS
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
// @Failure 400 {object} swagger.APIErrorResponse "Invalid collection name or intent, or saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
// @Failure 409 {object} swagger.APIErrorResponse "Collection name already exists"
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
		req.CollectionIntent,
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

// ListCollections godoc
// @Summary List collections
// @Description Retrieve the authenticated user's collections using cursor-based pagination.
// @Description The Unsorted collection is always returned first, regardless of sort order, and is identified by its system_key ("unsorted").
// @Description Collections can be sorted by creation time (newest or oldest) or alphabetically by name. The default sort is "newest".
// @Description saved_items_count represents the number of saved items in a collection. Counts are exact up to 99; when saved_items_count_overflow is true, the count represents 99 or more.
// @Description Pass meta.cursor.next_cursor from the previous response to retrieve the next page. A null next_cursor indicates the final page.
// @Description Possible error codes:
// @Description - INVALID_COLLECTION_SORT
// @Description - INVALID_CURSOR
// @Description - VALIDATION_ERROR
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Collection
// @Produce json
// @Security BearerAuth
// @Param sort query string false "Collection order" Enums(newest, oldest, name) default(newest) example(newest)
// @Param limit query int false "Number of collections per page. Defaults to 20, capped at 50." minimum(1) maximum(50) default(20) example(20)
// @Param cursor query string false "Opaque cursor from meta.cursor.next_cursor of the previous page. Omit to start at the beginning." maxLength(512) example(eyJ2IjoxLCJzIjoibmV3ZXN0In0)
// @Success 200 {object} ListCollectionsAPIResponse "One page of collections, with meta.cursor.next_cursor null when there is no next page"
// @Failure 400 {object} swagger.ValidationErrorResponse "Invalid sort, limit or cursor"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /collections [get]
func (h *Handler) ListCollections(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req ListCollectionsRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.ListCollections(
		c.Context(),
		userID,
		req.Sort,
		req.Limit,
		req.Cursor,
	)
	if err != nil {
		return err
	}

	response := mapListCollectionsResponse(result)

	meta := httpx.Meta{
		Cursor: &httpx.CursorPage{
			NextCursor: result.NextCursor,
			HasMore:    result.HasMore,
		},
	}

	return httpx.OKWithMeta(
		c,
		"collections retrieved successfully",
		&response,
		meta,
	)
}

// ListSavedItemsInCollection godoc
// @Summary List a collection's saved items
// @Description Retrieve a collection and its saved items, ordered by newest first.
// @Description The response includes collection details even when the collection contains no saved items.
// @Description Saved items are returned regardless of their enrichment status. A completed enrichment does not guarantee that all metadata fields are populated; nullable fields may still be null. Each item includes its optional note, which is null when the user has not set one.
// @Description Pass meta.cursor.next_cursor from the previous response to retrieve the next page. A null next_cursor indicates the final page.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - INVALID_CURSOR
// @Description - VALIDATION_ERROR
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - COLLECTION_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Collection
// @Produce json
// @Security BearerAuth
// @Param id path string true "Collection ID" format(uuid) example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Param limit query int false "Number of saved items per page. Defaults to 20, capped at 50." minimum(1) maximum(50) default(20) example(20)
// @Param cursor query string false "Opaque cursor from meta.cursor.next_cursor of the previous page. Omit to start at the newest item." maxLength(512) example(eyJ2IjoxLCJ2YWwiOiIyMDI2LTEwLTAyVDEwOjMwOjAwWiJ9)
// @Success 200 {object} ListSavedItemsInCollectionAPIResponse "A page of the collection's saved items, with meta.cursor.next_cursor null when there is no next page"
// @Failure 400 {object} swagger.ValidationErrorResponse "Invalid limit, cursor or collection id"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Collection not found, or owned by another user"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /collections/{id}/saved-items [get]
func (h *Handler) ListSavedItemsInCollection(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	collectionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid collection id",
			err,
		)
	}

	var req ListSavedItemsInCollectionRequest

	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.ListSavedItemsInCollection(
		c.Context(),
		userID,
		collectionID,
		req.Limit,
		req.Cursor,
	)
	if err != nil {
		return err
	}

	response := mapListSavedItemsInCollectionResponse(result)

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

// DeleteCollection godoc
// @Summary Delete a collection
// @Description Delete one of the user's collections.
// @Description saved_items_action determines what happens to the saved items in the collection. "delete" deletes the saved items, while "move" moves them to the collection specified by target_collection_id before deleting the collection.
// @Description target_collection_id must be provided when saved_items_action is "move" and must be null when it is "delete". Unsorted can be used as a move destination.
// @Description The Unsorted collection cannot be deleted.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - INVALID_COLLECTION_DELETE_ACTION
// @Description - INVALID_COLLECTION_DELETE_TARGET
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - COLLECTION_NOT_FOUND
// @Description - COLLECTION_DELETE_TARGET_NOT_FOUND
// @Description - UNSORTED_COLLECTION_PROTECTED
// @Description - COLLECTION_NOT_EMPTY
// @Description - INTERNAL_SERVER_ERROR
// @Tags Collection
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Collection ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Param request body DeleteCollectionRequest true "Delete collection request"
// @Success 200 {object} DeleteCollectionAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid collection ID, action or target"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Collection or target collection not found"
// @Failure 409 {object} swagger.APIErrorResponse "Unsorted collection, or collection is not empty"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /collections/{id} [delete]
func (h *Handler) DeleteCollection(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	collectionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid collection id",
			err,
		)
	}

	var req DeleteCollectionRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	params := DeleteCollectionParams{
		UserID:       userID,
		CollectionID: collectionID,
		Action:       req.SavedItemsAction,
	}

	// The pointer is the difference between "no target" and a target. Nil means the
	// caller said nothing, which is what the action rules are checked against;
	// collapsing it to uuid.Nil here would lose that distinction before the service
	// could see it.
	if req.TargetCollectionID != nil {
		params.TargetCollectionID = *req.TargetCollectionID
	}

	if err := h.service.DeleteCollection(
		c.Context(),
		params,
	); err != nil {
		return err
	}

	return httpx.OKMessage(
		c,
		"collection deleted successfully",
	)
}
