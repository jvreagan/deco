package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func runSetup() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Router IP [192.168.68.1]: ")
	scanner.Scan()
	host := strings.TrimSpace(scanner.Text())
	if host == "" {
		host = "192.168.68.1"
	}

	fmt.Print("Admin password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // ReadPassword doesn't echo newline
	if err != nil {
		// Fallback to plain text if terminal is not available
		scanner.Scan()
		passwordBytes = []byte(strings.TrimSpace(scanner.Text()))
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		fmt.Println("Error: password is required")
		os.Exit(1)
	}

	config := Config{Host: host, Password: password}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	exe, _ := os.Executable()
	configPath := filepath.Join(filepath.Dir(exe), "deco_config.json")

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		// Fall back to current directory
		configPath = "deco_config.json"
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			fmt.Printf("Error writing config: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("\nConfig saved to %s\n", configPath)
	fmt.Println("Try it out: deco clients")
}

func runVersion() {
	fmt.Println(version)
}

func runClients(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Apply filters
	nameFilter := getFlagString("--name", "-n")
	macFilter := getFlagString("--mac", "-m")
	if nameFilter != "" || macFilter != "" {
		data = filterClients(data, nameFilter, macFilter)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printClientsTable(data)
	}
}

func runNetwork(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetNetwork()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printNetworkTable(data)
	}
}

func runWireless(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetWireless()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printWirelessTable(data)
	}
}

func runMesh(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetMesh()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printMeshTable(data)
	}
}

func runAll() {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	network, _ := client.GetNetwork()
	wireless, _ := client.GetWireless()
	mesh, _ := client.GetMesh()
	clients, _ := client.GetClients()

	data := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"router":    config.Host,
		"network":   network,
		"wireless":  wireless,
		"mesh":      mesh,
		"clients":   clients,
	}

	printJSON(data)
}

func runAPI(endpoint, body string) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	var reqData map[string]interface{}
	if err := json.Unmarshal([]byte(body), &reqData); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid JSON body: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Request(endpoint, reqData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "API error: %v\n", err)
		os.Exit(1)
	}

	printJSON(resp)
}

func runPoll(interval int) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ok, size := checkDBSizeLimit()
	if !ok {
		fmt.Println(strings.Repeat("=", 70))
		fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
		fmt.Println(strings.Repeat("=", 70))
		fmt.Printf("Current size: %s\n", formatSize(size))
		fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
		fmt.Println("\nPolling cannot continue. Please run 'purge' to clear the database:")
		fmt.Println("  deco purge")
		fmt.Println(strings.Repeat("=", 70))
		os.Exit(1)
	}

	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting bandwidth monitor (polling every %ds)\n", interval)
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
	fmt.Println("Press Ctrl+C to stop")

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	running := true
	sampleCount := 0

	go func() {
		<-sigChan
		fmt.Println("\nStopping monitor...")
		running = false
	}()

	for running {
		// Check size limit
		ok, size := checkDBSizeLimit()
		if !ok {
			fmt.Println(strings.Repeat("=", 70))
			fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
			fmt.Println(strings.Repeat("=", 70))
			fmt.Printf("Current size: %s\n", formatSize(size))
			fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
			fmt.Println("\nPolling stopped. Please run 'purge' to clear the database:")
			fmt.Println("  deco purge")
			fmt.Println(strings.Repeat("=", 70))
			return
		}

		start := time.Now()

		client := NewDecoClient(config.Host, config.Password)
		if err := client.Authorize(); err != nil {
			fmt.Printf("Error connecting: %v, retrying...\n", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		data, err := client.GetClients()
		client.Logout()

		if err != nil {
			fmt.Printf("Error getting clients: %v, retrying...\n", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		clients := data["clients"].([]map[string]interface{})
		timestamp := time.Now().Format(time.RFC3339)

		// Record samples
		for _, c := range clients {
			_, err := db.Exec(`
				INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, timestamp, c["mac"], c["name"], c["ip"], c["connection"], c["type"], c["download_kbps"], c["upload_kbps"])
			if err != nil {
				fmt.Printf("DB error: %v\n", err)
			}
		}

		sampleCount++

		// Show activity
		activeCount := 0
		totalDown := 0
		totalUp := 0
		for _, c := range clients {
			down := toInt(c["download_kbps"])
			up := toInt(c["upload_kbps"])
			if down > 0 || up > 0 {
				activeCount++
			}
			totalDown += down
			totalUp += up
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Sample #%d: %d clients, %d active, %d KB/s down %d KB/s up\n",
			ts, sampleCount, len(clients), activeCount, totalDown, totalUp)

		// Wait for next interval
		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 && running {
			time.Sleep(sleepTime)
		}
	}

	fmt.Printf("Monitor stopped. Recorded %d samples.\n", sampleCount)
}

func runMonitor(interval int) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ok, size := checkDBSizeLimit()
	if !ok {
		fmt.Println(strings.Repeat("=", 70))
		fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
		fmt.Println(strings.Repeat("=", 70))
		fmt.Printf("Current size: %s\n", formatSize(size))
		fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
		fmt.Println("\nMonitoring cannot continue. Please run 'purge' to clear the database:")
		fmt.Println("  deco purge")
		fmt.Println(strings.Repeat("=", 70))
		os.Exit(1)
	}

	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting full network monitor (polling every %ds)\n", interval)
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
	fmt.Println("Press Ctrl+C to stop")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	running := true
	cycleCount := 0

	go func() {
		<-sigChan
		fmt.Println("\nStopping monitor...")
		running = false
	}()

	for running {
		ok, size := checkDBSizeLimit()
		if !ok {
			fmt.Println(strings.Repeat("=", 70))
			fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
			fmt.Println(strings.Repeat("=", 70))
			fmt.Printf("Current size: %s\n", formatSize(size))
			fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
			fmt.Println("\nMonitoring stopped. Please run 'purge' to clear the database:")
			fmt.Println("  deco purge")
			fmt.Println(strings.Repeat("=", 70))
			return
		}

		start := time.Now()

		client := NewDecoClient(config.Host, config.Password)
		if err := client.Authorize(); err != nil {
			fmt.Printf("[%s] Error connecting: %v, retrying next cycle...\n", time.Now().Format("15:04:05"), err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		clientData, clientErr := client.GetClients()
		networkData, networkErr := client.GetNetwork()
		wirelessData, wirelessErr := client.GetWireless()
		meshData, meshErr := client.GetMesh()
		client.Logout()

		timestamp := time.Now().Format(time.RFC3339)
		cycleCount++

		// Insert bandwidth_samples (same as poll)
		clientCount := 0
		if clientErr == nil {
			if clients, ok := clientData["clients"].([]map[string]interface{}); ok {
				clientCount = len(clients)
				for _, c := range clients {
					db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, c["mac"], c["name"], c["ip"], c["connection"], c["type"], c["download_kbps"], c["upload_kbps"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting clients: %v\n", time.Now().Format("15:04:05"), clientErr)
		}

		// Insert network_snapshot
		var cpuPct, memPct interface{}
		if networkErr == nil {
			wan := getMap(networkData, "wan")
			lan := getMap(networkData, "lan")
			perf := getMap(networkData, "performance")

			cpuPct = perf["cpu_percent"]
			memPct = perf["mem_percent"]

			var dns1, dns2 interface{}
			if dnsSlice, ok := wan["dns"].([]interface{}); ok && len(dnsSlice) >= 2 {
				dns1 = dnsSlice[0]
				dns2 = dnsSlice[1]
			}

			db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timestamp, wan["ip"], wan["gateway"], dns1, dns2, lan["ip"], lan["netmask"], cpuPct, memPct)
		} else {
			fmt.Printf("[%s] Error getting network: %v\n", time.Now().Format("15:04:05"), networkErr)
		}

		// Insert mesh_snapshots
		meshCount := 0
		if meshErr == nil {
			if devices, ok := meshData["devices"].([]map[string]interface{}); ok {
				meshCount = len(devices)
				for _, d := range devices {
					db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, d["name"], d["role"], d["ip"], d["mac"], d["model"], d["firmware"], d["status"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting mesh: %v\n", time.Now().Format("15:04:05"), meshErr)
		}

		// Insert wireless_snapshots
		if wirelessErr == nil {
			if bands, ok := wirelessData["bands"].(map[string]interface{}); ok {
				for bandName, b := range bands {
					if b == nil {
						continue
					}
					band := b.(map[string]interface{})
					host := getMap(band, "host")
					guest := getMap(band, "guest")

					hostEnabled := 0
					if e, ok := host["enabled"].(bool); ok && e {
						hostEnabled = 1
					}
					guestEnabled := 0
					if e, ok := guest["enabled"].(bool); ok && e {
						guestEnabled = 1
					}

					db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, bandName, host["ssid"], host["channel"], host["channel_width"], hostEnabled, guestEnabled, guest["ssid"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting wireless: %v\n", time.Now().Format("15:04:05"), wirelessErr)
		}

		// Print summary line
		cpuStr := "?"
		memStr := "?"
		if cpuPct != nil {
			cpuStr = fmt.Sprintf("%.0f%%", toFloat(cpuPct))
		}
		if memPct != nil {
			memStr = fmt.Sprintf("%.0f%%", toFloat(memPct))
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Cycle #%d: %d clients, CPU %s, Mem %s, %d mesh nodes\n",
			ts, cycleCount, clientCount, cpuStr, memStr, meshCount)

		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 && running {
			time.Sleep(sleepTime)
		}
	}

	fmt.Printf("Monitor stopped. Completed %d cycles.\n", cycleCount)
}

func runReport(period string, jsonOut bool) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var startTime time.Time
	var periodName string

	switch period {
	case "today":
		now := time.Now()
		startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		periodName = "Today"
	case "hour":
		startTime = time.Now().Add(-1 * time.Hour)
		periodName = "Last hour"
	default:
		startTime = time.Time{}
		periodName = "All time"
	}

	query := `
		SELECT
			mac, name, ip, connection, device_type,
			COUNT(*) as sample_count,
			SUM(download_kbps) as total_download,
			SUM(upload_kbps) as total_upload,
			MAX(download_kbps) as max_download,
			MAX(upload_kbps) as max_upload
		FROM bandwidth_samples
		WHERE timestamp >= ?
		GROUP BY mac
		ORDER BY (total_download + total_upload) DESC
	`

	rows, err := db.Query(query, startTime.Format(time.RFC3339))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Query error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	devices := []map[string]interface{}{}
	for rows.Next() {
		var mac, name, ip, connection, deviceType sql.NullString
		var sampleCount, totalDown, totalUp, maxDown, maxUp int64

		err := rows.Scan(&mac, &name, &ip, &connection, &deviceType,
			&sampleCount, &totalDown, &totalUp, &maxDown, &maxUp)
		if err != nil {
			continue
		}

		devices = append(devices, map[string]interface{}{
			"mac":            mac.String,
			"name":           name.String,
			"ip":             ip.String,
			"connection":     connection.String,
			"device_type":    deviceType.String,
			"sample_count":   sampleCount,
			"total_download": totalDown,
			"total_upload":   totalUp,
			"max_download":   maxDown,
			"max_upload":     maxUp,
		})
	}

	// Apply filters
	nameFilter := getFlagString("--name", "-n")
	macFilter := getFlagString("--mac", "-m")
	if nameFilter != "" || macFilter != "" {
		filtered := []map[string]interface{}{}
		for _, d := range devices {
			if nameFilter != "" {
				name := strings.ToLower(fmt.Sprintf("%v", d["name"]))
				if !strings.Contains(name, strings.ToLower(nameFilter)) {
					continue
				}
			}
			if macFilter != "" {
				mac := strings.ToLower(fmt.Sprintf("%v", d["mac"]))
				if mac != strings.ToLower(macFilter) {
					continue
				}
			}
			filtered = append(filtered, d)
		}
		devices = filtered
	}

	// Get total samples
	var totalSamples int64
	db.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startTime.Format(time.RFC3339)).Scan(&totalSamples)

	report := map[string]interface{}{
		"period":           periodName,
		"start_time":       startTime.Format(time.RFC3339),
		"query_time":       time.Now().Format(time.RFC3339),
		"interval_seconds": 5,
		"total_samples":    totalSamples,
		"devices":          devices,
	}

	if jsonOut {
		// Add calculated totals for JSON
		for _, d := range devices {
			totalDown := d["total_download"].(int64)
			totalUp := d["total_upload"].(int64)
			d["download_kb"] = totalDown * 5
			d["upload_kb"] = totalUp * 5
			d["total_kb"] = (totalDown + totalUp) * 5
		}
		printJSON(report)
	} else {
		printReport(report)
	}
}

func runStatus() {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Run 'poll' first to collect data.")
		return
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	defer db.Close()

	var totalSamples, uniqueDevices int64
	var firstSample, lastSample sql.NullString

	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples)
	db.QueryRow("SELECT COUNT(DISTINCT mac) FROM bandwidth_samples").Scan(&uniqueDevices)
	db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM bandwidth_samples").Scan(&firstSample, &lastSample)

	size := getDBSize()

	fmt.Printf("\nDatabase: %s\n", dbPath)
	fmt.Printf("Size: %.2f MB / %.0f GB limit\n", float64(size)/(1024*1024), float64(DBSizeLimitBytes)/(1024*1024*1024))
	fmt.Printf("Total samples: %d\n", totalSamples)
	fmt.Printf("Unique devices: %d\n", uniqueDevices)
	fmt.Printf("First sample: %s\n", firstSample.String)
	fmt.Printf("Last sample: %s\n", lastSample.String)
}

func runPurge(force bool) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Nothing to purge.")
		return
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	var totalSamples int64
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples)

	if !force {
		fmt.Printf("\nWARNING: This will delete ALL %d records!\n", totalSamples)
		fmt.Printf("Database: %s\n", dbPath)
		fmt.Print("Type 'yes' to confirm: ")

		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "yes" {
			fmt.Println("Purge cancelled.")
			db.Close()
			return
		}
	}

	_, err = db.Exec("DELETE FROM bandwidth_samples")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting records: %v\n", err)
		db.Close()
		return
	}

	db.Exec("VACUUM")
	db.Close()

	fmt.Printf("Purged %d records from database.\n", totalSamples)
	fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
}

func runReboot() {
	force := hasFlag("--force", "-f")

	if !force {
		fmt.Print("Reboot the router? This will disconnect all devices. Type 'yes' to confirm: ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "yes" {
			fmt.Println("Reboot cancelled.")
			return
		}
	}

	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	if err := client.Reboot(); err != nil {
		fmt.Fprintf(os.Stderr, "Error rebooting: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Reboot command sent. The router will restart shortly.")
}

func runBlock(mac string) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	if err := client.BlockClient(mac); err != nil {
		fmt.Fprintf(os.Stderr, "Error blocking device: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Blocked device %s\n", mac)
}

func runUnblock(mac string) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	if err := client.UnblockClient(mac); err != nil {
		fmt.Fprintf(os.Stderr, "Error unblocking device: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Unblocked device %s\n", mac)
}

func runAlias() {
	aliases := loadAliases()

	// No args: list aliases
	if len(os.Args) < 3 || strings.HasPrefix(os.Args[2], "-") {
		if hasFlag("--remove", "-r") {
			if len(os.Args) < 4 {
				fmt.Println("Usage: deco alias --remove <MAC>")
				os.Exit(1)
			}
			mac := strings.ToUpper(os.Args[3])
			if _, ok := aliases[mac]; !ok {
				fmt.Printf("No alias found for %s\n", mac)
				os.Exit(1)
			}
			delete(aliases, mac)
			saveAliases(aliases)
			fmt.Printf("Removed alias for %s\n", mac)
			return
		}

		if len(aliases) == 0 {
			fmt.Println("No aliases set. Usage: deco alias <MAC> <name>")
			return
		}

		fmt.Printf("\n%-20s %s\n", "MAC", "ALIAS")
		fmt.Println(strings.Repeat("-", 50))
		for mac, name := range aliases {
			fmt.Printf("%-20s %s\n", mac, name)
		}
		return
	}

	// Set alias: deco alias <MAC> <name>
	if len(os.Args) < 4 {
		fmt.Println("Usage: deco alias <MAC> <name>")
		os.Exit(1)
	}

	mac := strings.ToUpper(os.Args[2])
	name := strings.Join(os.Args[3:], " ")

	aliases[mac] = name
	saveAliases(aliases)
	fmt.Printf("Set alias: %s -> %s\n", mac, name)
}

func loadAliases() map[string]string {
	exe, _ := os.Executable()
	aliasPath := filepath.Join(filepath.Dir(exe), "deco_aliases.json")

	data, err := os.ReadFile(aliasPath)
	if err != nil {
		data, err = os.ReadFile("deco_aliases.json")
		if err != nil {
			return map[string]string{}
		}
	}

	var aliases map[string]string
	if err := json.Unmarshal(data, &aliases); err != nil {
		return map[string]string{}
	}
	return aliases
}

func saveAliases(aliases map[string]string) {
	exe, _ := os.Executable()
	aliasPath := filepath.Join(filepath.Dir(exe), "deco_aliases.json")

	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
		return
	}

	if err := os.WriteFile(aliasPath, data, 0644); err != nil {
		aliasPath = "deco_aliases.json"
		if err := os.WriteFile(aliasPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
		}
	}
}

func filterClients(data map[string]interface{}, nameFilter, macFilter string) map[string]interface{} {
	clients := data["clients"].([]map[string]interface{})
	filtered := []map[string]interface{}{}

	aliases := loadAliases()

	for _, c := range clients {
		if nameFilter != "" {
			name := strings.ToLower(fmt.Sprintf("%v", c["name"]))
			// Also check alias
			if mac, ok := c["mac"].(string); ok {
				if alias, ok := aliases[strings.ToUpper(mac)]; ok {
					name = strings.ToLower(alias)
				}
			}
			if !strings.Contains(name, strings.ToLower(nameFilter)) {
				continue
			}
		}
		if macFilter != "" {
			mac := strings.ToLower(fmt.Sprintf("%v", c["mac"]))
			if mac != strings.ToLower(macFilter) {
				continue
			}
		}
		filtered = append(filtered, c)
	}

	return map[string]interface{}{
		"clients": filtered,
		"count":   len(filtered),
	}
}
