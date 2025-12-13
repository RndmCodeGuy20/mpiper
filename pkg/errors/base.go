package errors

type Error interface {
	error
	CodeString() string
	Unwrap() error
	WithMetadata(key string, value interface{}) *BaseError
}

type BaseError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	Cause   error                  `json:"-"`
}

func (e *BaseError) Error() string {

	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}

	return e.Message
}

func (e *BaseError) CodeString() string {
	return e.Code
}

func (e *BaseError) Unwrap() error {
	return e.Cause
}

func (e *BaseError) WithMetadata(key string, value interface{}) *BaseError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

func (e *BaseError) Metadata() map[string]interface{} {
	return e.Details
}
