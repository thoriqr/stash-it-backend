package saved_item

import (
	"time"

	"github.com/google/uuid"
)

type SavedItemResponse struct {
	ID        uuid.UUID  `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	URL       string     `json:"url" example:"https://example.com/articles/1"`
	Domain    *string    `json:"domain" example:"example.com"`
	Platform  *string    `json:"platform" example:"web"`
	Title     *string    `json:"title" example:"An interesting article"`
	CreatedAt time.Time  `json:"created_at" example:"2026-10-02T10:30:00Z"`
	UpdatedAt time.Time  `json:"updated_at" example:"2026-10-02T10:30:00Z"`
}

type CreateSavedItemResponse struct {
	SavedItem SavedItemResponse `json:"saved_item"`
}

type GetSavedItemResponse struct {
	SavedItem SavedItemResponse `json:"saved_item"`
}

type DeleteSavedItemAPIResponse struct {
	Data    *struct{} `json:"data"`
	Message string    `json:"message" example:"saved item deleted successfully"`
}

type ListSavedItemsResponse struct {
	SavedItems []SavedItemResponse `json:"saved_items"`
}

type CreateSavedItemAPIResponse struct {
	Data    *CreateSavedItemResponse `json:"data"`
	Message string                   `json:"message" example:"saved item created successfully"`
}

type GetSavedItemAPIResponse struct {
	Data    *GetSavedItemResponse `json:"data"`
	Message string                `json:"message" example:"saved item retrieved successfully"`
}

type ListSavedItemsAPIResponse struct {
	Data    *ListSavedItemsResponse `json:"data"`
	Message string                  `json:"message" example:"saved items retrieved successfully"`
	Meta    ListSavedItemsMeta      `json:"meta"`
}

type ListSavedItemsMeta struct {
	Pagination ListSavedItemsPagination `json:"pagination"`
}

type ListSavedItemsPagination struct {
	Page       int   `json:"page" example:"1"`
	Limit      int   `json:"limit" example:"20"`
	Total      int64 `json:"total" example:"42"`
	TotalPages int   `json:"total_pages" example:"3"`
}
