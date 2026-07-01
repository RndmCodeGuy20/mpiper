package utils

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rndmcodeguy20/mpiper/pkg/errors"
)

func RespondJSON(w http.ResponseWriter, v interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func ParseJSON(body io.ReadCloser, v interface{}) error {
	return json.NewDecoder(body).Decode(v)
}

func WriteErrorResponse(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")

	var apiErr *errors.ApiError

	switch e := err.(type) {
	case *errors.NotFoundError:
		apiErr = e.ApiError
	case *errors.BadRequestError:
		apiErr = e.ApiError
	case *errors.InternalServerErrorError:
		apiErr = e.ApiError
	case *errors.UnauthorizedError:
		apiErr = e.ApiError
	case *errors.ConflictError:
		apiErr = e.ApiError
	case *errors.UnprocessableEntityError:
		apiErr = e.ApiError
	case *errors.ForbiddenError:
		apiErr = e.ApiError
	case *errors.TooManyRequestsError:
		apiErr = e.ApiError
	default:
		apiErr = &errors.ApiError{
			Message:    "Internal server error",
			Code:       "INTERNAL_ERROR",
			StatusCode: http.StatusInternalServerError,
		}
	}
	w.WriteHeader(apiErr.StatusCode)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"message": apiErr.Message,
		"error": map[string]interface{}{
			"code":    apiErr.Code,
			"details": apiErr.Details,
		},
	})
}
