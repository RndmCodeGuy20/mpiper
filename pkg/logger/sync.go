package logger

import (
	"runtime"
	"strings"

	"go.uber.org/zap"
)

func Sync(l *zap.Logger) error {
	err := l.Sync()
	if err != nil {
		if runtime.GOOS == "windows" && strings.Contains(err.Error(), "The handle is invalid") {
			return nil
		}
	}
	return err
}
