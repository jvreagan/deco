package main

import (
	"fmt"
	"os"
	"time"
)

// LogLevel controls the minimum severity of log messages written to stderr.
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

var logLevel = LevelWarn // default: only warnings and errors

// SetVerbose enables debug-level logging when v is true.
func SetVerbose(v bool) {
	if v {
		logLevel = LevelDebug
	}
}

func logDebug(format string, args ...interface{}) { logAt(LevelDebug, "DBG", format, args...) }
func logInfo(format string, args ...interface{})  { logAt(LevelInfo, "INF", format, args...) }
func logWarn(format string, args ...interface{})   { logAt(LevelWarn, "WRN", format, args...) }
func logError(format string, args ...interface{})  { logAt(LevelError, "ERR", format, args...) }

func logAt(level LogLevel, prefix, format string, args ...interface{}) {
	if level < logLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), prefix, msg)
}
