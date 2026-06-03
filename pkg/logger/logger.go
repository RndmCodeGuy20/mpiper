package logger

import (
	"os"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/log/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func New(cfg Config) *zap.Logger {
	if cfg.Environment == "prod" || cfg.Environment == "production" {
		return newProdLogger(cfg)
	}
	return newDevLogger(cfg)
}

func newProdLogger(cfg Config) *zap.Logger {
	encoderCfg := zapcore.EncoderConfig{
		TimeKey:       "ts",
		LevelKey:      "level",
		MessageKey:    "msg",
		CallerKey:     "caller",
		StacktraceKey: "stacktrace",

		EncodeTime:   zapcore.ISO8601TimeEncoder,
		EncodeLevel:  zapcore.LowercaseLevelEncoder,
		EncodeCaller: zapcore.ShortCallerEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		cfg.Level,
	)
	core = withOTelCore(cfg, core)

	return zap.New(core,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	).With(
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
	)
}

func newDevLogger(cfg Config) *zap.Logger {
	core := &prettyCore{
		LevelEnabler: cfg.Level,
	}
	otelCore := withOTelCore(cfg, core)

	return zap.New(otelCore,
		zap.AddCaller(),
	).With(
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
	)
}

func withOTelCore(cfg Config, base zapcore.Core) zapcore.Core {
	if !cfg.EnableOTel {
		return base
	}

	otelCore := otelzap.NewCore(
		cfg.ServiceName,
		otelzap.WithLoggerProvider(global.GetLoggerProvider()),
	)

	filteredOTelCore, err := zapcore.NewIncreaseLevelCore(otelCore, cfg.Level)
	if err != nil {
		return base
	}

	return zapcore.NewTee(base, filteredOTelCore)
}
