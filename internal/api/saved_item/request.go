package saved_item

type CreateSavedItemRequest struct {
	URL string `json:"url" validate:"required,url,max=2048" example:"https://example.com/articles/1"`
}

type ListSavedItemsRequest struct {
	Page  int `query:"page" validate:"omitempty,min=1" example:"1"`
	Limit int `query:"limit" validate:"omitempty,min=1,max=50" example:"20"`
}
