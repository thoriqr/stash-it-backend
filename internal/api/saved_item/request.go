package saved_item

type CreateSavedItemRequest struct {
	URL string `json:"url" validate:"required,url,max=2048" example:"https://example.com/articles/1"`
}
