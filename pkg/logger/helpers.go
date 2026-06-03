package logger

import "go.uber.org/zap"

func WithRequestID(l *zap.Logger, requestID string) *zap.Logger {
	return l.With(zap.String("request_id", requestID))
}
