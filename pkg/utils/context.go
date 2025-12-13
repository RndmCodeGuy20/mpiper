package utils

import (
	"context"
	"time"
)

func GetTimeoutContext(parentCtx context.Context, seconds int) (context.Context, func()) {
	return context.WithTimeout(parentCtx, time.Duration(seconds)*time.Second)
}
