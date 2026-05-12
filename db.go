package main

import (
	"database/sql"
	"fmt"
	"os"

	dbpkg "github.com/jvreagan/deco/internal/db"
)

// Thin wrappers delegating to internal/db.

const DBSizeLimitBytes = dbpkg.DBSizeLimitBytes

func setDBPath(path string)                 { dbpkg.SetDBPath(path) }
func initDB() (*sql.DB, error)              { return dbpkg.InitDB() }
func getDBSize() int64                       { return dbpkg.GetDBSize() }
func checkDBSizeLimit() (bool, int64)        { return dbpkg.CheckDBSizeLimit() }
func pruneOlderThan(database *sql.DB, days int) error { return dbpkg.PruneOlderThan(database, days) }

// checkDBCapacity stays in main because it calls formatSize from output.go.
func checkDBCapacity(database *sql.DB) {
	size := getDBSize()
	pct := float64(size) / float64(DBSizeLimitBytes) * 100
	if pct >= 90 {
		fmt.Fprintf(os.Stderr, "Warning: database at %.0f%% capacity, auto-pruning records older than 30 days\n", pct)
		if err := pruneOlderThan(database, 30); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-prune failed: %v\n", err)
		} else {
			dbpkg.ResetSizeCache()
		}
	} else if pct >= 80 {
		fmt.Fprintf(os.Stderr, "Warning: database at %.0f%% capacity (%s / %s). Consider running 'deco purge --days 30'\n",
			pct, formatSize(size), formatSize(DBSizeLimitBytes))
	}
}
