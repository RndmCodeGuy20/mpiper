package logger

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func WithTrace(ctx context.Context, l *zap.Logger) *zap.Logger {
	span := trace.SpanFromContext(ctx)
	if span == nil {
		return l
	}

	sc := span.SpanContext()
	if !sc.IsValid() {
		return l
	}

	return l.With(
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	)
}
