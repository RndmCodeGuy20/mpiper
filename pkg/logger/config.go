package logger

import "go.uber.org/zap/zapcore"

type Config struct {
	ServiceName string
	Environment string // "dev" | "prod"

	Level zapcore.Level

	// EnableOTel wires logs into the global OTel log provider (optional).
	EnableOTel bool
}

// ParseLevel converts a log-level string (case-insensitive) to zapcore.Level.
// Unknown strings default to InfoLevel.
func ParseLevel(s string) zapcore.Level {
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return zapcore.InfoLevel
	}
	return l
}
