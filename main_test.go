package main

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

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

// ==================== FLAG TESTS ====================

func TestHasFlag(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags []string
		want  bool
	}{
		{"flag present", []string{"cmd", "--json"}, []string{"--json", "-j"}, true},
		{"short flag present", []string{"cmd", "-j"}, []string{"--json", "-j"}, true},
		{"flag absent", []string{"cmd", "--verbose"}, []string{"--json", "-j"}, false},
		{"multiple flags some present", []string{"cmd", "--verbose", "--json"}, []string{"--json"}, true},
		{"empty args", []string{"cmd"}, []string{"--json"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origArgs := os.Args
			defer func() { os.Args = origArgs }()
			os.Args = tt.args

			got := hasFlag(tt.flags...)
			if got != tt.want {
				t.Errorf("hasFlag(%v) with args %v = %v, want %v", tt.flags, tt.args, got, tt.want)
			}
		})
	}
}

func TestGetFlagInt(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		long       string
		short      string
		defaultVal int
		want       int
	}{
		{"flag with value", []string{"cmd", "--interval", "30"}, "--interval", "-i", 10, 30},
		{"short flag with value", []string{"cmd", "-i", "60"}, "--interval", "-i", 10, 60},
		{"flag missing returns default", []string{"cmd"}, "--interval", "-i", 10, 10},
		{"flag with invalid value returns default", []string{"cmd", "--interval", "abc"}, "--interval", "-i", 10, 10},
		{"flag with zero value returns default", []string{"cmd", "--interval", "0"}, "--interval", "-i", 10, 10},
		{"flag at end without value returns default", []string{"cmd", "--interval"}, "--interval", "-i", 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origArgs := os.Args
			defer func() { os.Args = origArgs }()
			os.Args = tt.args

			got := getFlagInt(tt.long, tt.short, tt.defaultVal)
			if got != tt.want {
				t.Errorf("getFlagInt(%q, %q, %d) with args %v = %d, want %d",
					tt.long, tt.short, tt.defaultVal, tt.args, got, tt.want)
			}
		})
	}
}

func TestGetFlagString(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		long  string
		short string
		want  string
	}{
		{"flag with value", []string{"cmd", "--name", "xbox"}, "--name", "-n", "xbox"},
		{"short flag with value", []string{"cmd", "-n", "xbox"}, "--name", "-n", "xbox"},
		{"flag missing returns empty", []string{"cmd"}, "--name", "-n", ""},
		{"flag at end without value returns empty", []string{"cmd", "--name"}, "--name", "-n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origArgs := os.Args
			defer func() { os.Args = origArgs }()
			os.Args = tt.args

			got := getFlagString(tt.long, tt.short)
			if got != tt.want {
				t.Errorf("getFlagString(%q, %q) with args %v = %q, want %q",
					tt.long, tt.short, tt.args, got, tt.want)
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
