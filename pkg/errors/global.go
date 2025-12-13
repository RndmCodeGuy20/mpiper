package errors

type ConfigError struct{ *BaseError }

func NewConfigError(message string, cause error) *ConfigError {
	return &ConfigError{&BaseError{
		Message: message,
		Code:    "CONFIG_ERROR",
		Cause:   cause,
	}}
}
