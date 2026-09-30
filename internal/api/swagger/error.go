package swagger

// @name ErrorResponse
type APIErrorResponse struct {
	Error APIError `json:"error"`
}

// @name Error
type APIError struct {
	Code    string `json:"code" example:"ERROR_CODE"`
	Message string `json:"message" example:"request failed"`
}

// @name ValidationErrorResponse
type ValidationErrorResponse struct {
	Error ValidationError `json:"error"`
}

// @name ValidationError
type ValidationError struct {
	Code    string          `json:"code" example:"VALIDATION_ERROR"`
	Message string          `json:"message" example:"validation failed"`
	Fields  []APIErrorField `json:"fields"`
}

// @name ErrorField
type APIErrorField struct {
	Field   string `json:"field" example:"email"`
	Message string `json:"message" example:"invalid email"`
}