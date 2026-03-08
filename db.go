package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Database constants
const (
	DBSizeLimitBytes = 250 * 1024 * 1024 * 1024 // 250 GB
)

var dbPath string

func init() {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(dir); err == nil {
			dbPath = filepath.Join(dir, "network_usage.db")
			return
		}
	}
	dbPath = "network_usage.db"
}

func initDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
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
	`)
	if err != nil {
		return nil, err
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

func checkDBSizeLimit() (bool, int64) {
	size := getDBSize()
	return size < DBSizeLimitBytes, size
}

func checkDBCapacity(db *sql.DB) {
	size := getDBSize()
	pct := float64(size) / float64(DBSizeLimitBytes) * 100
	if pct >= 90 {
		fmt.Fprintf(os.Stderr, "Warning: database at %.0f%% capacity, auto-pruning records older than 30 days\n", pct)
		if err := pruneOlderThan(db, 30); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-prune failed: %v\n", err)
		}
	} else if pct >= 80 {
		fmt.Fprintf(os.Stderr, "Warning: database at %.0f%% capacity (%s / %s). Consider running 'deco purge --days 30'\n",
			pct, formatSize(size), formatSize(DBSizeLimitBytes))
	}
}

func pruneOlderThan(db *sql.DB, days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		if _, err := db.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoff); err != nil {
			return fmt.Errorf("failed to prune %s: %v", table, err)
		}
	}
	return nil
}
