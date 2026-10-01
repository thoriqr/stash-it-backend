package swagger

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code" example:"ERROR_CODE"`
	Message string `json:"message" example:"request failed"`
}

type ValidationErrorResponse struct {
	Error ValidationError `json:"error"`
}

type ValidationError struct {
	Code    string          `json:"code" example:"VALIDATION_ERROR"`
	Message string          `json:"message" example:"validation failed"`
	Fields  []APIErrorField `json:"fields"`
}

type APIErrorField struct {
	Field   string `json:"field" example:"field_name"`
	Message string `json:"message" example:"invalid value"`
}