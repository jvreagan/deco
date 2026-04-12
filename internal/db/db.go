package db

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jvreagan/deco/internal/decolog"
	"github.com/jvreagan/deco/internal/paths"

	_ "modernc.org/sqlite"
)

// DBSizeLimitBytes is the maximum database size before writes are blocked.
const DBSizeLimitBytes = 1 * 1024 * 1024 * 1024 // 1 GB

var dbPath string

func init() {
	dbPath = paths.CfgPath("network_usage.db")
}

// SetDBPath overrides the database path (used in tests).
func SetDBPath(path string) {
	dbPath = path
}

// DBPath returns the current database path.
func DBPath() string {
	return dbPath
}

// currentSchemaVersion is the latest migration version.
const currentSchemaVersion = 1

type migration struct {
	version int
	sql     string
}

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
}

// GetSchemaVersion returns the current schema version of the database.
func GetSchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

// SetSchemaVersion sets the SQLite user_version pragma. fmt.Sprintf is used
// because PRAGMA statements do not support parameterized queries (?). The
// version argument comes from hardcoded migration constants, not user input,
// so SQL injection is not a concern here.
func SetSchemaVersion(db *sql.DB, version int) error {
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}

// RunMigrations applies pending schema migrations.
func RunMigrations(db *sql.DB) error {
	current, err := GetSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("reading schema version: %v", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		decolog.Debug("running migration to schema version %d", m.version)

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %v", m.version, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration to version %d failed: %v", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %v", m.version, err)
		}
		if err := SetSchemaVersion(db, m.version); err != nil {
			return fmt.Errorf("setting schema version %d: %v", m.version, err)
		}
	}
	return nil
}

// CurrentSchemaVersion returns the expected schema version constant.
func CurrentSchemaVersion() int {
	return currentSchemaVersion
}

// InitDB opens (or creates) the SQLite database and runs migrations.
func InitDB() (*sql.DB, error) {
	if err := paths.EnsureConfigDir(); err != nil {
		return nil, fmt.Errorf("cannot create config directory: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %v", err)
	}

	return db, nil
}

// GetDBSize returns the size of the database file in bytes.
func GetDBSize() int64 {
	info, err := os.Stat(dbPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

const dbSizeCheckThrottleSec = 60

var dbSizeCheckMu sync.Mutex
var (
	lastDBSizeCheckTime time.Time
	lastDBSizeCheckOK   bool
	lastDBSizeCheckSize int64
)

// CheckDBSizeLimit returns whether the database is within the size limit and its current size.
func CheckDBSizeLimit() (bool, int64) {
	dbSizeCheckMu.Lock()
	defer dbSizeCheckMu.Unlock()

	now := time.Now()
	if !lastDBSizeCheckTime.IsZero() && now.Sub(lastDBSizeCheckTime).Seconds() < float64(dbSizeCheckThrottleSec) {
		return lastDBSizeCheckOK, lastDBSizeCheckSize
	}
	size := GetDBSize()
	lastDBSizeCheckTime = now
	lastDBSizeCheckOK = size < DBSizeLimitBytes
	lastDBSizeCheckSize = size
	return lastDBSizeCheckOK, size
}

// PruneOlderThan deletes records older than the given number of days from all tables.
func PruneOlderThan(db *sql.DB, days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin prune transaction: %v", err)
	}
	for _, table := range tables {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoff); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to prune %s: %v", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prune transaction: %v", err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		decolog.Warn("VACUUM failed: %v", err)
	}
	return nil
}
