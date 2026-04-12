package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	got := ConfigDir()
	want := filepath.Join(tmpDir, "deco")
	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDirDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got := ConfigDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "deco")
	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestCfgPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	got := CfgPath("foo.json")
	want := filepath.Join(tmpDir, "deco", "foo.json")
	if got != want {
		t.Errorf("CfgPath(\"foo.json\") = %q, want %q", got, want)
	}
}

func TestMigrateIfNeeded(t *testing.T) {
	// Create a temp dir to serve as XDG_CONFIG_HOME
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create a legacy file in a "legacy" directory simulating cwd
	legacyDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(legacyDir)
	defer os.Chdir(origDir)

	os.WriteFile("deco_config.json", []byte(`{"host":"1.2.3.4"}`), 0600)

	MigrateIfNeeded()

	// Verify file moved to new location
	newPath := CfgPath("deco_config.json")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected file at %s after migration", newPath)
	}
	// Verify old file is gone
	if _, err := os.Stat("deco_config.json"); !os.IsNotExist(err) {
		t.Error("legacy file should be removed after migration")
	}
}
