package paths

import (
	"os"
	"path/filepath"

	"github.com/jvreagan/deco/internal/decolog"
)

// ConfigDir returns the directory for all deco config/data files.
// Respects $XDG_CONFIG_HOME; defaults to ~/.config/deco.
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "deco")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "deco")
	}
	return filepath.Join(home, ".config", "deco")
}

// EnsureConfigDir creates the config directory if it doesn't exist.
func EnsureConfigDir() error {
	return os.MkdirAll(ConfigDir(), 0700)
}

// CfgPath returns the full path for a file inside the config directory.
func CfgPath(filename string) string {
	return filepath.Join(ConfigDir(), filename)
}

// MigrateIfNeeded moves legacy config/data files from the exe directory or
// cwd into the new config directory. Called once before loading config.
// Each file is migrated independently, so a crash mid-migration is safe:
// the next run resumes where it left off. Config is migrated first (needed
// to connect), then aliases, then the database (largest, least critical).
func MigrateIfNeeded() {
	files := []string{"deco_config.json", "deco_aliases.json", "network_usage.db"}

	var legacyDirs []string
	if exe, err := os.Executable(); err == nil {
		legacyDirs = append(legacyDirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		legacyDirs = append(legacyDirs, cwd)
	}

	for _, f := range files {
		newPath := CfgPath(f)
		if _, err := os.Stat(newPath); err == nil {
			continue // already at new location
		}

		for _, dir := range legacyDirs {
			oldPath := filepath.Join(dir, f)
			if _, err := os.Stat(oldPath); err != nil {
				continue
			}
			if err := EnsureConfigDir(); err != nil {
				decolog.Warn("cannot create config dir: %v", err)
				return
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				decolog.Warn("failed to migrate %s: %v", oldPath, err)
			} else {
				decolog.Info("migrated %s -> %s", oldPath, newPath)
			}
			break
		}
	}
}
