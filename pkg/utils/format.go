package utils

import "fmt"

func Format(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
