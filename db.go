package main

import (
	"database/sql"

	"github.com/jvreagan/deco/internal/db"
	"github.com/jvreagan/deco/internal/decolog"
)

// Thin wrappers delegating to internal/db.

const DBSizeLimitBytes = db.DBSizeLimitBytes

func setDBPath(path string)                 { db.SetDBPath(path); dbPath = path }
func initDB() (*sql.DB, error)              { return db.InitDB() }
func getDBSize() int64                       { return db.GetDBSize() }
func checkDBSizeLimit() (bool, int64)        { return db.CheckDBSizeLimit() }
func pruneOlderThan(database *sql.DB, days int) error { return db.PruneOlderThan(database, days) }

var dbPath = db.DBPath() // for legacy references; prefer db.DBPath()

// checkDBCapacity stays in main because it calls formatSize from output.go.
func checkDBCapacity(database *sql.DB) {
	size := getDBSize()
	pct := float64(size) / float64(DBSizeLimitBytes) * 100
	if pct >= 90 {
		decolog.Warn("database at %.0f%% capacity, auto-pruning records older than 30 days", pct)
		if err := pruneOlderThan(database, 30); err != nil {
			decolog.Warn("auto-prune failed: %v", err)
		}
	} else if pct >= 80 {
		decolog.Warn("database at %.0f%% capacity (%s / %s). Consider running 'deco purge --days 30'",
			pct, formatSize(size), formatSize(DBSizeLimitBytes))
	}
}
