package main

import (
	"os"
	"path/filepath"
)

// configDir returns the directory for all deco config/data files.
// Respects $XDG_CONFIG_HOME; defaults to ~/.config/deco.
func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "deco")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "deco")
	}
	return filepath.Join(home, ".config", "deco")
}

// ensureConfigDir creates the config directory if it doesn't exist.
func ensureConfigDir() error {
	return os.MkdirAll(configDir(), 0700)
}

// cfgPath returns the full path for a file inside the config directory.
func cfgPath(filename string) string {
	return filepath.Join(configDir(), filename)
}

// migrateIfNeeded moves legacy config/data files from the exe directory or
// cwd into the new config directory. Called once before loading config.
func migrateIfNeeded() {
	files := []string{"deco_config.json", "network_usage.db", "deco_aliases.json"}

	var legacyDirs []string
	if exe, err := os.Executable(); err == nil {
		legacyDirs = append(legacyDirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		legacyDirs = append(legacyDirs, cwd)
	}

	for _, f := range files {
		newPath := cfgPath(f)
		if _, err := os.Stat(newPath); err == nil {
			continue // already at new location
		}

		for _, dir := range legacyDirs {
			oldPath := filepath.Join(dir, f)
			if _, err := os.Stat(oldPath); err != nil {
				continue
			}
			if err := ensureConfigDir(); err != nil {
				logWarn("cannot create config dir: %v", err)
				return
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				logWarn("failed to migrate %s: %v", oldPath, err)
			} else {
				logInfo("migrated %s -> %s", oldPath, newPath)
			}
			break
		}
	}
}
