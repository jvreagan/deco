package db

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an isolated test database in a temp directory.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	origPath := dbPath
	t.Cleanup(func() { SetDBPath(origPath) })
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	SetDBPath(tmpDir + "/test.db")

	database, err := InitDB()
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	return database
}

// ==================== DATABASE SCHEMA TESTS ====================

func TestDBMigrationVersion(t *testing.T) {
	database := setupTestDB(t)

	version, err := GetSchemaVersion(database)
	if err != nil {
		t.Fatalf("GetSchemaVersion failed: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestDBMigrationIdempotent(t *testing.T) {
	database := setupTestDB(t)

	if err := RunMigrations(database); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}

	version, _ := GetSchemaVersion(database)
	if version != currentSchemaVersion {
		t.Errorf("schema version after re-migration = %d, want %d", version, currentSchemaVersion)
	}
}

func TestDBSchemaTablesExist(t *testing.T) {
	database := setupTestDB(t)

	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var name string
			err := database.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
			).Scan(&name)
			if err != nil {
				t.Errorf("table %q not found: %v", table, err)
			}
		})
	}
}

func TestDBSchemaColumns(t *testing.T) {
	database := setupTestDB(t)

	expected := map[string][]string{
		"bandwidth_samples":  {"id", "timestamp", "mac", "name", "ip", "connection", "device_type", "download_kbps", "upload_kbps"},
		"network_snapshots":  {"id", "timestamp", "wan_ip", "wan_gateway", "wan_dns1", "wan_dns2", "lan_ip", "lan_netmask", "cpu_percent", "mem_percent"},
		"mesh_snapshots":     {"id", "timestamp", "name", "role", "ip", "mac", "model", "firmware", "status"},
		"wireless_snapshots": {"id", "timestamp", "band", "ssid", "channel", "channel_width", "host_enabled", "guest_enabled", "guest_ssid"},
	}

	for table, wantCols := range expected {
		t.Run(table, func(t *testing.T) {
			rows, err := database.Query("PRAGMA table_info(" + table + ")")
			if err != nil {
				t.Fatalf("PRAGMA table_info failed: %v", err)
			}
			defer rows.Close()

			gotCols := map[string]bool{}
			for rows.Next() {
				var cid int
				var name, ctype string
				var notnull int
				var dflt *string
				var pk int
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
					t.Fatalf("scan failed: %v", err)
				}
				gotCols[name] = true
			}

			for _, col := range wantCols {
				if !gotCols[col] {
					t.Errorf("table %q missing column %q", table, col)
				}
			}
		})
	}
}

func TestDBSchemaIndexes(t *testing.T) {
	database := setupTestDB(t)

	expectedIndexes := []string{
		"idx_timestamp",
		"idx_mac",
		"idx_net_timestamp",
		"idx_mesh_timestamp",
		"idx_wireless_timestamp",
	}

	for _, idx := range expectedIndexes {
		t.Run(idx, func(t *testing.T) {
			var name string
			err := database.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
			).Scan(&name)
			if err != nil {
				t.Errorf("index %q not found: %v", idx, err)
			}
		})
	}
}

func TestDBInitIdempotent(t *testing.T) {
	origPath := dbPath
	t.Cleanup(func() { SetDBPath(origPath) })
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	SetDBPath(tmpDir + "/test.db")

	db1, err := InitDB()
	if err != nil {
		t.Fatalf("first InitDB failed: %v", err)
	}
	db1.Close()

	db2, err := InitDB()
	if err != nil {
		t.Fatalf("second InitDB failed: %v", err)
	}
	db2.Close()
}

// ==================== DATABASE INSERTION + QUERY TESTS ====================

func TestDBBandwidthSamplesRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := database.Exec(
		`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDevice", "192.168.68.100", "wireless", "phone", 1500, 300,
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var mac, name, ip, conn, dtype string
	var dl, ul int
	err = database.QueryRow(
		"SELECT mac, name, ip, connection, device_type, download_kbps, upload_kbps FROM bandwidth_samples WHERE mac=?",
		"AA-BB-CC-DD-EE-FF",
	).Scan(&mac, &name, &ip, &conn, &dtype, &dl, &ul)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if mac != "AA-BB-CC-DD-EE-FF" || name != "TestDevice" || ip != "192.168.68.100" ||
		conn != "wireless" || dtype != "phone" || dl != 1500 || ul != 300 {
		t.Errorf("round-trip mismatch: got mac=%s name=%s ip=%s conn=%s type=%s dl=%d ul=%d",
			mac, name, ip, conn, dtype, dl, ul)
	}
}

func TestDBNetworkSnapshotsRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := database.Exec(
		`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", "192.168.68.1", "255.255.255.0", 12.5, 45.3,
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var wanIP, wanGW, dns1, dns2, lanIP, lanMask string
	var cpu, mem float64
	err = database.QueryRow(
		"SELECT wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent FROM network_snapshots WHERE timestamp=?", ts,
	).Scan(&wanIP, &wanGW, &dns1, &dns2, &lanIP, &lanMask, &cpu, &mem)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if wanIP != "1.2.3.4" || wanGW != "1.2.3.1" || dns1 != "8.8.8.8" || dns2 != "8.8.4.4" ||
		lanIP != "192.168.68.1" || lanMask != "255.255.255.0" || cpu != 12.5 || mem != 45.3 {
		t.Errorf("round-trip mismatch: got %s %s %s %s %s %s %f %f",
			wanIP, wanGW, dns1, dns2, lanIP, lanMask, cpu, mem)
	}
}

func TestDBMeshSnapshotsRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := database.Exec(
		`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "Deco BE63", "1.2.10", "online",
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var name, role, ip, mac, model, fw, status string
	err = database.QueryRow(
		"SELECT name, role, ip, mac, model, firmware, status FROM mesh_snapshots WHERE timestamp=?", ts,
	).Scan(&name, &role, &ip, &mac, &model, &fw, &status)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if name != "Main" || role != "master" || ip != "192.168.68.1" ||
		mac != "8C-90-2D-B5-5F-86" || model != "Deco BE63" || fw != "1.2.10" || status != "online" {
		t.Errorf("round-trip mismatch: got %s %s %s %s %s %s %s",
			name, role, ip, mac, model, fw, status)
	}
}

func TestDBWirelessSnapshotsRoundTrip(t *testing.T) {
	database := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := database.Exec(
		`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "5GHz", "MyNetwork", "149", "80MHz", 1, 0, "MyNetwork_Guest",
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var band, ssid, ch, chw, guestSSID string
	var hostEn, guestEn int
	err = database.QueryRow(
		"SELECT band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid FROM wireless_snapshots WHERE timestamp=?", ts,
	).Scan(&band, &ssid, &ch, &chw, &hostEn, &guestEn, &guestSSID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if band != "5GHz" || ssid != "MyNetwork" || ch != "149" || chw != "80MHz" ||
		hostEn != 1 || guestEn != 0 || guestSSID != "MyNetwork_Guest" {
		t.Errorf("round-trip mismatch: got %s %s %s %s %d %d %s",
			band, ssid, ch, chw, hostEn, guestEn, guestSSID)
	}
}

func TestDBCrossTableCorrelation(t *testing.T) {
	database := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDev", 100, 50)
	database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, cpu_percent, mem_percent) VALUES (?, ?, ?, ?)`,
		ts, "1.2.3.4", 10.0, 20.0)
	database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, status) VALUES (?, ?, ?, ?)`,
		ts, "Main", "master", "online")
	database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`,
		ts, "5GHz", "TestNet")

	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var count int
			err := database.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE timestamp=?", ts).Scan(&count)
			if err != nil {
				t.Fatalf("query failed: %v", err)
			}
			if count != 1 {
				t.Errorf("expected 1 row in %s for timestamp %s, got %d", table, ts, count)
			}
		})
	}
}

// ==================== SIZE LIMIT TESTS ====================

func TestCheckDBSizeLimit(t *testing.T) {
	_ = setupTestDB(t)

	ok, size := CheckDBSizeLimit()
	if !ok {
		t.Errorf("CheckDBSizeLimit should return true for small DB, got false (size=%d)", size)
	}
}

// ==================== PRUNE TESTS ====================

func TestPruneOlderThan(t *testing.T) {
	database := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		oldTS, "AA-BB-CC-DD-EE-FF", "OldDevice", 100, 50)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		newTS, "AA-BB-CC-DD-EE-FF", "NewDevice", 200, 100)

	database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, newTS, "1.2.3.4")

	database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, newTS, "Main", "master")

	database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")
	database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, newTS, "5GHz", "Net")

	err := PruneOlderThan(database, 30)
	if err != nil {
		t.Fatalf("PruneOlderThan failed: %v", err)
	}

	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		var count int
		database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 1 {
			t.Errorf("expected 1 row in %s after prune, got %d", table, count)
		}
	}
}

func TestSelectivePurge(t *testing.T) {
	database := setupTestDB(t)

	dates := []string{
		time.Now().AddDate(0, 0, -10).Format(time.RFC3339),
		time.Now().AddDate(0, 0, -5).Format(time.RFC3339),
		time.Now().Format(time.RFC3339),
	}

	for _, ts := range dates {
		database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
			ts, "AA-BB-CC-DD-EE-FF", "Device", 100, 50)
	}

	err := PruneOlderThan(database, 7)
	if err != nil {
		t.Fatalf("PruneOlderThan failed: %v", err)
	}

	var count int
	database.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 rows after prune (7 days), got %d", count)
	}
}

// ==================== PARSE PERIOD TESTS ====================

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantErr  bool
	}{
		{"today", "Today", false},
		{"hour", "Last hour", false},
		{"all", "All time", false},
		{"", "", true},
		{"bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			startTime, name, err := ParsePeriod(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParsePeriod(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePeriod(%q) unexpected error: %v", tt.input, err)
			}
			if name != tt.wantName {
				t.Errorf("ParsePeriod(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
			if tt.input == "today" {
				now := time.Now()
				want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
				if !startTime.Equal(want) {
					t.Errorf("ParsePeriod(\"today\") startTime = %v, want %v", startTime, want)
				}
			}
			if tt.input == "hour" {
				if time.Since(startTime) > 61*time.Minute || time.Since(startTime) < 59*time.Minute {
					t.Errorf("ParsePeriod(\"hour\") startTime should be ~1 hour ago, got %v", startTime)
				}
			}
			if tt.input == "all" {
				if !startTime.IsZero() {
					t.Errorf("ParsePeriod(%q) startTime should be zero, got %v", tt.input, startTime)
				}
			}
		})
	}
}

// ==================== PURGE BY DATE INTEGRATION TESTS ====================

func TestPurgeByDaysIntegration(t *testing.T) {
	database := setupTestDB(t)

	ts20 := time.Now().AddDate(0, 0, -20).Format(time.RFC3339)
	ts5 := time.Now().AddDate(0, 0, -5).Format(time.RFC3339)
	tsNow := time.Now().Format(time.RFC3339)

	for _, ts := range []string{ts20, ts5, tsNow} {
		database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
			ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
		database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, ts, "1.2.3.4")
		database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, ts, "Main", "master")
		database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, ts, "5GHz", "Net")
	}

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := PurgeByDate(database, cutoff)
	if err != nil {
		t.Fatalf("PurgeByDate failed: %v", err)
	}

	if deleted != 4 {
		t.Errorf("PurgeByDate deleted %d records, want 4", deleted)
	}

	for _, table := range AllTables {
		var count int
		database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 2 {
			t.Errorf("expected 2 rows in %s after purge, got %d", table, count)
		}
	}
}

func TestPurgeByDatePreservesRecent(t *testing.T) {
	database := setupTestDB(t)

	tsNow := time.Now().Format(time.RFC3339)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		tsNow, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := PurgeByDate(database, cutoff)
	if err != nil {
		t.Fatalf("PurgeByDate failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("PurgeByDate deleted %d records, want 0 (all recent)", deleted)
	}

	var count int
	database.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row preserved, got %d", count)
	}
}

func TestPurgeByDateEmptyDB(t *testing.T) {
	database := setupTestDB(t)
	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := PurgeByDate(database, cutoff)
	if err != nil {
		t.Fatalf("PurgeByDate on empty DB failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("PurgeByDate on empty DB deleted %d records, want 0", deleted)
	}
}

func TestPurgeByDateAllTables(t *testing.T) {
	database := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)

	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		oldTS, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
	database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := PurgeByDate(database, cutoff)
	if err != nil {
		t.Fatalf("PurgeByDate failed: %v", err)
	}
	if deleted != 4 {
		t.Errorf("PurgeByDate deleted %d records, want 4 (1 per table)", deleted)
	}

	for _, table := range AllTables {
		var count int
		database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 rows in %s after full purge, got %d", table, count)
		}
	}
}

// ==================== KNOWN MACS TESTS ====================

func TestLoadKnownMACs(t *testing.T) {
	database := setupTestDB(t)

	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "AA-BB-CC-DD-EE-FF", "Dev1", 100, 50)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "11-22-33-44-55-66", "Dev2", 200, 100)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "AA-BB-CC-DD-EE-FF", "Dev1Again", 50, 25)

	known := LoadKnownMACs(database)
	if len(known) != 2 {
		t.Errorf("LoadKnownMACs returned %d MACs, want 2", len(known))
	}
	if !known["AA-BB-CC-DD-EE-FF"] {
		t.Error("missing AA-BB-CC-DD-EE-FF in known MACs")
	}
	if !known["11-22-33-44-55-66"] {
		t.Error("missing 11-22-33-44-55-66 in known MACs")
	}
}

// ==================== ESTIMATE INTERVAL TESTS ====================

func TestEstimateInterval(t *testing.T) {
	database := setupTestDB(t)

	ResetCachedInterval()

	// No data → fallback to 5
	got := EstimateInterval(database, time.Time{})
	if got != 5 {
		t.Errorf("empty DB: EstimateInterval = %d, want 5", got)
	}

	// Insert samples 60s apart
	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * 60 * time.Second).Format(time.RFC3339)
		database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
	}

	// Reset cache to force re-computation with new data
	ResetCachedInterval()

	got = EstimateInterval(database, time.Time{})
	if got != 60 {
		t.Errorf("60s intervals: EstimateInterval = %d, want 60", got)
	}
}

func TestEstimateIntervalSingleSample(t *testing.T) {
	database := setupTestDB(t)

	ResetCachedInterval()

	ts := time.Now().Format(time.RFC3339)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)

	got := EstimateInterval(database, time.Time{})
	if got != 5 {
		t.Errorf("single sample: EstimateInterval = %d, want 5 (fallback)", got)
	}
}

// ==================== countBeforeDate TESTS ====================

func TestCountBeforeDate(t *testing.T) {
	database := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, oldTS, "AA-BB-CC-DD-EE-FF", "Old", 100, 50)
	database.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, newTS, "AA-BB-CC-DD-EE-FF", "New", 200, 100)
	database.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	database.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	database.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")

	cutoff := time.Now().AddDate(0, 0, -7)
	count, err := CountBeforeDate(database, cutoff)
	if err != nil {
		t.Fatalf("CountBeforeDate failed: %v", err)
	}
	if count != 4 {
		t.Errorf("CountBeforeDate = %d, want 4 (1 per table)", count)
	}
}

func TestCountBeforeDateEmpty(t *testing.T) {
	database := setupTestDB(t)

	cutoff := time.Now().AddDate(0, 0, -7)
	count, err := CountBeforeDate(database, cutoff)
	if err != nil {
		t.Fatalf("CountBeforeDate failed: %v", err)
	}
	if count != 0 {
		t.Errorf("CountBeforeDate on empty DB = %d, want 0", count)
	}
}
