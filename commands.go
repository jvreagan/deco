package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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

	configPath := "deco_config.json"
	if exe, err := os.Executable(); err == nil {
		configPath = filepath.Join(filepath.Dir(exe), "deco_config.json")
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		// Fall back to current directory
		configPath = "deco_config.json"
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			fmt.Printf("Error writing config: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("\nConfig saved to %s\n", configPath)

	// Test the connection
	fmt.Print("Testing connection... ")
	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Printf("WARNING: could not connect: %v\n", err)
		fmt.Println("Check your router IP and password, then try: deco clients")
	} else {
		client.Logout()
		fmt.Println("OK!")
		fmt.Println("Try it out: deco clients")
	}
}

func runVersion() {
	fmt.Println(version)
}

func connectClient() (*DecoClient, *Config) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := validateConfig(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}

	return client, config
}

func runClients(jsonOut bool, nameFilter, macFilter string) {
	client, _ := connectClient()
	defer client.Logout()

	data, err := client.GetClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if nameFilter != "" || macFilter != "" {
		data = filterClients(data, nameFilter, macFilter)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printClientsTable(data)
	}
}

func runWatch(interval int, nameFilter, macFilter string) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := validateConfig(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := NewDecoClient(config.Host, config.Password)
	defer client.Logout()

	consecutiveFailures := 0
	baseDuration := time.Duration(interval) * time.Second

	for {
		if err := client.EnsureAuthorized(); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("\033[2J\033[HError connecting: %v, retrying in %s...\n", err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
				continue
			}
		}

		data, err := client.GetClients()
		if err != nil {
			client.Invalidate()
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("\033[2J\033[HError: %v, retrying in %s...\n", err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
				continue
			}
		}

		consecutiveFailures = 0

		if nameFilter != "" || macFilter != "" {
			data = filterClients(data, nameFilter, macFilter)
		}

		fmt.Print("\033[2J\033[H")
		fmt.Printf("Watching clients (every %ds) — %s — Press Ctrl+C to stop\n", interval, time.Now().Format("15:04:05"))
		printClientsTable(data)

		select {
		case <-ctx.Done():
			return
		case <-time.After(baseDuration):
		}
	}
}

func runNetwork(jsonOut bool) {
	client, _ := connectClient()
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
	client, _ := connectClient()
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
	client, _ := connectClient()
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
	client, config := connectClient()
	defer client.Logout()

	network, netErr := client.GetNetwork()
	wireless, wlErr := client.GetWireless()
	mesh, meshErr := client.GetMesh()
	clients, cliErr := client.GetClients()

	if netErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get network: %v\n", netErr)
	}
	if wlErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get wireless: %v\n", wlErr)
	}
	if meshErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get mesh: %v\n", meshErr)
	}
	if cliErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get clients: %v\n", cliErr)
	}

	data := AllInfo{
		Timestamp: time.Now().Format(time.RFC3339),
		Router:    config.Host,
		Network:   network,
		Wireless:  wireless,
		Mesh:      mesh,
		Clients:   clients,
	}

	printJSON(data)
}

func runAPI(endpoint, body string) {
	client, _ := connectClient()
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
		printDBLimitError(size, "Polling")
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	sampleCount := 0
	consecutiveFailures := 0
	baseDuration := time.Duration(interval) * time.Second

	client := NewDecoClient(config.Host, config.Password)
	defer client.Logout()

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\nMonitor stopped. Recorded %d samples.\n", sampleCount)
			return
		default:
		}

		ok, size := checkDBSizeLimit()
		if !ok {
			printDBLimitError(size, "Polling")
			return
		}
		checkDBCapacity(db)

		start := time.Now()

		if err := client.EnsureAuthorized(); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("Error connecting: %v, retrying in %s...\n", err, wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Recorded %d samples.\n", sampleCount)
				return
			case <-time.After(wait):
			}
			continue
		}

		data, err := client.GetClients()
		if err != nil {
			client.Invalidate()
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("Error getting clients: %v, retrying in %s...\n", err, wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Recorded %d samples.\n", sampleCount)
				return
			case <-time.After(wait):
			}
			continue
		}

		consecutiveFailures = 0

		timestamp := time.Now().Format(time.RFC3339)

		for _, c := range data.Clients {
			_, err := db.Exec(`
				INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, timestamp, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps)
			if err != nil {
				fmt.Printf("DB error: %v\n", err)
			}
		}

		sampleCount++

		activeCount := 0
		var totalDown, totalUp int64
		for _, c := range data.Clients {
			if c.DownloadKbps > 0 || c.UploadKbps > 0 {
				activeCount++
			}
			totalDown += int64(c.DownloadKbps)
			totalUp += int64(c.UploadKbps)
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Sample #%d: %d clients, %d active, %d KB/s down %d KB/s up\n",
			ts, sampleCount, len(data.Clients), activeCount, totalDown, totalUp)

		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 {
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Recorded %d samples.\n", sampleCount)
				return
			case <-time.After(sleepTime):
			}
		}
	}
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
		printDBLimitError(size, "Monitoring")
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cycleCount := 0
	consecutiveFailures := 0
	baseDuration := time.Duration(interval) * time.Second

	client := NewDecoClient(config.Host, config.Password)
	defer client.Logout()

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\nMonitor stopped. Completed %d cycles.\n", cycleCount)
			return
		default:
		}

		ok, size := checkDBSizeLimit()
		if !ok {
			printDBLimitError(size, "Monitoring")
			return
		}
		checkDBCapacity(db)

		start := time.Now()

		if err := client.EnsureAuthorized(); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] Error connecting: %v, retrying in %s...\n", time.Now().Format("15:04:05"), err, wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Completed %d cycles.\n", cycleCount)
				return
			case <-time.After(wait):
			}
			continue
		}

		clientData, clientErr := client.GetClients()
		networkData, networkErr := client.GetNetwork()
		wirelessData, wirelessErr := client.GetWireless()
		meshData, meshErr := client.GetMesh()

		// If all requests failed, session is probably dead
		if clientErr != nil && networkErr != nil && wirelessErr != nil && meshErr != nil {
			client.Invalidate()
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "Error: %d consecutive failures, giving up\n", consecutiveFailures)
				return
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] All requests failed, retrying in %s...\n", time.Now().Format("15:04:05"), wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Completed %d cycles.\n", cycleCount)
				return
			case <-time.After(wait):
			}
			continue
		}

		consecutiveFailures = 0
		timestamp := time.Now().Format(time.RFC3339)
		cycleCount++

		clientCount := 0
		if clientErr == nil {
			clientCount = len(clientData.Clients)
			for _, c := range clientData.Clients {
				if _, err := db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] DB error (bandwidth): %v\n", time.Now().Format("15:04:05"), err)
				}
			}
		} else {
			fmt.Printf("[%s] Error getting clients: %v\n", time.Now().Format("15:04:05"), clientErr)
		}

		var cpuPct, memPct *float64
		if networkErr == nil {
			cpuPct = networkData.Performance.CPUPercent
			memPct = networkData.Performance.MemPercent

			var dns1, dns2 string
			if len(networkData.WAN.DNS) >= 2 {
				dns1 = networkData.WAN.DNS[0]
				dns2 = networkData.WAN.DNS[1]
			}

			if _, err := db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timestamp, networkData.WAN.IP, networkData.WAN.Gateway, dns1, dns2, networkData.LAN.IP, networkData.LAN.Netmask, cpuPct, memPct); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] DB error (network): %v\n", time.Now().Format("15:04:05"), err)
			}
		} else {
			fmt.Printf("[%s] Error getting network: %v\n", time.Now().Format("15:04:05"), networkErr)
		}

		meshCount := 0
		if meshErr == nil {
			meshCount = len(meshData.Devices)
			for _, d := range meshData.Devices {
				if _, err := db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, d.Name, d.Role, d.IP, d.MAC, d.Model, d.Firmware, d.Status); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] DB error (mesh): %v\n", time.Now().Format("15:04:05"), err)
				}
			}
		} else {
			fmt.Printf("[%s] Error getting mesh: %v\n", time.Now().Format("15:04:05"), meshErr)
		}

		if wirelessErr == nil {
			for bandName, band := range wirelessData.Bands {
				hostEnabled := 0
				if band.Host.Enabled {
					hostEnabled = 1
				}
				guestEnabled := 0
				if band.Guest.Enabled {
					guestEnabled = 1
				}

				if _, err := db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, bandName, band.Host.SSID, band.Host.Channel, band.Host.ChannelWidth, hostEnabled, guestEnabled, band.Guest.SSID); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] DB error (wireless): %v\n", time.Now().Format("15:04:05"), err)
				}
			}
		} else {
			fmt.Printf("[%s] Error getting wireless: %v\n", time.Now().Format("15:04:05"), wirelessErr)
		}

		cpuStr := "?"
		memStr := "?"
		if cpuPct != nil {
			cpuStr = fmt.Sprintf("%.0f%%", *cpuPct)
		}
		if memPct != nil {
			memStr = fmt.Sprintf("%.0f%%", *memPct)
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Cycle #%d: %d clients, CPU %s, Mem %s, %d mesh nodes\n",
			ts, cycleCount, clientCount, cpuStr, memStr, meshCount)

		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 {
			select {
			case <-ctx.Done():
				fmt.Printf("\nMonitor stopped. Completed %d cycles.\n", cycleCount)
				return
			case <-time.After(sleepTime):
			}
		}
	}
}

func runReport(period string, jsonOut bool, nameFilter, macFilter string) {
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

	var devices []ReportDevice
	for rows.Next() {
		var mac, name, ip, connection, deviceType sql.NullString
		var sampleCount, totalDown, totalUp, maxDown, maxUp int64

		err := rows.Scan(&mac, &name, &ip, &connection, &deviceType,
			&sampleCount, &totalDown, &totalUp, &maxDown, &maxUp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to scan row: %v\n", err)
			continue
		}

		devices = append(devices, ReportDevice{
			MAC:           mac.String,
			Name:          name.String,
			IP:            ip.String,
			Connection:    connection.String,
			DeviceType:    deviceType.String,
			SampleCount:   sampleCount,
			TotalDownload: totalDown,
			TotalUpload:   totalUp,
			MaxDownload:   maxDown,
			MaxUpload:     maxUp,
		})
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error iterating rows: %v\n", err)
	}

	if nameFilter != "" || macFilter != "" {
		aliases := loadAliases()
		var filtered []ReportDevice
		for _, d := range devices {
			if nameFilter != "" {
				name := strings.ToLower(d.Name)
				// Also check alias
				if alias, ok := aliases[strings.ToUpper(d.MAC)]; ok {
					name = strings.ToLower(alias)
				}
				if !strings.Contains(name, strings.ToLower(nameFilter)) {
					continue
				}
			}
			if macFilter != "" {
				if !strings.EqualFold(d.MAC, macFilter) {
					continue
				}
			}
			filtered = append(filtered, d)
		}
		devices = filtered
	}

	var totalSamples int64
	if err := db.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startTime.Format(time.RFC3339)).Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to count samples: %v\n", err)
	}

	report := &Report{
		Period:          periodName,
		StartTime:       startTime.Format(time.RFC3339),
		QueryTime:       time.Now().Format(time.RFC3339),
		IntervalSeconds: 5,
		TotalSamples:    totalSamples,
		Devices:         devices,
	}

	if jsonOut {
		interval := int64(report.IntervalSeconds)
		for i := range report.Devices {
			d := &report.Devices[i]
			d.DownloadKB = d.TotalDownload * interval
			d.UploadKB = d.TotalUpload * interval
			d.TotalKB = (d.TotalDownload + d.TotalUpload) * interval
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

	if err := db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to count samples: %v\n", err)
	}
	if err := db.QueryRow("SELECT COUNT(DISTINCT mac) FROM bandwidth_samples").Scan(&uniqueDevices); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to count devices: %v\n", err)
	}
	if err := db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM bandwidth_samples").Scan(&firstSample, &lastSample); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to get time range: %v\n", err)
	}

	size := getDBSize()

	fmt.Printf("\nDatabase: %s\n", dbPath)
	fmt.Printf("Size: %.2f MB / %.0f GB limit\n", float64(size)/(1024*1024), float64(DBSizeLimitBytes)/(1024*1024*1024))
	fmt.Printf("Total samples: %d\n", totalSamples)
	fmt.Printf("Unique devices: %d\n", uniqueDevices)
	fmt.Printf("First sample: %s\n", firstSample.String)
	fmt.Printf("Last sample: %s\n", lastSample.String)
}

func runPurge(force bool, beforeStr string, days int) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Nothing to purge.")
		return
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	var cutoff time.Time
	selective := false
	if beforeStr != "" {
		cutoff, err = time.Parse("2006-01-02", beforeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid date %q (use YYYY-MM-DD)\n", beforeStr)
			db.Close()
			return
		}
		selective = true
	} else if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
		selective = true
	}

	tables := []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}

	if selective {
		cutoffStr := cutoff.Format(time.RFC3339)
		// Count records to be deleted
		var count int64
		for _, table := range tables {
			var c int64
			if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE timestamp < ?", cutoffStr).Scan(&c); err == nil {
				count += c
			}
		}

		if !force {
			fmt.Printf("\nWill delete %d records older than %s\n", count, cutoff.Format("2006-01-02"))
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

		for _, table := range tables {
			if _, err := db.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoffStr); err != nil {
				fmt.Fprintf(os.Stderr, "Error deleting from %s: %v\n", table, err)
			}
		}

		if _, err := db.Exec("VACUUM"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: VACUUM failed: %v\n", err)
		}
		db.Close()

		fmt.Printf("Purged %d records older than %s\n", count, cutoff.Format("2006-01-02"))
		fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
		return
	}

	// Full purge
	var totalSamples int64
	if err := db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Error counting records: %v\n", err)
		db.Close()
		return
	}

	if !force {
		fmt.Printf("\nWARNING: This will delete ALL %d bandwidth records and all related snapshots!\n", totalSamples)
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

	for _, table := range tables {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting from %s: %v\n", table, err)
		}
	}

	if _, err := db.Exec("VACUUM"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: VACUUM failed: %v\n", err)
	}
	db.Close()

	fmt.Printf("Purged all records from database.\n")
	fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
}

func runReboot(force bool) {
	if !force {
		fmt.Print("Reboot the router? This will disconnect all devices. Type 'yes' to confirm: ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "yes" {
			fmt.Println("Reboot cancelled.")
			return
		}
	}

	client, _ := connectClient()
	defer client.Logout()

	if err := client.Reboot(); err != nil {
		fmt.Fprintf(os.Stderr, "Error rebooting: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Reboot command sent. The router will restart shortly.")
}

func runBlock(mac string) {
	if !validMAC(mac) {
		fmt.Fprintf(os.Stderr, "Error: invalid MAC address %q\n", mac)
		os.Exit(1)
	}

	client, _ := connectClient()
	defer client.Logout()

	if err := client.BlockClient(mac); err != nil {
		fmt.Fprintf(os.Stderr, "Error blocking device: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Blocked device %s\n", mac)
}

func runUnblock(mac string) {
	if !validMAC(mac) {
		fmt.Fprintf(os.Stderr, "Error: invalid MAC address %q\n", mac)
		os.Exit(1)
	}

	client, _ := connectClient()
	defer client.Logout()

	if err := client.UnblockClient(mac); err != nil {
		fmt.Fprintf(os.Stderr, "Error unblocking device: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Unblocked device %s\n", mac)
}

func runAlias(remove bool, args []string) {
	aliases := loadAliases()

	if remove {
		if len(args) < 1 {
			fmt.Println("Usage: deco alias --remove <MAC>")
			os.Exit(1)
		}
		mac := strings.ToUpper(args[0])
		if !validMAC(mac) {
			fmt.Fprintf(os.Stderr, "Error: invalid MAC address %q\n", mac)
			os.Exit(1)
		}
		if _, ok := aliases[mac]; !ok {
			fmt.Printf("No alias found for %s\n", mac)
			os.Exit(1)
		}
		delete(aliases, mac)
		saveAliases(aliases)
		fmt.Printf("Removed alias for %s\n", mac)
		return
	}

	// No args: list aliases
	if len(args) == 0 {
		if len(aliases) == 0 {
			fmt.Println("No aliases set. Usage: deco alias <MAC> <name>")
			return
		}

		fmt.Printf("\n%-20s %s\n", "MAC", "ALIAS")
		fmt.Println(strings.Repeat("-", 50))
		macs := make([]string, 0, len(aliases))
		for mac := range aliases {
			macs = append(macs, mac)
		}
		sort.Strings(macs)
		for _, mac := range macs {
			fmt.Printf("%-20s %s\n", mac, aliases[mac])
		}
		return
	}

	// Set alias: deco alias <MAC> <name>
	if len(args) < 2 {
		fmt.Println("Usage: deco alias <MAC> <name>")
		os.Exit(1)
	}

	mac := strings.ToUpper(args[0])
	if !validMAC(mac) {
		fmt.Fprintf(os.Stderr, "Error: invalid MAC address %q\n", mac)
		os.Exit(1)
	}
	name := strings.Join(args[1:], " ")

	aliases[mac] = name
	saveAliases(aliases)
	fmt.Printf("Set alias: %s -> %s\n", mac, name)
}

func loadAliases() map[string]string {
	var data []byte
	if exe, err := os.Executable(); err == nil {
		data, _ = os.ReadFile(filepath.Join(filepath.Dir(exe), "deco_aliases.json"))
	}
	if data == nil {
		var err error
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
	aliasPath := "deco_aliases.json"
	if exe, err := os.Executable(); err == nil {
		aliasPath = filepath.Join(filepath.Dir(exe), "deco_aliases.json")
	}

	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
		return
	}

	if err := os.WriteFile(aliasPath, data, 0600); err != nil {
		aliasPath = "deco_aliases.json"
		if err := os.WriteFile(aliasPath, data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
		}
	}
}

func filterClients(data *ClientList, nameFilter, macFilter string) *ClientList {
	var filtered []ClientInfo
	aliases := loadAliases()

	for _, c := range data.Clients {
		if nameFilter != "" {
			name := strings.ToLower(c.Name)
			if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
				name = strings.ToLower(alias)
			}
			if !strings.Contains(name, strings.ToLower(nameFilter)) {
				continue
			}
		}
		if macFilter != "" {
			if !strings.EqualFold(c.MAC, macFilter) {
				continue
			}
		}
		filtered = append(filtered, c)
	}

	return &ClientList{
		Clients: filtered,
		Count:   len(filtered),
	}
}

const maxConsecutiveFailures = 10

func backoff(failures int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < failures; i++ {
		d *= 2
		if d > max {
			return max
		}
	}
	return d
}

func waitOrCancel(ctx context.Context, seconds int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(seconds) * time.Second):
	}
}

func printDBLimitError(size int64, action string) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("Current size: %s\n", formatSize(size))
	fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
	fmt.Printf("\n%s cannot continue. Please run 'purge' to clear the database:\n", action)
	fmt.Println("  deco purge")
	fmt.Println(strings.Repeat("=", 70))
}

