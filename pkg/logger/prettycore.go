package logger

import (
	"fmt"
	"os"

	"go.uber.org/zap/zapcore"
)

type prettyCore struct {
	zapcore.LevelEnabler
	fields []zapcore.Field
}

func (c *prettyCore) With(fields []zapcore.Field) zapcore.Core {
	newFields := make([]zapcore.Field, len(c.fields)+len(fields))
	copy(newFields, c.fields)
	copy(newFields[len(c.fields):], fields)

	return &prettyCore{
		LevelEnabler: c.LevelEnabler,
		fields:       newFields,
	}
}

func (c *prettyCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

func (c *prettyCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	allFields := append(c.fields, fields...)

	enc := zapcore.NewMapObjectEncoder()
	for _, f := range allFields {
		f.AddTo(enc)
	}

	ts := entry.Time.Format("15:04:05")
	level := colorLevel(entry.Level)
	caller := entry.Caller.TrimmedPath()

	line := fmt.Sprintf(
		"%s  %s  %s  %s\n",
		grey(ts),
		level,
		caller,
		entry.Message,
	)

	if len(enc.Fields) > 0 {
		line += "\n"

		maxLen := 0
		for k := range enc.Fields {
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}

		for k, v := range enc.Fields {
			line += fmt.Sprintf("  %s: %v\n",
				grey(fmt.Sprintf("%-*s", maxLen, k)),
				v,
			)
		}
	}

	line += "\n"

	_, err := os.Stdout.Write([]byte(line))
	return err
}

func (c *prettyCore) Sync() error { return nil }

func grey(s string) string   { return "\033[90m" + s + "\033[0m" }
func green(s string) string  { return "\033[32m" + s + "\033[0m" }
func yellow(s string) string { return "\033[33m" + s + "\033[0m" }
func red(s string) string    { return "\033[31m" + s + "\033[0m" }
func blue(s string) string   { return "\033[34m" + s + "\033[0m" }

func colorLevel(l zapcore.Level) string {
	switch l {
	case zapcore.DebugLevel:
		return blue("DEBUG")
	case zapcore.InfoLevel:
		return green("INFO ")
	case zapcore.WarnLevel:
		return yellow("WARN ")
	case zapcore.ErrorLevel:
		return red("ERROR")
	default:
		return "?????"
	}
}
