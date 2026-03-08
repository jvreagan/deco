package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
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
	origLevel := logLevel
	defer func() { logLevel = origLevel }()

	logLevel = LevelWarn
	SetVerbose(true)
	if logLevel != LevelDebug {
		t.Errorf("SetVerbose(true): logLevel = %d, want %d", logLevel, LevelDebug)
	}

	// SetVerbose(false) should not change level
	logLevel = LevelWarn
	SetVerbose(false)
	if logLevel != LevelWarn {
		t.Errorf("SetVerbose(false): logLevel = %d, want %d", logLevel, LevelWarn)
	}
}

func TestLogLevels(t *testing.T) {
	origLevel := logLevel
	defer func() { logLevel = origLevel }()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// At default level (Warn), debug should be hidden
	logLevel = LevelWarn
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

	logLevel = LevelDebug
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

// ==================== DATABASE SCHEMA TESTS ====================

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp("", "network_test_*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	origDBPath := dbPath
	dbPath = tmp.Name()
	t.Cleanup(func() { dbPath = origDBPath })

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
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
	tmp, err := os.CreateTemp("", "network_test_*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	origDBPath := dbPath
	dbPath = tmp.Name()
	t.Cleanup(func() { dbPath = origDBPath })

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
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	_, err := loadConfig()
	if err == nil {
		t.Error("loadConfig should fail when no config file exists")
	}
}

func TestLoadSaveAliases(t *testing.T) {
	// Save aliases to a temp dir as cwd
	origDir, _ := os.Getwd()
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	aliases := map[string]string{
		"AA-BB-CC-DD-EE-FF": "My Phone",
		"11-22-33-44-55-66": "My PC",
	}

	// Write aliases to cwd (saveAliases tries exe dir first, falls back to cwd)
	data, _ := json.MarshalIndent(aliases, "", "  ")
	os.WriteFile("deco_aliases.json", data, 0600)

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
