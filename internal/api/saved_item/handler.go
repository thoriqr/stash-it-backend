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
// @Description Save a URL to the current user's inbox. The domain is derived
// server-side from the submitted URL by lowercasing the host and removing a
// single leading "www."; the remote page is never fetched and no metadata
// extraction is performed. Platform and title are left null and are populated
// later by background enrichment.
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
// @Success 201 {object} CreateSavedItemAPIResponse
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
// @Description Get one of the current user's saved items by ID. A saved item
// that does not exist and one owned by another user both return the same
// not found response, so this endpoint never discloses whether an ID exists.
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
// @Success 200 {object} GetSavedItemAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid saved item ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Saved item not found"
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

	if err := h.service.Delete(
		c.Context(),
		userID,
		savedItemID,
	); err != nil {
		return err
	}

	return httpx.OKMessage(
		c,
		"saved item deleted successfully",
	)
}

// List godoc
// @Summary List saved items
// @Description Get the current user's saved items, newest first.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags SavedItem
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number" minimum(1) default(1) example(1)
// @Param limit query int false "Number of saved items per page" minimum(1) maximum(50) default(20) example(20)
// @Success 200 {object} ListSavedItemsAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
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
		req.Page,
		req.Limit,
	)
	if err != nil {
		return err
	}

	response := mapListSavedItemsResponse(result)

	meta := httpx.Meta{
		Pagination: &httpx.Pagination{
			Page:       result.Page,
			Limit:      result.Limit,
			Total:      result.Total,
			TotalPages: result.TotalPages,
		},
	}

	return httpx.OKWithMeta(
		c,
		"saved items retrieved successfully",
		&response,
		meta,
	)
}
