package decolog

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Level controls the minimum severity of log messages written to stderr.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var logLevel atomic.Int32

func init() {
	logLevel.Store(int32(LevelWarn)) // default: only warnings and errors
}

// SetVerbose enables debug-level logging when v is true.
func SetVerbose(v bool) {
	if v {
		logLevel.Store(int32(LevelDebug))
	}
}

// SetLevel sets the log level directly.
func SetLevel(l Level) {
	logLevel.Store(int32(l))
}

// GetLevel returns the current log level.
func GetLevel() Level {
	return Level(logLevel.Load())
}

func Debug(format string, args ...interface{}) { logAt(LevelDebug, "DBG", format, args...) }
func Info(format string, args ...interface{})  { logAt(LevelInfo, "INF", format, args...) }
func Warn(format string, args ...interface{})  { logAt(LevelWarn, "WRN", format, args...) }
func Error(format string, args ...interface{}) { logAt(LevelError, "ERR", format, args...) }

func logAt(level Level, prefix, format string, args ...interface{}) {
	if int32(level) < logLevel.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), prefix, msg)
}
