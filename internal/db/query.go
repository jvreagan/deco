package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jvreagan/deco/internal/decolog"
)

// AllTables lists all data tables in the database.
var AllTables = []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}


// ParsePeriod converts a period string to a start time and display name.
func ParsePeriod(period string) (time.Time, string, error) {
	switch strings.ToLower(period) {
	case "today":
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), "Today", nil
	case "hour", "1h":
		return time.Now().Add(-1 * time.Hour), "Last hour", nil
	case "6h":
		return time.Now().Add(-6 * time.Hour), "Last 6 hours", nil
	case "12h":
		return time.Now().Add(-12 * time.Hour), "Last 12 hours", nil
	case "24h":
		return time.Now().Add(-24 * time.Hour), "Last 24 hours", nil
	case "7d":
		return time.Now().AddDate(0, 0, -7), "Last 7 days", nil
	case "30d":
		return time.Now().AddDate(0, 0, -30), "Last 30 days", nil
	case "all":
		// Use Unix epoch (1970) instead of Go zero time (year 0001) to produce
		// a valid, unambiguous RFC3339 timestamp for SQL comparisons.
		return time.Unix(0, 0).UTC(), "All time", nil
	default:
		return time.Time{}, "", fmt.Errorf("unknown period %q; valid periods: today, hour, 1h, 6h, 12h, 24h, 7d, 30d, all", period)
	}
}


// CachedInterval caches the result of EstimateInterval to avoid redundant queries.
// The cache is TTL-based only (no key on the `since` parameter) because the
// sampling interval is a property of the database, not the query window.
var CachedInterval struct {
	mu       sync.Mutex
	Val      int
	Set      bool
	CachedAt time.Time
}

const intervalCacheTTL = 5 * time.Minute

// ResetCachedInterval clears the cached interval state. Intended for use in tests.
func ResetCachedInterval() {
	CachedInterval.mu.Lock()
	defer CachedInterval.mu.Unlock()
	CachedInterval.Set = false
	CachedInterval.Val = 0
}

// Querier is implemented by both *sql.DB and *sql.Tx, allowing functions to
// operate within or outside a transaction.
type Querier interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// EstimateInterval derives the polling interval from sample timestamps.
// Returns the median gap between consecutive distinct timestamps, or 5 as fallback.
// Accepts both *sql.DB and *sql.Tx via the Querier interface.
func EstimateInterval(database Querier, since time.Time) int {
	CachedInterval.mu.Lock()
	if CachedInterval.Set && time.Since(CachedInterval.CachedAt) < intervalCacheTTL {
		val := CachedInterval.Val
		CachedInterval.mu.Unlock()
		return val
	}
	CachedInterval.mu.Unlock()

	rows, err := database.Query(`SELECT DISTINCT timestamp FROM bandwidth_samples
		WHERE timestamp >= ? ORDER BY timestamp LIMIT 20`, since.Format(time.RFC3339))
	if err != nil {
		return 5
	}
	defer rows.Close()

	var timestamps []time.Time
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err == nil {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				timestamps = append(timestamps, t)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 5
	}

	if len(timestamps) < 2 {
		return 5
	}

	var gaps []int
	for i := 1; i < len(timestamps); i++ {
		gap := int(timestamps[i].Sub(timestamps[i-1]).Seconds())
		if gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 5
	}

	sort.Ints(gaps)
	result := gaps[len(gaps)/2] // median

	CachedInterval.mu.Lock()
	CachedInterval.Val = result
	CachedInterval.Set = true
	CachedInterval.CachedAt = time.Now()
	CachedInterval.mu.Unlock()

	return result
}

// LoadKnownMACs returns a set of all distinct MACs seen in the bandwidth_samples table.
func LoadKnownMACs(database *sql.DB) map[string]bool {
	known := map[string]bool{}
	rows, err := database.Query("SELECT DISTINCT mac FROM bandwidth_samples")
	if err != nil {
		return known
	}
	defer rows.Close()
	for rows.Next() {
		var mac string
		if err := rows.Scan(&mac); err == nil {
			known[strings.ToUpper(mac)] = true
		}
	}
	if err := rows.Err(); err != nil {
		decolog.Warn("error iterating known MACs: %v", err)
	}
	return known
}

// CountBeforeDate returns the total number of records across all tables before the cutoff.
func CountBeforeDate(database *sql.DB, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.Format(time.RFC3339)
	var total int64
	for _, table := range AllTables {
		var c int64
		if err := database.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE timestamp < ?", cutoffStr).Scan(&c); err != nil {
			return total, err
		}
		total += c
	}
	return total, nil
}

// PurgeByDate deletes all records before the cutoff across all tables.
// All deletes are wrapped in a single transaction for atomicity.
func PurgeByDate(database *sql.DB, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.Format(time.RFC3339)
	tx, err := database.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %v", err)
	}
	var total int64
	for _, table := range AllTables {
		result, err := tx.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoffStr)
		if err != nil {
			tx.Rollback()
			return total, fmt.Errorf("error deleting from %s: %v", table, err)
		}
		n, _ := result.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("commit transaction: %v", err)
	}
	if total > 0 {
		if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			decolog.Warn("WAL checkpoint failed: %v", err)
		}
	}
	return total, nil
}
