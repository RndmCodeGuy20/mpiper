package logger

import (
	"context"

	"go.uber.org/zap"
)

type contextKey struct{}

func WithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

func FromContext(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(contextKey{}).(*zap.Logger); ok {
		return l
	}
	return zap.L()
}
