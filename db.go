package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// Database constants
const (
	DBSizeLimitBytes = 1 * 1024 * 1024 * 1024 // 1 GB
)

var dbPath string

func init() {
	dbPath = cfgPath("network_usage.db")
}

// setDBPath overrides the database path (used in tests).
func setDBPath(path string) {
	dbPath = path
}

// currentSchemaVersion is the latest migration version. Bump this and add a
// new case in runMigrations whenever the schema changes.
const currentSchemaVersion = 1

// migration is a function that upgrades the database from version-1 to version.
type migration struct {
	version int
	sql     string
}

// migrations is an ordered list of schema migrations.
// Version 1 is the initial schema. Future changes add new entries.
var migrations = []migration{
	{version: 1, sql: `
		CREATE TABLE IF NOT EXISTS bandwidth_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			mac TEXT NOT NULL,
			name TEXT,
			ip TEXT,
			connection TEXT,
			device_type TEXT,
			download_kbps INTEGER DEFAULT 0,
			upload_kbps INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_timestamp ON bandwidth_samples(timestamp);
		CREATE INDEX IF NOT EXISTS idx_mac ON bandwidth_samples(mac);
		CREATE INDEX IF NOT EXISTS idx_bandwidth_mac_ts ON bandwidth_samples(mac, timestamp);

		CREATE TABLE IF NOT EXISTS network_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			wan_ip TEXT,
			wan_gateway TEXT,
			wan_dns1 TEXT,
			wan_dns2 TEXT,
			lan_ip TEXT,
			lan_netmask TEXT,
			cpu_percent REAL,
			mem_percent REAL
		);
		CREATE INDEX IF NOT EXISTS idx_net_timestamp ON network_snapshots(timestamp);

		CREATE TABLE IF NOT EXISTS mesh_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			name TEXT,
			role TEXT,
			ip TEXT,
			mac TEXT,
			model TEXT,
			firmware TEXT,
			status TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_mesh_timestamp ON mesh_snapshots(timestamp);

		CREATE TABLE IF NOT EXISTS wireless_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			band TEXT,
			ssid TEXT,
			channel TEXT,
			channel_width TEXT,
			host_enabled INTEGER,
			guest_enabled INTEGER,
			guest_ssid TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_wireless_timestamp ON wireless_snapshots(timestamp);
	`},
	// Future migrations go here:
	// {version: 2, sql: `ALTER TABLE bandwidth_samples ADD COLUMN ...;`},
}

func getSchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

func setSchemaVersion(db *sql.DB, version int) error {
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}

func runMigrations(db *sql.DB) error {
	current, err := getSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("reading schema version: %v", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		logDebug("running migration to schema version %d", m.version)
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("migration to version %d failed: %v", m.version, err)
		}
		if err := setSchemaVersion(db, m.version); err != nil {
			return fmt.Errorf("setting schema version %d: %v", m.version, err)
		}
	}
	return nil
}

func initDB() (*sql.DB, error) {
	if err := ensureConfigDir(); err != nil {
		return nil, fmt.Errorf("cannot create config directory: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrent read/write performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %v", err)
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %v", err)
	}

	return db, nil
}

func getDBSize() int64 {
	info, err := os.Stat(dbPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

// dbSizeCheckThrottleSec controls how often checkDBSizeLimit re-stats the file.
const dbSizeCheckThrottleSec = 60

var (
	lastDBSizeCheckTime time.Time
	lastDBSizeCheckOK   bool
	lastDBSizeCheckSize int64
)

func checkDBSizeLimit() (bool, int64) {
	now := time.Now()
	if !lastDBSizeCheckTime.IsZero() && now.Sub(lastDBSizeCheckTime).Seconds() < float64(dbSizeCheckThrottleSec) {
		return lastDBSizeCheckOK, lastDBSizeCheckSize
	}
	size := getDBSize()
	lastDBSizeCheckTime = now
	lastDBSizeCheckOK = size < DBSizeLimitBytes
	lastDBSizeCheckSize = size
	return lastDBSizeCheckOK, size
}

func checkDBCapacity(db *sql.DB) {
	size := getDBSize()
	pct := float64(size) / float64(DBSizeLimitBytes) * 100
	if pct >= 90 {
		logWarn("database at %.0f%% capacity, auto-pruning records older than 30 days", pct)
		if err := pruneOlderThan(db, 30); err != nil {
			logWarn("auto-prune failed: %v", err)
		}
	} else if pct >= 80 {
		logWarn("database at %.0f%% capacity (%s / %s). Consider running 'deco purge --days 30'",
			pct, formatSize(size), formatSize(DBSizeLimitBytes))
	}
}

func pruneOlderThan(db *sql.DB, days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	// Safety: table names are hardcoded string literals below, not user input,
	// so direct concatenation into the query is safe from SQL injection.
	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		if _, err := db.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoff); err != nil {
			return fmt.Errorf("failed to prune %s: %v", table, err)
		}
	}
	db.Exec("VACUUM")
	return nil
}
