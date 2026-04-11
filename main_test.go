package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ==================== MAC VALIDATION TESTS ====================

func TestValidMAC(t *testing.T) {
	tests := []struct {
		name string
		mac  string
		want bool
	}{
		{"valid dash", "AA-BB-CC-DD-EE-FF", true},
		{"valid colon", "AA:BB:CC:DD:EE:FF", true},
		{"valid lowercase", "aa-bb-cc-dd-ee-ff", true},
		{"valid mixed case", "aA-Bb-Cc-Dd-Ee-Ff", true},
		{"too short", "AA-BB-CC-DD-EE", false},
		{"too long", "AA-BB-CC-DD-EE-FF-00", false},
		{"non-hex", "GG-HH-II-JJ-KK-LL", false},
		{"empty", "", false},
		{"no separator", "AABBCCDDEEFF", false},
		{"mixed separators", "AA-BB:CC-DD:EE-FF", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validMAC(tt.mac)
			if got != tt.want {
				t.Errorf("validMAC(%q) = %v, want %v", tt.mac, got, tt.want)
			}
		})
	}
}

// ==================== PURE UTILITY TESTS ====================

func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want int
	}{
		{"int", 42, 42},
		{"int64", int64(99), 99},
		{"float64", float64(3.7), 3},
		{"string returns zero", "hello", 0},
		{"nil returns zero", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toInt(tt.in)
			if got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestToFloat(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want float64
	}{
		{"float64", float64(3.14), 3.14},
		{"int", 42, 42.0},
		{"int64", int64(99), 99.0},
		{"string returns zero", "hello", 0},
		{"nil returns zero", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toFloat(tt.in)
			if got != tt.want {
				t.Errorf("toFloat(%v) = %f, want %f", tt.in, got, tt.want)
			}
		})
	}
}

func TestGetMap(t *testing.T) {
	data := map[string]interface{}{
		"nested": map[string]interface{}{"a": 1},
		"wrong":  "not a map",
	}

	t.Run("key exists", func(t *testing.T) {
		got := getMap(data, "nested")
		if got["a"] != 1 {
			t.Errorf("getMap returned %v, want map with a=1", got)
		}
	})

	t.Run("key missing", func(t *testing.T) {
		got := getMap(data, "nope")
		if len(got) != 0 {
			t.Errorf("getMap for missing key returned %v, want empty map", got)
		}
	})

	t.Run("key wrong type", func(t *testing.T) {
		got := getMap(data, "wrong")
		if len(got) != 0 {
			t.Errorf("getMap for wrong type returned %v, want empty map", got)
		}
	})
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"bytes", 512, "512 B"},
		{"KB", 2048, "2.00 KB"},
		{"MB", 5 * 1024 * 1024, "5.00 MB"},
		{"GB", 3 * 1024 * 1024 * 1024, "3.00 GB"},
		{"zero", 0, "0 B"},
		{"boundary KB", 1024, "1.00 KB"},
		{"boundary MB", 1024 * 1024, "1.00 MB"},
		{"boundary GB", 1024 * 1024 * 1024, "1.00 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		kb   float64
		want string
	}{
		{"KB", 500, "500.0 KB"},
		{"MB", 2048, "2.00 MB"},
		{"GB", 2 * 1024 * 1024, "2.00 GB"},
		{"zero", 0, "0.0 KB"},
		{"boundary MB", 1024, "1.00 MB"},
		{"boundary GB", 1024 * 1024, "1.00 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBytes(tt.kb)
			if got != tt.want {
				t.Errorf("formatBytes(%f) = %q, want %q", tt.kb, got, tt.want)
			}
		})
	}
}

// ==================== LOGGER TESTS ====================

func TestSetVerbose(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	logLevel.Store(int32(LevelWarn))
	SetVerbose(true)
	if logLevel.Load() != int32(LevelDebug) {
		t.Errorf("SetVerbose(true): logLevel = %d, want %d", logLevel.Load(), LevelDebug)
	}

	// SetVerbose(false) should not change level
	logLevel.Store(int32(LevelWarn))
	SetVerbose(false)
	if logLevel.Load() != int32(LevelWarn) {
		t.Errorf("SetVerbose(false): logLevel = %d, want %d", logLevel.Load(), LevelWarn)
	}
}

func TestLogLevels(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// At default level (Warn), debug should be hidden
	logLevel.Store(int32(LevelWarn))
	logDebug("hidden message")
	logWarn("visible warning")

	w.Close()
	os.Stderr = oldStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if strings.Contains(output, "hidden message") {
		t.Error("debug message should be hidden at Warn level")
	}
	if !strings.Contains(output, "visible warning") {
		t.Error("warn message should be visible at Warn level")
	}

	// At debug level, debug should be visible
	r2, w2, _ := os.Pipe()
	os.Stderr = w2

	logLevel.Store(int32(LevelDebug))
	logDebug("now visible")

	w2.Close()
	os.Stderr = oldStderr

	var buf2 [4096]byte
	n2, _ := r2.Read(buf2[:])
	output2 := string(buf2[:n2])

	if !strings.Contains(output2, "now visible") {
		t.Error("debug message should be visible at Debug level")
	}
}

// ==================== COBRA CLI TESTS ====================

func TestCobraRootHelp(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs([]string{"--help"})
	rootCmd.Execute()

	w.Close()
	os.Stdout = old

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	subcommands := []string{"clients", "network", "wireless", "mesh", "all", "poll", "monitor", "report", "status", "purge", "setup", "api", "version", "reboot", "block", "unblock", "alias"}
	for _, cmd := range subcommands {
		if !strings.Contains(output, cmd) {
			t.Errorf("root help should list %q subcommand", cmd)
		}
	}
}

// ==================== BACKOFF TESTS ====================

func TestBackoff(t *testing.T) {
	base := 5 * time.Second
	max := 5 * time.Minute

	tests := []struct {
		failures int
		want     time.Duration
	}{
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 160 * time.Second},
		{6, 5 * time.Minute},  // capped
		{10, 5 * time.Minute}, // still capped
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("failures=%d", tt.failures), func(t *testing.T) {
			got := backoff(tt.failures, base, max)
			if got != tt.want {
				t.Errorf("backoff(%d, %s, %s) = %s, want %s", tt.failures, base, max, got, tt.want)
			}
		})
	}
}

// ==================== TEST HELPERS ====================

// testEnv sets up an isolated config environment for a test.
// It points XDG_CONFIG_HOME at a temp dir and re-derives dbPath.
//
// LIMITATION: This function calls setDBPath which mutates the package-level
// dbPath variable. Because of this (and other package-level state such as
// the initDB schema-version cache), tests that call testEnv or setupTestDB
// must NOT use t.Parallel(). Restructuring to pass a DB path through the
// call chain would be the proper fix but is a large cross-cutting change.
//
// t.Cleanup restores the original dbPath so that tests run in sequence do
// not leak state from one test into the next.
func testEnv(t *testing.T) string {
	t.Helper()
	origDBPath := dbPath
	t.Cleanup(func() { setDBPath(origDBPath) })
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	setDBPath(cfgPath("network_usage.db"))
	return tmpDir
}

// ==================== DATABASE SCHEMA TESTS ====================

// TODO(#23): Many tests in this file capture stdout by reassigning os.Stdout
// (e.g., `old := os.Stdout; os.Stdout = w; ... os.Stdout = old`). This is
// fragile: it is not safe with t.Parallel(), panics can leak the reassignment,
// and it couples tests to global state. The proper fix is to refactor the
// production functions (runList, runReport, runPurge, etc.) to accept an
// io.Writer parameter instead of writing directly to os.Stdout. That change
// is large but would make the tests safer and more composable.

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}

func TestDBMigrationVersion(t *testing.T) {
	db := setupTestDB(t)

	version, err := getSchemaVersion(db)
	if err != nil {
		t.Fatalf("getSchemaVersion failed: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestDBMigrationIdempotent(t *testing.T) {
	db := setupTestDB(t)

	// Running migrations again on an already-migrated DB should be a no-op
	if err := runMigrations(db); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}

	version, _ := getSchemaVersion(db)
	if version != currentSchemaVersion {
		t.Errorf("schema version after re-migration = %d, want %d", version, currentSchemaVersion)
	}
}

func TestDBSchemaTablesExist(t *testing.T) {
	db := setupTestDB(t)

	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var name string
			err := db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
			).Scan(&name)
			if err != nil {
				t.Errorf("table %q not found: %v", table, err)
			}
		})
	}
}

func TestDBSchemaColumns(t *testing.T) {
	db := setupTestDB(t)

	expected := map[string][]string{
		"bandwidth_samples": {"id", "timestamp", "mac", "name", "ip", "connection", "device_type", "download_kbps", "upload_kbps"},
		"network_snapshots": {"id", "timestamp", "wan_ip", "wan_gateway", "wan_dns1", "wan_dns2", "lan_ip", "lan_netmask", "cpu_percent", "mem_percent"},
		"mesh_snapshots":    {"id", "timestamp", "name", "role", "ip", "mac", "model", "firmware", "status"},
		"wireless_snapshots": {"id", "timestamp", "band", "ssid", "channel", "channel_width", "host_enabled", "guest_enabled", "guest_ssid"},
	}

	for table, wantCols := range expected {
		t.Run(table, func(t *testing.T) {
			rows, err := db.Query("PRAGMA table_info(" + table + ")")
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
	db := setupTestDB(t)

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
			err := db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
			).Scan(&name)
			if err != nil {
				t.Errorf("index %q not found: %v", idx, err)
			}
		})
	}
}

func TestDBInitIdempotent(t *testing.T) {
	testEnv(t)

	db1, err := initDB()
	if err != nil {
		t.Fatalf("first initDB failed: %v", err)
	}
	db1.Close()

	db2, err := initDB()
	if err != nil {
		t.Fatalf("second initDB failed: %v", err)
	}
	db2.Close()
}

// ==================== DATABASE INSERTION + QUERY TESTS ====================

func TestDBBandwidthSamplesRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := db.Exec(
		`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDevice", "192.168.68.100", "wireless", "phone", 1500, 300,
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var mac, name, ip, conn, dtype string
	var dl, ul int
	err = db.QueryRow(
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
	db := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := db.Exec(
		`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", "192.168.68.1", "255.255.255.0", 12.5, 45.3,
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var wanIP, wanGW, dns1, dns2, lanIP, lanMask string
	var cpu, mem float64
	err = db.QueryRow(
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
	db := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := db.Exec(
		`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "Deco BE63", "1.2.10", "online",
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var name, role, ip, mac, model, fw, status string
	err = db.QueryRow(
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
	db := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err := db.Exec(
		`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "5GHz", "MyNetwork", "149", "80MHz", 1, 0, "MyNetwork_Guest",
	)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	var band, ssid, ch, chw, guestSSID string
	var hostEn, guestEn int
	err = db.QueryRow(
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
	db := setupTestDB(t)
	ts := time.Now().UTC().Format("2006-01-02 15:04:05")

	// Insert a row into each table with the same timestamp
	_, err := db.Exec(
		`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDev", 100, 50,
	)
	if err != nil {
		t.Fatalf("bandwidth insert failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO network_snapshots (timestamp, wan_ip, cpu_percent, mem_percent) VALUES (?, ?, ?, ?)`,
		ts, "1.2.3.4", 10.0, 20.0,
	)
	if err != nil {
		t.Fatalf("network insert failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO mesh_snapshots (timestamp, name, role, status) VALUES (?, ?, ?, ?)`,
		ts, "Main", "master", "online",
	)
	if err != nil {
		t.Fatalf("mesh insert failed: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`,
		ts, "5GHz", "TestNet",
	)
	if err != nil {
		t.Fatalf("wireless insert failed: %v", err)
	}

	// Query each table by the shared timestamp — all should return exactly 1 row
	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			var count int
			err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE timestamp=?", ts).Scan(&count)
			if err != nil {
				t.Fatalf("query failed: %v", err)
			}
			if count != 1 {
				t.Errorf("expected 1 row in %s for timestamp %s, got %d", table, ts, count)
			}
		})
	}
}

// ==================== OUTPUT TESTS ====================

func TestPrintClientsTable(t *testing.T) {
	data := &ClientList{
		Clients: []ClientInfo{
			{Name: "TestDevice", IP: "192.168.68.100", MAC: "AA-BB-CC-DD-EE-FF", Connection: "WiFi 5GHz", Type: "phone", DownloadKbps: 1500, UploadKbps: 300},
			{Name: "VeryLongDeviceNameThatShouldBeTruncatedHere", IP: "192.168.68.101", MAC: "11-22-33-44-55-66", Connection: "Wired", Type: "computer", DownloadKbps: 0, UploadKbps: 0},
		},
		Count: 2,
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printClientsTable(data)

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "NAME") {
		t.Error("output should contain header NAME")
	}
	if !strings.Contains(output, "TestDevice") {
		t.Error("output should contain TestDevice")
	}
	if !strings.Contains(output, "Total: 2 clients") {
		t.Error("output should contain Total: 2 clients")
	}
	if !strings.Contains(output, "1500KB/s") {
		t.Error("output should contain download speed 1500KB/s")
	}
}

func TestPrintNetworkTable(t *testing.T) {
	cpu := float64(15)
	mem := float64(42)
	data := &NetworkInfo{
		WAN: WANInfo{IP: "1.2.3.4", Gateway: "1.2.3.1", Netmask: "255.255.255.0", MAC: "AA-BB-CC-DD-EE-00"},
		LAN: LANInfo{IP: "192.168.68.1", Netmask: "255.255.255.0", MAC: "AA-BB-CC-DD-EE-01"},
		Performance: PerformanceInfo{CPUPercent: &cpu, MemPercent: &mem},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printNetworkTable(data)

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "=== WAN ===") {
		t.Error("output should contain WAN section")
	}
	if !strings.Contains(output, "=== LAN ===") {
		t.Error("output should contain LAN section")
	}
	if !strings.Contains(output, "=== Performance ===") {
		t.Error("output should contain Performance section")
	}
	if !strings.Contains(output, "15%") {
		t.Error("output should show CPU 15%")
	}
	if !strings.Contains(output, "42%") {
		t.Error("output should show Memory 42%")
	}

	// Test with nil CPU/Mem
	dataNil := &NetworkInfo{
		WAN: WANInfo{IP: "1.2.3.4"},
		LAN: LANInfo{IP: "192.168.68.1"},
	}

	r2, w2, _ := os.Pipe()
	os.Stdout = w2

	printNetworkTable(dataNil)

	w2.Close()
	os.Stdout = old

	var buf2 [4096]byte
	n2, _ := r2.Read(buf2[:])
	output2 := string(buf2[:n2])

	if !strings.Contains(output2, "N/A") {
		t.Error("output should show N/A for nil CPU/Mem")
	}
}

func TestPrintMeshTable(t *testing.T) {
	data := &MeshInfo{
		Devices: []MeshDevice{
			{Name: "Main", Model: "Deco BE63", Role: "master", IP: "192.168.68.1", MAC: "8C-90-2D-B5-5F-86"},
			{Name: "Slave", Model: "Deco BE63", Role: "slave", IP: "192.168.71.250", MAC: "8C-90-2D-B5-5F-8C"},
		},
		Count: 2,
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printMeshTable(data)

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "* Main") {
		t.Error("output should show master with * marker")
	}
	if !strings.Contains(output, "Mesh Devices (2)") {
		t.Error("output should show device count")
	}
}

func TestPrintReport(t *testing.T) {
	report := &Report{
		Period:          "Today",
		StartTime:       "2026-03-08T00:00:00Z",
		QueryTime:       "2026-03-08T12:00:00Z",
		IntervalSeconds: 5,
		TotalSamples:    100,
		Devices: []ReportDevice{
			{MAC: "AA-BB-CC-DD-EE-FF", Name: "Phone", IP: "192.168.68.100", Connection: "WiFi 5GHz", TotalDownload: 1000, TotalUpload: 500},
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printReport(report)

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "BANDWIDTH USAGE REPORT") {
		t.Error("output should contain report header")
	}
	if !strings.Contains(output, "TOTAL") {
		t.Error("output should contain TOTAL row")
	}

	// Test no data case
	emptyReport := &Report{
		Period:  "Today",
		Devices: []ReportDevice{},
	}

	r2, w2, _ := os.Pipe()
	os.Stdout = w2

	printReport(emptyReport)

	w2.Close()
	os.Stdout = old

	var buf2 [4096]byte
	n2, _ := r2.Read(buf2[:])
	output2 := string(buf2[:n2])

	if !strings.Contains(output2, "No data recorded") {
		t.Error("empty report should say 'No data recorded'")
	}
}

func TestPrintWirelessTableDeterministic(t *testing.T) {
	data := &WirelessInfo{
		Bands: map[string]BandInfo{
			"6GHz":   {Band: "6GHz", Host: HostInfo{Enabled: true, SSID: "Net6", Channel: "1"}},
			"2.4GHz": {Band: "2.4GHz", Host: HostInfo{Enabled: true, SSID: "Net2", Channel: "6"}},
			"5GHz":   {Band: "5GHz", Host: HostInfo{Enabled: true, SSID: "Net5", Channel: "149"}},
		},
	}

	captureOutput := func() string {
		old := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		printWirelessTable(data)
		w.Close()
		os.Stdout = old
		var buf [4096]byte
		n, _ := r.Read(buf[:])
		return string(buf[:n])
	}

	out1 := captureOutput()
	out2 := captureOutput()

	if out1 != out2 {
		t.Error("printWirelessTable should produce deterministic output")
	}

	// Verify ordering: 2.4GHz should come before 5GHz which should come before 6GHz
	idx24 := strings.Index(out1, "2.4GHz")
	idx5 := strings.Index(out1, "5GHz")
	idx6 := strings.Index(out1, "6GHz")

	if idx24 >= idx5 || idx5 >= idx6 {
		t.Errorf("bands should be sorted: 2.4GHz(%d) < 5GHz(%d) < 6GHz(%d)", idx24, idx5, idx6)
	}
}

// ==================== CONFIG + ALIAS TESTS ====================

func TestLoadConfigMissing(t *testing.T) {
	// Override os.Executable to point somewhere without a config
	origDir, _ := os.Getwd()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	_, err := loadConfig()
	if err == nil {
		t.Error("loadConfig should fail when no config file exists")
	}
}

func TestLoadSaveAliases(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	aliases := map[string]string{
		"AA-BB-CC-DD-EE-FF": "My Phone",
		"11-22-33-44-55-66": "My PC",
	}

	saveAliases(aliases)

	loaded := loadAliases()
	if loaded["AA-BB-CC-DD-EE-FF"] != "My Phone" {
		t.Errorf("alias = %q, want %q", loaded["AA-BB-CC-DD-EE-FF"], "My Phone")
	}
	if loaded["11-22-33-44-55-66"] != "My PC" {
		t.Errorf("alias = %q, want %q", loaded["11-22-33-44-55-66"], "My PC")
	}
}

// ==================== DB PRUNE + CAPACITY TESTS ====================

func TestCheckDBSizeLimit(t *testing.T) {
	// With the test DB, it should be well within limits
	_ = setupTestDB(t)

	ok, size := checkDBSizeLimit()
	if !ok {
		t.Errorf("checkDBSizeLimit should return true for small DB, got false (size=%d)", size)
	}
}

func TestPruneOlderThan(t *testing.T) {
	db := setupTestDB(t)

	// Insert old records (60 days ago)
	oldTS := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	// Insert into all 4 tables
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		oldTS, "AA-BB-CC-DD-EE-FF", "OldDevice", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		newTS, "AA-BB-CC-DD-EE-FF", "NewDevice", 200, 100)

	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, newTS, "1.2.3.4")

	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, newTS, "Main", "master")

	db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")
	db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, newTS, "5GHz", "Net")

	// Prune records older than 30 days
	err := pruneOlderThan(db, 30)
	if err != nil {
		t.Fatalf("pruneOlderThan failed: %v", err)
	}

	// Verify only new records remain in each table
	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}
	for _, table := range tables {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 1 {
			t.Errorf("expected 1 row in %s after prune, got %d", table, count)
		}
	}
}

func TestSelectivePurge(t *testing.T) {
	db := setupTestDB(t)

	// Insert records spanning multiple dates
	dates := []string{
		time.Now().AddDate(0, 0, -10).Format(time.RFC3339),
		time.Now().AddDate(0, 0, -5).Format(time.RFC3339),
		time.Now().Format(time.RFC3339),
	}

	for _, ts := range dates {
		db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
			ts, "AA-BB-CC-DD-EE-FF", "Device", 100, 50)
	}

	// Prune records older than 7 days (should delete the -10 day record)
	err := pruneOlderThan(db, 7)
	if err != nil {
		t.Fatalf("pruneOlderThan failed: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 rows after prune (7 days), got %d", count)
	}
}

// ==================== CONFIG PATH TESTS ====================

func TestConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	got := configDir()
	want := filepath.Join(tmpDir, "deco")
	if got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

func TestConfigDirDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got := configDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "deco")
	if got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

func TestCfgPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	got := cfgPath("foo.json")
	want := filepath.Join(tmpDir, "deco", "foo.json")
	if got != want {
		t.Errorf("cfgPath(\"foo.json\") = %q, want %q", got, want)
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

	migrateIfNeeded()

	// Verify file moved to new location
	newPath := cfgPath("deco_config.json")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected file at %s after migration", newPath)
	}
	// Verify old file is gone
	if _, err := os.Stat("deco_config.json"); !os.IsNotExist(err) {
		t.Error("legacy file should be removed after migration")
	}
}

// ==================== PARSE PERIOD TESTS ====================

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
	}{
		{"today", "Today"},
		{"hour", "Last hour"},
		{"all", "All time"},
		{"", "All time"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			startTime, name := parsePeriod(tt.input)
			if name != tt.wantName {
				t.Errorf("parsePeriod(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
			if tt.input == "today" {
				now := time.Now()
				want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
				if !startTime.Equal(want) {
					t.Errorf("parsePeriod(\"today\") startTime = %v, want %v", startTime, want)
				}
			}
			if tt.input == "hour" {
				if time.Since(startTime) > 61*time.Minute || time.Since(startTime) < 59*time.Minute {
					t.Errorf("parsePeriod(\"hour\") startTime should be ~1 hour ago, got %v", startTime)
				}
			}
			if tt.input == "all" || tt.input == "" {
				if !startTime.IsZero() {
					t.Errorf("parsePeriod(%q) startTime should be zero, got %v", tt.input, startTime)
				}
			}
		})
	}
}

// ==================== PURGE BY DATE INTEGRATION TESTS ====================

func TestPurgeByDaysIntegration(t *testing.T) {
	db := setupTestDB(t)

	// Insert records at 3 different timestamps: 20 days ago, 5 days ago, now
	ts20 := time.Now().AddDate(0, 0, -20).Format(time.RFC3339)
	ts5 := time.Now().AddDate(0, 0, -5).Format(time.RFC3339)
	tsNow := time.Now().Format(time.RFC3339)

	for _, ts := range []string{ts20, ts5, tsNow} {
		db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
			ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
		db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, ts, "1.2.3.4")
		db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, ts, "Main", "master")
		db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, ts, "5GHz", "Net")
	}

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := purgeByDate(db, cutoff)
	if err != nil {
		t.Fatalf("purgeByDate failed: %v", err)
	}

	// Should have deleted 1 record per table (the 20-day-old one)
	if deleted != 4 {
		t.Errorf("purgeByDate deleted %d records, want 4", deleted)
	}

	// Verify 2 records remain in each table
	for _, table := range allTables {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 2 {
			t.Errorf("expected 2 rows in %s after purge, got %d", table, count)
		}
	}
}

func TestPurgeByDatePreservesRecent(t *testing.T) {
	db := setupTestDB(t)

	// Only insert recent records
	tsNow := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		tsNow, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := purgeByDate(db, cutoff)
	if err != nil {
		t.Fatalf("purgeByDate failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("purgeByDate deleted %d records, want 0 (all recent)", deleted)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row preserved, got %d", count)
	}
}

func TestPurgeByDateEmptyDB(t *testing.T) {
	db := setupTestDB(t)
	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := purgeByDate(db, cutoff)
	if err != nil {
		t.Fatalf("purgeByDate on empty DB failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("purgeByDate on empty DB deleted %d records, want 0", deleted)
	}
}

func TestPurgeByDateAllTables(t *testing.T) {
	db := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)

	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		oldTS, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")

	cutoff := time.Now().AddDate(0, 0, -7)
	deleted, err := purgeByDate(db, cutoff)
	if err != nil {
		t.Fatalf("purgeByDate failed: %v", err)
	}
	if deleted != 4 {
		t.Errorf("purgeByDate deleted %d records, want 4 (1 per table)", deleted)
	}

	for _, table := range allTables {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 rows in %s after full purge, got %d", table, count)
		}
	}
}

// ==================== KNOWN MACS TESTS ====================

func TestLoadKnownMACs(t *testing.T) {
	db := setupTestDB(t)

	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "AA-BB-CC-DD-EE-FF", "Dev1", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "11-22-33-44-55-66", "Dev2", 200, 100)
	// Duplicate MAC
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(time.RFC3339), "AA-BB-CC-DD-EE-FF", "Dev1Again", 50, 25)

	known := loadKnownMACs(db)
	if len(known) != 2 {
		t.Errorf("loadKnownMACs returned %d MACs, want 2", len(known))
	}
	if !known["AA-BB-CC-DD-EE-FF"] {
		t.Error("missing AA-BB-CC-DD-EE-FF in known MACs")
	}
	if !known["11-22-33-44-55-66"] {
		t.Error("missing 11-22-33-44-55-66 in known MACs")
	}
}

func TestNotifyNewMAC(t *testing.T) {
	// Just verify it doesn't panic
	notifyNewMAC("AA-BB-CC-DD-EE-FF", "TestDevice", "192.168.68.100", "")
}

// ==================== REPORT NETWORK/MESH TESTS ====================

func TestRunReportNetworkQuery(t *testing.T) {
	db := setupTestDB(t)

	// Insert network snapshots with different WAN IPs
	ts1 := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	ts2 := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	ts3 := time.Now().Format(time.RFC3339)

	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts1, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", 10.0, 20.0)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts2, "5.6.7.8", "5.6.7.1", "8.8.8.8", "8.8.4.4", 30.0, 40.0)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts3, "5.6.7.8", "5.6.7.1", "8.8.8.8", "8.8.4.4", 20.0, 30.0)

	// Query
	rows, err := db.Query(`SELECT timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2,
		COALESCE(cpu_percent, 0), COALESCE(mem_percent, 0)
		FROM network_snapshots ORDER BY timestamp`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	var entries []NetworkReportEntry
	for rows.Next() {
		var e NetworkReportEntry
		if err := rows.Scan(&e.Timestamp, &e.WANIP, &e.Gateway, &e.DNS1, &e.DNS2, &e.CPU, &e.Memory); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		entries = append(entries, e)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].WANIP != "1.2.3.4" {
		t.Errorf("first entry WAN IP = %q, want 1.2.3.4", entries[0].WANIP)
	}
	if entries[1].WANIP != "5.6.7.8" {
		t.Errorf("second entry WAN IP = %q, want 5.6.7.8", entries[1].WANIP)
	}
}

func TestRunReportMeshQuery(t *testing.T) {
	db := setupTestDB(t)

	ts1 := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	ts2 := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)

	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts1, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "online", "1.2.10")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts2, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "online", "1.2.10")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts1, "Slave", "slave", "192.168.71.250", "8C-90-2D-B5-5F-8C", "offline", "1.2.10")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts2, "Slave", "slave", "192.168.71.250", "8C-90-2D-B5-5F-8C", "online", "1.2.10")

	rows, err := db.Query(`SELECT timestamp, name, role, ip, mac, status, firmware
		FROM mesh_snapshots ORDER BY timestamp`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	var entries []MeshReportEntry
	for rows.Next() {
		var e MeshReportEntry
		if err := rows.Scan(&e.Timestamp, &e.Name, &e.Role, &e.IP, &e.MAC, &e.Status, &e.Firmware); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		entries = append(entries, e)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	// Verify the printMeshReport output
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printMeshReport(entries, "All time")

	w.Close()
	os.Stdout = old

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "MESH REPORT") {
		t.Error("output should contain MESH REPORT header")
	}
	if !strings.Contains(output, "Main") {
		t.Error("output should contain Main node")
	}
	if !strings.Contains(output, "100.0%") {
		t.Error("Main node should have 100% uptime")
	}
	if !strings.Contains(output, "50.0%") {
		t.Error("Slave node should have 50% uptime")
	}
}

func TestPrintNetworkReportEmpty(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printNetworkReport(nil, "Today")

	w.Close()
	os.Stdout = old

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "No network snapshots") {
		t.Error("empty report should show 'No network snapshots'")
	}
}

// ==================== COBRA SUBCOMMAND TESTS ====================

func TestReportSubcommands(t *testing.T) {
	// Verify report has network and mesh subcommands
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs([]string{"report", "--help"})
	rootCmd.Execute()

	w.Close()
	os.Stdout = old

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "network") {
		t.Error("report --help should list 'network' subcommand")
	}
	if !strings.Contains(output, "mesh") {
		t.Error("report --help should list 'mesh' subcommand")
	}
}

func TestMonitorNotifyFlag(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs([]string{"monitor", "--help"})
	rootCmd.Execute()

	w.Close()
	os.Stdout = old

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "--notify") {
		t.Error("monitor --help should show --notify flag")
	}
}

// ==================== ERROR PROPAGATION TESTS ====================

func TestConnectClientError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	_, _, err := connectClient()
	if err == nil {
		t.Error("connectClient() should return error when no config exists")
	}
}

func TestRunClientsNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	err := runClients(false, "", "")
	if err == nil {
		t.Error("runClients() should return error without config")
	}
}

// ==================== PURGE initDB TEST ====================

func TestPurgeUsesInitDB(t *testing.T) {
	testEnv(t)

	// Create an empty DB file (no tables) — runPurge with initDB should handle it
	dbFile := cfgPath("network_usage.db")
	if err := ensureConfigDir(); err != nil {
		t.Fatalf("ensureConfigDir: %v", err)
	}
	f, err := os.Create(dbFile)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	f.Close()

	// Should not panic — initDB will create tables
	if err := runPurge(true, "", 0); err != nil {
		t.Fatalf("runPurge failed: %v", err)
	}
}

// ==================== POLL LOOP TESTS ====================

func TestPollLoopShutdownOnCancel(t *testing.T) {
	testEnv(t)

	// Serve valid key-exchange and login responses so auth succeeds
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.String()
		if strings.Contains(path, "form=keys") {
			// Return RSA key pair (small test modulus)
			w.Write([]byte(`{"result":{"password":["` +
				"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF" +
				`","10001"]},"error_code":0}`))
		} else if strings.Contains(path, "form=auth") {
			w.Write([]byte(`{"result":{"key":["` +
				"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF" +
				`","10001"],"seq":1},"error_code":0}`))
		} else {
			w.Write([]byte(`{"error_code":0,"result":{}}`))
		}
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	config := &Config{Host: host, Password: "test"}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	// Auth will fail (login response not properly encrypted), which is fine —
	// we're testing that context cancellation causes a clean return.
	err := pollLoop(pollLoopConfig{
		interval:       1,
		label:          "Test",
		needsDB:        false,
		ctx:            ctx,
		maxFailures:    100, // high so we don't exit due to failures
		configOverride: config,
		work: func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error {
			return nil
		},
	})

	if err != nil {
		t.Errorf("pollLoop should return nil on context cancel, got: %v", err)
	}
}

func TestPollLoopMaxFailures(t *testing.T) {
	testEnv(t)

	// Auth will fail because login response isn't properly encrypted.
	// Each auth failure counts as a consecutive failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.String()
		if strings.Contains(path, "form=keys") {
			w.Write([]byte(`{"result":{"password":["` +
				"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF" +
				`","10001"]},"error_code":0}`))
		} else if strings.Contains(path, "form=auth") {
			w.Write([]byte(`{"result":{"key":["` +
				"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF" +
				`","10001"],"seq":1},"error_code":0}`))
		} else {
			w.Write([]byte(`{"error_code":0,"result":{}}`))
		}
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	config := &Config{Host: host, Password: "test"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := pollLoop(pollLoopConfig{
		interval:       1,
		label:          "Test",
		needsDB:        false,
		ctx:            ctx,
		maxFailures:    2,
		configOverride: config,
		work: func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error {
			return fmt.Errorf("deliberate failure")
		},
	})

	if err == nil {
		t.Error("pollLoop should return error after max failures")
	}
	if !strings.Contains(err.Error(), "consecutive failures") {
		t.Errorf("error should mention consecutive failures, got: %v", err)
	}
}

// ==================== COMPLETION TESTS ====================

func TestCompletionCmd(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Read pipe concurrently to avoid deadlock when output exceeds pipe buffer
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		buf.ReadFrom(r)
		close(done)
	}()

	rootCmd.SetArgs([]string{"completion", "bash"})
	err := rootCmd.Execute()

	w.Close()
	os.Stdout = old
	<-done

	if err != nil {
		t.Fatalf("completion bash failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "bash") && !strings.Contains(output, "completion") {
		t.Error("bash completion output should contain bash completion markers")
	}
}

func TestCompletionCmdInvalidShell(t *testing.T) {
	rootCmd.SetArgs([]string{"completion", "invalid"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("completion with invalid shell should return error")
	}
}

// ==================== CONNECTION BREAKDOWN TESTS ====================

func TestConnAbbrev(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"WiFi 2.4GHz", "2.4"},
		{"WiFi 5GHz", "5"},
		{"WiFi 6GHz", "6"},
		{"Wired", "W"},
		{"unknown", "unknown"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := connAbbrev(tt.input)
			if got != tt.want {
				t.Errorf("connAbbrev(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatConnBreakdown(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]int64
		want  string
	}{
		{"single", map[string]int64{"WiFi 5GHz": 10}, "5:100%"},
		{"empty", map[string]int64{}, ""},
		{"nil", nil, ""},
		{"multi", map[string]int64{"WiFi 5GHz": 80, "Wired": 20}, "5:80% W:20%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatConnBreakdown(tt.input)
			if got != tt.want {
				t.Errorf("formatConnBreakdown(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReportConnectionBreakdown(t *testing.T) {
	db := setupTestDB(t)

	ts1 := time.Now().Format(time.RFC3339)
	ts2 := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	ts3 := time.Now().Add(-2 * time.Minute).Format(time.RFC3339)

	// Same MAC, different connection types
	mac := "AA-BB-CC-DD-EE-FF"
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts1, mac, "Dev", "192.168.68.100", "WiFi 5GHz", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts2, mac, "Dev", "192.168.68.100", "WiFi 5GHz", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts3, mac, "Dev", "192.168.68.100", "Wired", 100, 50)

	// Query connection breakdown
	rows, err := db.Query(`SELECT mac, connection, COUNT(*) as samples
		FROM bandwidth_samples WHERE timestamp >= ? GROUP BY mac, connection`,
		time.Time{}.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	breakdown := map[string]map[string]int64{}
	for rows.Next() {
		var m, conn string
		var count int64
		if err := rows.Scan(&m, &conn, &count); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if breakdown[m] == nil {
			breakdown[m] = map[string]int64{}
		}
		breakdown[m][conn] = count
	}

	bd := breakdown[mac]
	if bd == nil {
		t.Fatal("no breakdown found for test MAC")
	}
	if bd["WiFi 5GHz"] != 2 {
		t.Errorf("WiFi 5GHz count = %d, want 2", bd["WiFi 5GHz"])
	}
	if bd["Wired"] != 1 {
		t.Errorf("Wired count = %d, want 1", bd["Wired"])
	}
}

// ==================== ESTIMATE INTERVAL TESTS ====================

func TestEstimateInterval(t *testing.T) {
	db := setupTestDB(t)

	// No data → fallback to 5
	got := estimateInterval(db, time.Time{})
	if got != 5 {
		t.Errorf("empty DB: estimateInterval = %d, want 5", got)
	}

	// Insert samples 60s apart
	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * 60 * time.Second).Format(time.RFC3339)
		db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
	}

	got = estimateInterval(db, time.Time{})
	if got != 60 {
		t.Errorf("60s intervals: estimateInterval = %d, want 60", got)
	}
}

func TestEstimateIntervalSingleSample(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)

	got := estimateInterval(db, time.Time{})
	if got != 5 {
		t.Errorf("single sample: estimateInterval = %d, want 5 (fallback)", got)
	}
}

// ==================== runReport END-TO-END TESTS ====================

func TestRunReportEndToEnd(t *testing.T) {
	db := setupTestDB(t)

	// Insert samples at known timestamps
	base := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * 5 * time.Second).Format(time.RFC3339)
		db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, "AA-BB-CC-DD-EE-FF", "TestPhone", "192.168.68.100", "WiFi 5GHz", "phone", 1000, 500)
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReport("today", false, false, "", "", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "BANDWIDTH USAGE REPORT") {
		t.Error("output should contain report header")
	}
	if !strings.Contains(output, "TestPhone") {
		t.Error("output should contain device name")
	}
	if !strings.Contains(output, "TOTAL") {
		t.Error("output should contain TOTAL row")
	}
}

func TestRunReportJSON(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDev", "192.168.68.100", "WiFi 5GHz", "phone", 100, 50)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReport("today", true, false, "", "", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport JSON failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"period"`) {
		t.Error("JSON output should contain period field")
	}
	if !strings.Contains(output, `"download_kb"`) {
		t.Error("JSON output should contain download_kb field")
	}
}

func TestRunReportWithNameFilter(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "MyPhone", "192.168.68.100", "WiFi 5GHz", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "11-22-33-44-55-66", "MyPC", "192.168.68.101", "Wired", 200, 100)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReport("today", false, false, "phone", "", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport with filter failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "MyPhone") {
		t.Error("filtered output should contain MyPhone")
	}
	if strings.Contains(output, "MyPC") {
		t.Error("filtered output should NOT contain MyPC")
	}
}

func TestRunReportWithMACFilter(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "DeviceA", "192.168.68.100", "WiFi 5GHz", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "11-22-33-44-55-66", "DeviceB", "192.168.68.101", "Wired", 200, 100)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReport("today", false, false, "", "AA-BB-CC-DD-EE-FF", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport with MAC filter failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "DeviceA") {
		t.Error("MAC-filtered output should contain DeviceA")
	}
	if strings.Contains(output, "DeviceB") {
		t.Error("MAC-filtered output should NOT contain DeviceB")
	}
}

func TestRunReportEmpty(t *testing.T) {
	setupTestDB(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReport("today", false, false, "", "", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport empty failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No data recorded") {
		t.Error("empty report should say 'No data recorded'")
	}
}

// ==================== runReportNetwork / runReportMesh END-TO-END ====================

func TestRunReportNetworkEndToEnd(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", 15.0, 40.0)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportNetwork("today", false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportNetwork failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "NETWORK REPORT") {
		t.Error("output should contain NETWORK REPORT")
	}
	if !strings.Contains(output, "1.2.3.4") {
		t.Error("output should contain WAN IP")
	}
}

func TestRunReportNetworkJSON(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", 15.0, 40.0)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportNetwork("today", true)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportNetwork JSON failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"wan_ip"`) {
		t.Error("JSON output should contain wan_ip field")
	}
}

func TestRunReportMeshEndToEnd(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "online", "1.2.10")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportMesh("today", false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportMesh failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "MESH REPORT") {
		t.Error("output should contain MESH REPORT")
	}
	if !strings.Contains(output, "Main") {
		t.Error("output should contain node name")
	}
}

func TestRunReportMeshJSON(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, status, firmware)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "online", "1.2.10")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportMesh("today", true)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportMesh JSON failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"name"`) {
		t.Error("JSON output should contain name field")
	}
}

// ==================== runAlias TESTS ====================

func TestRunAliasListEmpty(t *testing.T) {
	testEnv(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runAlias(false, []string{})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runAlias list empty failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No aliases set") {
		t.Error("empty alias list should say 'No aliases set'")
	}
}

func TestRunAliasSetAndList(t *testing.T) {
	testEnv(t)

	err := runAlias(false, []string{"AA-BB-CC-DD-EE-FF", "My Phone"})
	if err != nil {
		t.Fatalf("runAlias set failed: %v", err)
	}

	// List and verify
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runAlias(false, []string{})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runAlias list failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "AA-BB-CC-DD-EE-FF") {
		t.Error("alias list should contain the MAC")
	}
	if !strings.Contains(output, "My Phone") {
		t.Error("alias list should contain the alias name")
	}
}

func TestRunAliasRemove(t *testing.T) {
	testEnv(t)

	// Set then remove
	runAlias(false, []string{"AA-BB-CC-DD-EE-FF", "My Phone"})
	err := runAlias(true, []string{"AA-BB-CC-DD-EE-FF"})
	if err != nil {
		t.Fatalf("runAlias remove failed: %v", err)
	}

	// Verify removed
	aliases := loadAliases()
	if _, ok := aliases["AA-BB-CC-DD-EE-FF"]; ok {
		t.Error("alias should be removed")
	}
}

func TestRunAliasRemoveNotFound(t *testing.T) {
	testEnv(t)

	err := runAlias(true, []string{"AA-BB-CC-DD-EE-FF"})
	if err == nil {
		t.Error("removing non-existent alias should return error")
	}
}

func TestRunAliasInvalidMAC(t *testing.T) {
	testEnv(t)

	err := runAlias(false, []string{"not-a-mac", "name"})
	if err == nil {
		t.Error("setting alias with invalid MAC should return error")
	}
}

func TestRunAliasRemoveInvalidMAC(t *testing.T) {
	testEnv(t)

	err := runAlias(true, []string{"not-a-mac"})
	if err == nil {
		t.Error("removing alias with invalid MAC should return error")
	}
}

func TestRunAliasRemoveNoArgs(t *testing.T) {
	testEnv(t)

	err := runAlias(true, []string{})
	if err == nil {
		t.Error("remove with no args should return error")
	}
}

func TestRunAliasTooFewArgs(t *testing.T) {
	testEnv(t)

	err := runAlias(false, []string{"AA-BB-CC-DD-EE-FF"})
	if err == nil {
		t.Error("alias with only MAC (no name) should return error")
	}
}

// ==================== runBlock / runUnblock TESTS ====================

func TestRunBlockInvalidMAC(t *testing.T) {
	err := runBlock("not-a-mac")
	if err == nil {
		t.Error("runBlock with invalid MAC should return error")
	}
	if !strings.Contains(err.Error(), "invalid MAC") {
		t.Errorf("error should mention invalid MAC, got: %v", err)
	}
}

func TestRunUnblockInvalidMAC(t *testing.T) {
	err := runUnblock("not-a-mac")
	if err == nil {
		t.Error("runUnblock with invalid MAC should return error")
	}
	if !strings.Contains(err.Error(), "invalid MAC") {
		t.Errorf("error should mention invalid MAC, got: %v", err)
	}
}

// ==================== runStatus TESTS ====================

func TestRunStatusNoDB(t *testing.T) {
	testEnv(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runStatus(false); err != nil {
		t.Fatalf("runStatus failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No database found") {
		t.Error("runStatus with no DB should say 'No database found'")
	}
}

func TestRunStatusWithData(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev1", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, ts, "11-22-33-44-55-66", "Dev2", 200, 100)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runStatus(false); err != nil {
		t.Fatalf("runStatus failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Total samples: 2") {
		t.Errorf("runStatus should show 2 total samples, got: %s", output)
	}
	if !strings.Contains(output, "Unique devices: 2") {
		t.Errorf("runStatus should show 2 unique devices, got: %s", output)
	}
	if !strings.Contains(output, "Database:") {
		t.Error("runStatus should show database path")
	}
}

// ==================== runPurge TESTS ====================

func TestRunPurgeNoDB(t *testing.T) {
	testEnv(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runPurge(true, "", 0); err != nil {
		t.Fatalf("runPurge failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No database found") {
		t.Error("purge with no DB should say 'No database found'")
	}
}

func TestRunPurgeForceAll(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "Dev", 100, 50)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, ts, "1.2.3.4")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runPurge(true, "", 0); err != nil {
		t.Fatalf("runPurge failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Purged all records") {
		t.Errorf("force purge should say 'Purged all records', got: %s", output)
	}

	// Verify tables are empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 0 {
		t.Errorf("bandwidth_samples should be empty after purge, got %d", count)
	}
}

func TestRunPurgeByDays(t *testing.T) {
	db := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, oldTS, "AA-BB-CC-DD-EE-FF", "Old", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, newTS, "AA-BB-CC-DD-EE-FF", "New", 200, 100)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runPurge(true, "", 7); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("runPurge failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Purged") {
		t.Errorf("purge by days should say 'Purged', got: %s", output)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 1 {
		t.Errorf("should have 1 record remaining after purge, got %d", count)
	}
}

func TestRunPurgeByBefore(t *testing.T) {
	db := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, oldTS, "AA-BB-CC-DD-EE-FF", "Old", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, newTS, "AA-BB-CC-DD-EE-FF", "New", 200, 100)

	beforeDate := time.Now().AddDate(0, 0, -7).Format("2006-01-02")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := runPurge(true, beforeDate, 0); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("runPurge failed: %v", err)
	}

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Purged") {
		t.Errorf("purge by date should say 'Purged', got: %s", output)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&count)
	if count != 1 {
		t.Errorf("should have 1 record remaining, got %d", count)
	}
}

func TestRunPurgeInvalidDate(t *testing.T) {
	_ = setupTestDB(t)

	err := runPurge(true, "not-a-date", 0)
	if err == nil {
		t.Fatal("runPurge with bad date should return an error")
	}
	if !strings.Contains(err.Error(), "invalid date") {
		t.Errorf("error should mention 'invalid date', got: %v", err)
	}
}

// ==================== countBeforeDate TESTS ====================

func TestCountBeforeDate(t *testing.T) {
	db := setupTestDB(t)

	oldTS := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	newTS := time.Now().Format(time.RFC3339)

	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, oldTS, "AA-BB-CC-DD-EE-FF", "Old", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?)`, newTS, "AA-BB-CC-DD-EE-FF", "New", 200, 100)
	db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip) VALUES (?, ?)`, oldTS, "1.2.3.4")
	db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role) VALUES (?, ?, ?)`, oldTS, "Main", "master")
	db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid) VALUES (?, ?, ?)`, oldTS, "5GHz", "Net")

	cutoff := time.Now().AddDate(0, 0, -7)
	count, err := countBeforeDate(db, cutoff)
	if err != nil {
		t.Fatalf("countBeforeDate failed: %v", err)
	}
	if count != 4 {
		t.Errorf("countBeforeDate = %d, want 4 (1 per table)", count)
	}
}

func TestCountBeforeDateEmpty(t *testing.T) {
	db := setupTestDB(t)

	cutoff := time.Now().AddDate(0, 0, -7)
	count, err := countBeforeDate(db, cutoff)
	if err != nil {
		t.Fatalf("countBeforeDate failed: %v", err)
	}
	if count != 0 {
		t.Errorf("countBeforeDate on empty DB = %d, want 0", count)
	}
}

// ==================== checkDBCapacity TESTS ====================

func TestCheckDBCapacity(t *testing.T) {
	db := setupTestDB(t)

	// Should not panic on a small DB
	checkDBCapacity(db)
}

// ==================== printJSON TESTS ====================

func TestPrintJSON(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printJSON(map[string]string{"key": "value"})

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"key": "value"`) {
		t.Errorf("printJSON output should contain key/value, got: %s", output)
	}
}

// ==================== printDBLimitError TESTS ====================

func TestPrintDBLimitError(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printDBLimitError(300*1024*1024*1024, "Poll")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "DATABASE SIZE LIMIT EXCEEDED") {
		t.Error("should contain limit exceeded message")
	}
	if !strings.Contains(output, "Poll") {
		t.Error("should contain the action label")
	}
}

// ==================== runVersion TESTS ====================

func TestRunVersion(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	runVersion()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, version) {
		t.Errorf("runVersion should print version %q, got: %s", version, output)
	}
}

// ==================== Cobra subcommand TESTS ====================

func TestCompletionSubcommand(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs([]string{"--help"})
	rootCmd.Execute()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "completion") {
		t.Error("root help should list completion subcommand")
	}
}

// ==================== NETWORK-DEPENDENT COMMAND ERROR TESTS ====================

func TestRunNetworkNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runNetwork(false); err == nil {
		t.Error("runNetwork should return error without config")
	}
}

func TestRunWirelessNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runWireless(false); err == nil {
		t.Error("runWireless should return error without config")
	}
}

func TestRunMeshNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runMesh(false); err == nil {
		t.Error("runMesh should return error without config")
	}
}

func TestRunAllNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runAll(); err == nil {
		t.Error("runAll should return error without config")
	}
}

func TestRunAPINoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runAPI("test", "{}"); err == nil {
		t.Error("runAPI should return error without config")
	}
}

func TestRunRebootNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runReboot(true); err == nil {
		t.Error("runReboot should return error without config")
	}
}

func TestRunBlockNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runBlock("AA-BB-CC-DD-EE-FF"); err == nil {
		t.Error("runBlock should return error without config")
	}
}

func TestRunUnblockNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runUnblock("AA-BB-CC-DD-EE-FF"); err == nil {
		t.Error("runUnblock should return error without config")
	}
}

func TestRunWatchNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runWatch(5, "", "", 10); err == nil {
		t.Error("runWatch should return error without config")
	}
}

func TestRunPollNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runPoll(5, 10); err == nil {
		t.Error("runPoll should return error without config")
	}
}

func TestRunMonitorNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := runMonitor(60, false, 0, "", 10); err == nil {
		t.Error("runMonitor should return error without config")
	}
}

// ==================== logError TESTS ====================

func TestLogError(t *testing.T) {
	origLevel := logLevel.Load()
	defer func() { logLevel.Store(origLevel) }()

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logLevel.Store(int32(LevelError))
	logError("test error message")

	w.Close()
	os.Stderr = oldStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "test error message") {
		t.Error("logError should output the message")
	}
	if !strings.Contains(output, "ERR") {
		t.Error("logError should contain ERR prefix")
	}
}

// ==================== filterClients with alias TESTS ====================

func TestFilterClientsByAlias(t *testing.T) {
	testEnv(t)

	// Set an alias
	aliases := map[string]string{"AA-BB-CC-DD-EE-FF": "FriendlyName"}
	saveAliases(aliases)

	data := &ClientList{
		Clients: []ClientInfo{
			{Name: "garbled_name", IP: "192.168.68.100", MAC: "AA-BB-CC-DD-EE-FF"},
			{Name: "OtherDevice", IP: "192.168.68.101", MAC: "11-22-33-44-55-66"},
		},
		Count: 2,
	}

	// Filter by alias name
	filtered := filterClients(data, "friendly", "")
	if len(filtered.Clients) != 1 {
		t.Fatalf("expected 1 client matching alias, got %d", len(filtered.Clients))
	}
	if filtered.Clients[0].MAC != "AA-BB-CC-DD-EE-FF" {
		t.Error("filtered client should match by alias")
	}
}

// ==================== printNetworkReport with data TESTS ====================

func TestPrintNetworkReportWithIPChanges(t *testing.T) {
	entries := []NetworkReportEntry{
		{Timestamp: "2026-03-08T10:00:00Z", WANIP: "1.2.3.4", Gateway: "1.2.3.1", DNS1: "8.8.8.8", DNS2: "8.8.4.4", CPU: 10, Memory: 20},
		{Timestamp: "2026-03-08T11:00:00Z", WANIP: "5.6.7.8", Gateway: "5.6.7.1", DNS1: "8.8.8.8", DNS2: "8.8.4.4", CPU: 30, Memory: 40},
		{Timestamp: "2026-03-08T12:00:00Z", WANIP: "5.6.7.8", Gateway: "5.6.7.1", DNS1: "8.8.8.8", DNS2: "8.8.4.4", CPU: 20, Memory: 30},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printNetworkReport(entries, "Today")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Should show both IPs in WAN history
	if !strings.Contains(output, "1.2.3.4") {
		t.Error("should contain first WAN IP")
	}
	if !strings.Contains(output, "5.6.7.8") {
		t.Error("should contain second WAN IP")
	}
	if !strings.Contains(output, "Performance") {
		t.Error("should contain Performance section")
	}
	// avg CPU = (10+30+20)/3 = 20
	if !strings.Contains(output, "20.0%") {
		t.Error("should contain avg CPU 20.0%")
	}
}

// ==================== CSV EXPORT TESTS ====================

func TestPrintReportCSV(t *testing.T) {
	testEnv(t)

	report := &Report{
		Period:          "Today",
		StartTime:       "2026-03-11T00:00:00Z",
		QueryTime:       "2026-03-11T12:00:00Z",
		IntervalSeconds: 60,
		TotalSamples:    100,
		Devices: []ReportDevice{
			{
				MAC:           "AA-BB-CC-DD-EE-FF",
				Name:          "TestDevice",
				IP:            "192.168.68.100",
				Connection:    "WiFi 5GHz",
				SampleCount:   50,
				TotalDownload: 1000,
				TotalUpload:   200,
			},
			{
				MAC:           "11-22-33-44-55-66",
				Name:          "Zero,Device",
				IP:            "192.168.68.101",
				Connection:    "Wired",
				SampleCount:   50,
				TotalDownload: 500,
				TotalUpload:   100,
			},
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printReportCSV(report)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Check header
	if !strings.HasPrefix(output, "mac,name,ip,connection,samples,download_kb,upload_kb,total_kb\n") {
		t.Errorf("expected CSV header, got: %s", output[:80])
	}
	// Check data row
	if !strings.Contains(output, "AA-BB-CC-DD-EE-FF,TestDevice,192.168.68.100") {
		t.Error("expected device row in CSV")
	}
	// Check comma-in-name is quoted
	if !strings.Contains(output, "\"Zero,Device\"") {
		t.Error("expected quoted name for comma-containing device name")
	}
	// Verify total calculation: 1000 * 60 = 60000 download, 200 * 60 = 12000 upload, total = 72000
	if !strings.Contains(output, "60000,12000,72000") {
		t.Error("expected correct KB totals in CSV")
	}
}

func TestRunReportCSV(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDev", "192.168.68.100", "WiFi 5GHz", "phone", 100, 50)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runReport("today", false, true, "", "", "")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport CSV failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.HasPrefix(output, "mac,name,") {
		t.Error("expected CSV header")
	}
	if !strings.Contains(output, "AA-BB-CC-DD-EE-FF") {
		t.Error("expected device MAC in CSV output")
	}
}

// ==================== TAG TESTS ====================

func TestLoadSaveTags(t *testing.T) {
	testEnv(t)

	tags := loadTags()
	if len(tags) != 0 {
		t.Error("expected empty tags initially")
	}

	tags["AA-BB-CC-DD-EE-FF"] = []string{"gaming", "kids"}
	tags["11-22-33-44-55-66"] = []string{"iot"}
	saveTags(tags)

	loaded := loadTags()
	if len(loaded) != 2 {
		t.Fatalf("expected 2 tagged devices, got %d", len(loaded))
	}
	if len(loaded["AA-BB-CC-DD-EE-FF"]) != 2 {
		t.Errorf("expected 2 tags for AA-BB, got %d", len(loaded["AA-BB-CC-DD-EE-FF"]))
	}
	if loaded["11-22-33-44-55-66"][0] != "iot" {
		t.Errorf("expected 'iot' tag, got %q", loaded["11-22-33-44-55-66"][0])
	}
}

func TestSaveTagsCleanup(t *testing.T) {
	testEnv(t)

	tags := map[string][]string{
		"AA-BB-CC-DD-EE-FF": {"gaming"},
		"11-22-33-44-55-66": {}, // empty should be cleaned up
	}
	saveTags(tags)

	loaded := loadTags()
	if len(loaded) != 1 {
		t.Errorf("expected 1 tagged device after cleanup, got %d", len(loaded))
	}
}

func TestRunAliasTag(t *testing.T) {
	testEnv(t)

	err := runAliasTag([]string{"AA-BB-CC-DD-EE-FF", "gaming"})
	if err != nil {
		t.Fatalf("runAliasTag failed: %v", err)
	}

	tags := loadTags()
	if len(tags["AA-BB-CC-DD-EE-FF"]) != 1 || tags["AA-BB-CC-DD-EE-FF"][0] != "gaming" {
		t.Errorf("expected tag 'gaming', got %v", tags["AA-BB-CC-DD-EE-FF"])
	}

	// Duplicate tag should be idempotent
	err = runAliasTag([]string{"AA-BB-CC-DD-EE-FF", "gaming"})
	if err != nil {
		t.Fatalf("duplicate tag failed: %v", err)
	}
	tags = loadTags()
	if len(tags["AA-BB-CC-DD-EE-FF"]) != 1 {
		t.Error("duplicate tag should not add second entry")
	}
}

func TestRunAliasTagInvalidMAC(t *testing.T) {
	err := runAliasTag([]string{"invalid", "gaming"})
	if err == nil {
		t.Error("expected error for invalid MAC")
	}
}

func TestRunAliasUntag(t *testing.T) {
	testEnv(t)

	// Set up a tag first
	tags := map[string][]string{
		"AA-BB-CC-DD-EE-FF": {"gaming", "kids"},
	}
	saveTags(tags)

	err := runAliasUntag([]string{"AA-BB-CC-DD-EE-FF", "gaming"})
	if err != nil {
		t.Fatalf("runAliasUntag failed: %v", err)
	}

	loaded := loadTags()
	if len(loaded["AA-BB-CC-DD-EE-FF"]) != 1 || loaded["AA-BB-CC-DD-EE-FF"][0] != "kids" {
		t.Errorf("expected only 'kids' tag remaining, got %v", loaded["AA-BB-CC-DD-EE-FF"])
	}
}

func TestRunAliasUntagNotFound(t *testing.T) {
	testEnv(t)

	err := runAliasUntag([]string{"AA-BB-CC-DD-EE-FF", "nonexistent"})
	if err == nil {
		t.Error("expected error for non-existent tag")
	}
}

func TestRunAliasTags(t *testing.T) {
	testEnv(t)

	// Set up some tags and aliases
	aliases := map[string]string{"AA-BB-CC-DD-EE-FF": "Xbox"}
	saveAliases(aliases)
	tags := map[string][]string{
		"AA-BB-CC-DD-EE-FF": {"gaming"},
		"11-22-33-44-55-66": {"iot"},
	}
	saveTags(tags)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runAliasTags()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runAliasTags failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "[gaming]") {
		t.Error("expected [gaming] group header")
	}
	if !strings.Contains(output, "Xbox") {
		t.Error("expected alias name 'Xbox' in tags output")
	}
	if !strings.Contains(output, "[iot]") {
		t.Error("expected [iot] group header")
	}
}

func TestRunReportWithGroup(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "Xbox", "192.168.68.71", "WiFi 5GHz", "console", 5000, 1000)
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "11-22-33-44-55-66", "Roomba", "192.168.68.80", "WiFi 2.4GHz", "iot", 10, 5)

	// Tag Xbox as "gaming"
	tagMap := map[string][]string{"AA-BB-CC-DD-EE-FF": {"gaming"}}
	saveTags(tagMap)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runReport("today", false, false, "", "", "gaming")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReport with group failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Xbox") {
		t.Error("expected Xbox in group-filtered report")
	}
	if strings.Contains(output, "Roomba") {
		t.Error("Roomba should be excluded by group filter")
	}
}

// ==================== NEW FLAG TESTS ====================

func TestChatCmdShowContextFlag(t *testing.T) {
	cmd := chatCmd()
	sc, err := cmd.Flags().GetBool("show-context")
	if err != nil {
		t.Fatalf("show-context flag error: %v", err)
	}
	if sc != false {
		t.Error("expected default show-context=false")
	}
}

func TestMonitorCmdAlertFlag(t *testing.T) {
	cmd := monitorCmd()
	alert, err := cmd.Flags().GetInt("alert")
	if err != nil {
		t.Fatalf("alert flag error: %v", err)
	}
	if alert != 0 {
		t.Error("expected default alert=0")
	}
}

func TestReportCmdCSVFlag(t *testing.T) {
	cmd := reportCmd()
	csv, err := cmd.Flags().GetBool("csv")
	if err != nil {
		t.Fatalf("csv flag error: %v", err)
	}
	if csv != false {
		t.Error("expected default csv=false")
	}
}

func TestReportCmdGroupFlag(t *testing.T) {
	cmd := reportCmd()
	group, err := cmd.Flags().GetString("group")
	if err != nil {
		t.Fatalf("group flag error: %v", err)
	}
	if group != "" {
		t.Error("expected default group=empty")
	}
}

func TestAliasTagSubcommands(t *testing.T) {
	cmd := aliasCmd()
	found := map[string]bool{}
	for _, sub := range cmd.Commands() {
		found[sub.Name()] = true
	}
	for _, name := range []string{"tag", "untag", "tags"} {
		if !found[name] {
			t.Errorf("expected subcommand %q on alias cmd", name)
		}
	}
}

// ==================== WEBHOOK TESTS ====================

func TestSendWebhook(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	sendWebhook(srv.URL, WebhookPayload{
		Event:     "new_device",
		Timestamp: "2026-03-11T12:00:00Z",
		Text:      "test message",
		Data:      NewDeviceEvent{MAC: "AA-BB-CC-DD-EE-FF", Name: "Dev", IP: "192.168.68.100"},
	})

	if len(received) == 0 {
		t.Fatal("webhook server received no data")
	}
	if !strings.Contains(string(received), `"event":"new_device"`) {
		t.Errorf("expected event field in payload, got: %s", received)
	}
	if !strings.Contains(string(received), `"mac":"AA-BB-CC-DD-EE-FF"`) {
		t.Errorf("expected MAC in payload, got: %s", received)
	}
}

func TestSendWebhookEmptyURL(t *testing.T) {
	// Should be a no-op, not panic
	sendWebhook("", WebhookPayload{Event: "test"})
}

func TestSendWebhookBadURL(t *testing.T) {
	// Should log warning, not panic
	sendWebhook("http://127.0.0.1:1", WebhookPayload{Event: "test"})
}

func TestMonitorCmdWebhookFlag(t *testing.T) {
	cmd := monitorCmd()
	webhook, err := cmd.Flags().GetString("webhook")
	if err != nil {
		t.Fatalf("webhook flag error: %v", err)
	}
	if webhook != "" {
		t.Error("expected default webhook=empty")
	}
}

// ==================== DEVICE REPORT TESTS ====================

func TestResolveDeviceMAC(t *testing.T) {
	db := setupTestDB(t)

	// Set up alias
	aliases := map[string]string{"AA-BB-CC-DD-EE-FF": "MyPhone"}
	saveAliases(aliases)

	// Insert sample with a name
	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "garbled_name", "192.168.68.100", "WiFi 5GHz", 100, 50)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "11-22-33-44-55-66", "Xbox", "192.168.68.71", "Wired", 200, 100)

	tests := []struct {
		name    string
		input   string
		wantMAC string
		wantErr bool
	}{
		{"by MAC", "AA-BB-CC-DD-EE-FF", "AA-BB-CC-DD-EE-FF", false},
		{"by MAC lowercase", "aa-bb-cc-dd-ee-ff", "AA-BB-CC-DD-EE-FF", false},
		{"by alias substring", "MyPhone", "AA-BB-CC-DD-EE-FF", false},
		{"by name substring", "Xbox", "11-22-33-44-55-66", false},
		{"not found", "nonexistent_device", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac, err := resolveDeviceMAC(db, tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mac != tt.wantMAC {
				t.Errorf("resolveDeviceMAC(%q) = %q, want %q", tt.input, mac, tt.wantMAC)
			}
		})
	}
}

func TestRunReportDevice(t *testing.T) {
	db := setupTestDB(t)

	// Insert samples for a device across multiple hours
	base := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * 30 * time.Minute).Format(time.RFC3339)
		db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "TestPhone", "192.168.68.100", "WiFi 5GHz", 100, 50)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportDevice("AA-BB-CC-DD-EE-FF", "today", false, false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportDevice failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "DEVICE REPORT") {
		t.Error("output should contain DEVICE REPORT header")
	}
	if !strings.Contains(output, "TestPhone") {
		t.Error("output should contain device name")
	}
	if !strings.Contains(output, "AA-BB-CC-DD-EE-FF") {
		t.Error("output should contain device MAC")
	}
}

func TestRunReportDeviceJSON(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "TestDev", "192.168.68.100", "WiFi 5GHz", 100, 50)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportDevice("AA-BB-CC-DD-EE-FF", "today", true, false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportDevice JSON failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"timeline"`) {
		t.Error("JSON output should contain timeline field")
	}
	if !strings.Contains(output, `"mac"`) {
		t.Error("JSON output should contain mac field")
	}
}

func TestRunReportDeviceNotFound(t *testing.T) {
	setupTestDB(t)

	err := runReportDevice("nonexistent", "today", false, false)
	if err == nil {
		t.Error("expected error for nonexistent device")
	}
}

func TestRunReportDeviceCSV(t *testing.T) {
	db := setupTestDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ts, "AA-BB-CC-DD-EE-FF", "TestDev", "192.168.68.100", "WiFi 5GHz", 100, 50)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReportDevice("AA-BB-CC-DD-EE-FF", "today", false, true)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("runReportDevice CSV failed: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.HasPrefix(output, "hour,download_kb,upload_kb,samples\n") {
		t.Errorf("expected CSV header, got: %s", output)
	}
}

func TestReportDeviceSubcommand(t *testing.T) {
	cmd := reportCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "device" {
			found = true
		}
	}
	if !found {
		t.Error("report cmd should have 'device' subcommand")
	}
}


