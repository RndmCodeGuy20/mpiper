package utils

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	grey  = "\033[90m"
	reset = "\033[0m"
)

// LoggerConfig holds all configuration for the logger
type LoggerConfig struct {
	// LogLevel: DEBUG, INFO, WARN, ERROR, FATAL
	LogLevel zapcore.Level
	// EnableCaller adds caller information to logs
	EnableCaller bool
	// EnableStacktrace adds stacktraces for errors
	EnableStacktrace bool
	// StacktraceLevel is the minimum level for stacktraces
	StacktraceLevel zapcore.Level
	// TimeFormat for log timestamps (e.g., "15:04:05", "2006-01-02 15:04:05")
	TimeFormat string
	// PrintFieldsBelow prints fields in JSON format below the log message
	PrintFieldsBelow bool
	// UseColors enables colored output
	UseColors bool
	// UseJSON outputs logs in JSON format instead of console
	UseJSON bool
}

// DefaultConfig returns sensible defaults
func DefaultConfig() LoggerConfig {
	return LoggerConfig{
		LogLevel:         getLogLevelFromEnv(),
		EnableCaller:     false,
		EnableStacktrace: false,
		StacktraceLevel:  zapcore.ErrorLevel,
		TimeFormat:       "2006-01-02 15:04:05.000",
		PrintFieldsBelow: true, // Changed to true by default
		UseColors:        true,
		UseJSON:          false,
	}
}

// Logger wraps zap.Logger with convenience methods
type Logger struct {
	*zap.Logger
}

// NewLogger creates a new logger with default configuration
func NewLogger() *Logger {
	return NewLoggerWithConfig(DefaultConfig())
}

// NewLoggerWithConfig creates a logger with custom configuration
func NewLoggerWithConfig(cfg LoggerConfig) *Logger {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeTime:     getTimeEncoder(cfg.TimeFormat),
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if cfg.UseJSON {
		encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoderConfig.ConsoleSeparator = "    "
		if cfg.UseColors {
			encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		} else {
			encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		}
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Wrap encoder if PrintFieldsBelow is enabled
	if cfg.PrintFieldsBelow && !cfg.UseJSON {
		encoder = &fieldEncoder{
			Encoder:     encoder,
			printFields: true,
		}
	}

	core := zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), cfg.LogLevel)

	// Apply field filtering if needed
	if cfg.PrintFieldsBelow && !cfg.UseJSON {
		core = &fieldFilterCore{
			Core:          core,
			encoder:       encoder.(*fieldEncoder),
			output:        zapcore.Lock(os.Stdout),
			contextFields: []zapcore.Field{},
		}
	}

	// Build options
	var options []zap.Option
	if cfg.EnableCaller {
		options = append(options, zap.AddCaller())
	}
	if cfg.EnableStacktrace {
		options = append(options, zap.AddStacktrace(cfg.StacktraceLevel))
	}

	return &Logger{
		Logger: zap.New(core, options...),
	}
}

// fieldEncoder wraps an encoder to print fields separately
type fieldEncoder struct {
	zapcore.Encoder
	printFields bool
}

func (e *fieldEncoder) Clone() zapcore.Encoder {
	return &fieldEncoder{
		Encoder:     e.Encoder.Clone(),
		printFields: e.printFields,
	}
}

// fieldFilterCore intercepts Write calls to separate fields from main message
type fieldFilterCore struct {
	zapcore.Core
	encoder       *fieldEncoder
	output        zapcore.WriteSyncer
	contextFields []zapcore.Field
}

func (c *fieldFilterCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Encode the entry WITHOUT fields for the main line
	buf, err := c.encoder.Encoder.EncodeEntry(entry, nil)
	if err != nil {
		return err
	}

	// Combine context fields and log-time fields
	if c.encoder.printFields {
		allFields := append(c.contextFields, fields...)

		if len(allFields) > 0 {
			enc := zapcore.NewMapObjectEncoder()
			for _, f := range allFields {
				f.AddTo(enc)
			}

			if len(enc.Fields) > 0 {
				if jsonBytes, marshalErr := json.MarshalIndent(enc.Fields, "", "  "); marshalErr == nil {
					buf.AppendString(grey + string(jsonBytes) + reset + "\n")
				}
			}
		}
	}

	_, err = c.output.Write(buf.Bytes())
	buf.Free()
	return err
}

func (c *fieldFilterCore) With(fields []zapcore.Field) zapcore.Core {
	// Clone and append new context fields
	newContextFields := make([]zapcore.Field, len(c.contextFields)+len(fields))
	copy(newContextFields, c.contextFields)
	copy(newContextFields[len(c.contextFields):], fields)

	return &fieldFilterCore{
		Core:          c.Core.With(fields),
		encoder:       c.encoder,
		output:        c.output,
		contextFields: newContextFields,
	}
}

func (c *fieldFilterCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

// getTimeEncoder returns a time encoder based on format string
func getTimeEncoder(format string) zapcore.TimeEncoder {
	if format == "" {
		return zapcore.ISO8601TimeEncoder
	}
	return func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.Format(format))
	}
}

// getLogLevelFromEnv reads log level from LOG_LEVEL environment variable
func getLogLevelFromEnv() zapcore.Level {
	level := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch level {
	case "DEBUG":
		return zapcore.DebugLevel
	case "INFO":
		return zapcore.InfoLevel
	case "WARN", "WARNING":
		return zapcore.WarnLevel
	case "ERROR":
		return zapcore.ErrorLevel
	case "FATAL":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// Convenience methods

// WithField adds a single field to the logger context
func (l *Logger) WithField(key string, value interface{}) *Logger {
	return &Logger{Logger: l.Logger.With(zap.Any(key, value))}
}

// WithFields adds multiple fields to the logger context
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	zapFields := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}
	return &Logger{Logger: l.Logger.With(zapFields...)}
}

// WithError adds an error field to the logger context
func (l *Logger) WithError(err error) *Logger {
	return &Logger{Logger: l.Logger.With(zap.Error(err))}
}

// Close syncs the logger (should be called before application exit)
func (l *Logger) Close() error {
	if err := l.Logger.Sync(); err != nil {
		// On Windows syncing stdout/stderr can return "The handle is invalid" — ignore it.
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "The handle is invalid") {
			return nil
		}
		return err
	}
	return nil
}

func CloseLogger(l *zap.Logger) error {
	if err := l.Sync(); err != nil {
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "The handle is invalid") {
			return nil
		}
		return err
	}
	return nil
}
