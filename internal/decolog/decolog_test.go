package decolog

import (
	"os"
	"strings"
	"testing"
)

func TestSetVerbose(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	logLevel.Store(int32(LevelWarn))
	SetVerbose(true)
	if logLevel.Load() != int32(LevelDebug) {
		t.Errorf("SetVerbose(true): logLevel = %d, want %d", logLevel.Load(), LevelDebug)
	}

	// SetVerbose(false) should not change level
	logLevel.Store(int32(LevelWarn))
	SetVerbose(false)
	if logLevel.Load() != int32(LevelWarn) {
		t.Errorf("SetVerbose(false): logLevel = %d, want %d", logLevel.Load(), LevelWarn)
	}
}

func TestLogLevels(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	// At default level (Warn), debug should be hidden
	logLevel.Store(int32(LevelWarn))
	Debug("hidden message")
	Warn("visible warning")

	w.Close()
	os.Stderr = oldStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if strings.Contains(output, "hidden message") {
		t.Error("debug message should be hidden at Warn level")
	}
	if !strings.Contains(output, "visible warning") {
		t.Error("warn message should be visible at Warn level")
	}

	// At debug level, debug should be visible
	r2, w2, _ := os.Pipe()
	os.Stderr = w2
	t.Cleanup(func() { os.Stderr = oldStderr })

	logLevel.Store(int32(LevelDebug))
	Debug("now visible")

	w2.Close()
	os.Stderr = oldStderr

	var buf2 [4096]byte
	n2, _ := r2.Read(buf2[:])
	output2 := string(buf2[:n2])

	if !strings.Contains(output2, "now visible") {
		t.Error("debug message should be visible at Debug level")
	}
}

func TestLogError(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	logLevel.Store(int32(LevelError))
	Warn("should be hidden")
	Error("should be visible")

	w.Close()
	os.Stderr = oldStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if strings.Contains(output, "should be hidden") {
		t.Error("warn should be hidden at Error level")
	}
	if !strings.Contains(output, "should be visible") {
		t.Error("error should be visible at Error level")
	}
}

func TestSetLevel(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	SetLevel(LevelInfo)
	if GetLevel() != LevelInfo {
		t.Errorf("GetLevel() = %d, want %d", GetLevel(), LevelInfo)
	}
}
