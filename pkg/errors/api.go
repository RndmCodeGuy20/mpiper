package errors

import "net/http"

type ApiError struct {
	Message    string      `json:"message"`
	Code       string      `json:"code"`
	Details    interface{} `json:"details,omitempty"`
	StatusCode int         `json:"-"`
}

func (e *ApiError) Error() string {
	return e.Message
}

type NotFoundError struct{ *ApiError }
type BadRequestError struct{ *ApiError }
type InternalServerErrorError struct{ *ApiError }
type UnauthorizedError struct{ *ApiError }
type ConflictError struct{ *ApiError }
type UnprocessableEntityError struct{ *ApiError }
type ForbiddenError struct{ *ApiError }
type TooManyRequestsError struct{ *ApiError }

func NewNotFoundError(message string, cause error) *NotFoundError {
	return &NotFoundError{&ApiError{
		Message:    message,
		Code:       "NOT_FOUND_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusNotFound,
	}}
}

func NewBadRequestError(message string, cause error) *BadRequestError {
	return &BadRequestError{&ApiError{
		Message:    message,
		Code:       "BAD_REQUEST_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusBadRequest,
	}}
}

func NewInternalServerError(message string, cause error) *InternalServerErrorError {
	return &InternalServerErrorError{&ApiError{
		Message:    message,
		Code:       "INTERNAL_SERVER_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusInternalServerError,
	}}
}

func NewUnauthorizedError(message string, cause error) *UnauthorizedError {
	return &UnauthorizedError{&ApiError{
		Message:    message,
		Code:       "UNAUTHORIZED_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusUnauthorized,
	}}
}

func NewConflictError(message string, cause error) *ConflictError {
	return &ConflictError{&ApiError{
		Message:    message,
		Code:       "CONFLICT_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusConflict,
	}}
}

func NewUnprocessableEntityError(message string, cause error) *UnprocessableEntityError {
	return &UnprocessableEntityError{&ApiError{
		Message:    message,
		Code:       "UNPROCESSABLE_ENTITY_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusUnprocessableEntity,
	}}
}

func NewForbiddenError(message string, cause error) *ForbiddenError {
	return &ForbiddenError{&ApiError{
		Message:    message,
		Code:       "FORBIDDEN_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusForbidden,
	}}
}

func NewTooManyRequestsError(message string, cause error) *TooManyRequestsError {
	return &TooManyRequestsError{&ApiError{
		Message:    message,
		Code:       "TOO_MANY_REQUESTS_ERROR",
		Details:    unwrapErrorDetails(cause),
		StatusCode: http.StatusTooManyRequests,
	}}
}

func unwrapErrorDetails(err error) interface{} {
	if err == nil {
		return nil
	}
	return err.Error()
}
