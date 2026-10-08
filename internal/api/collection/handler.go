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

// ListCollections godoc
// @Summary List collections
// @Description Get the authenticated user's collections, one page at a time.
// @Description The Unsorted collection is always the first result, under every sort, and never appears again on a later page. It is reported by its system_key, which is "unsorted"; the display name is not its identity and a collection's type never decides anything about it.
// @Description Collections automatic organization created appear here exactly like collections the user named themselves, because a collection belongs to one user either way.
// @Description sort selects the order and defaults to "newest". "newest" and "oldest" order by when the collection was created; "name" orders alphabetically, comparing names case-insensitively and ignoring surrounding whitespace, which is the same comparison collection names are stored under. Every order is broken by id so collections created in the same instant keep a stable order.
// @Description Pages are returned by cursor. Pass the next_cursor from one response as the cursor of the next request to continue where that page ended; omit it to start from the beginning. A cursor records a position rather than a row, so it keeps working even if the collection it came from has since been deleted.
// @Description meta.cursor.next_cursor is explicitly null on the final page, where has_more is false. There is deliberately no total count: a cursor-paginated response does not know how many collections exist overall.
// @Description Search is not available here. GET /search already searches collections by name.
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
// @Param limit query int false "Number of collections per page" minimum(1) maximum(50) default(20) example(20)
// @Param cursor query string false "Cursor from the previous page" example(eyJ2IjoxLCJzIjoibmV3ZXN0In0)
// @Success 200 {object} ListCollectionsAPIResponse
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

// DeleteCollection godoc
// @Summary Delete a collection
// @Description Delete one of the current user's collections by ID. The request must
// state what happens to the saved items currently filed in that collection, because
// a collection cannot be removed while saved items still reference it.
// @Description saved_items_action is required and must be "delete" or "move".
// @Description "delete" deletes those saved items along with the collection. "move"
// files them into target_collection_id first and then deletes the collection. The
// field names and their meanings never change with the action; which combinations
// are valid is stated here and enforced by the backend.
// @Description target_collection_id must be null when saved_items_action is
// "delete" and must be a collection ID when it is "move". It names an ordinary
// collection: Unsorted is a valid target and is referred to by its ID like any
// other, and there is no separate mode for it and no fallback to it. A target that
// does not exist or belongs to another user is an error, never a substitution.
// @Description Unsorted itself cannot be deleted. It is identified by its
// system_key, which is a stable identity; neither its display name nor its type
// decides this. A collection's type does not restrict deletion either: a collection
// created automatically is one the user may delete exactly like one they named
// themselves.
// @Description The whole operation is atomic. The saved items are deleted or moved
// and the collection is removed together, or nothing changes at all. If a saved item
// is filed into the collection while the operation runs, the database relationship
// protects it, the operation fails with COLLECTION_NOT_EMPTY, and the collection and
// its saved items are left as they were.
// @Description The collection must belong to the authenticated user. A collection
// that does not exist and one owned by another user both return the same not found
// response, so this endpoint never discloses whether an ID exists.
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
