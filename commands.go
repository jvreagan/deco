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

func runClients(jsonOut bool) {
	client, _ := connectClient()
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

func runWatch(interval int) {
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

	nameFilter := getFlagString("--name", "-n")
	macFilter := getFlagString("--mac", "-m")

	client := NewDecoClient(config.Host, config.Password)
	defer client.Logout()

	for {
		if err := client.EnsureAuthorized(); err != nil {
			fmt.Printf("\033[2J\033[HError connecting: %v, retrying...\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(interval) * time.Second):
				continue
			}
		}

		data, err := client.GetClients()
		if err != nil {
			client.Invalidate()
			fmt.Printf("\033[2J\033[HError: %v, retrying...\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(interval) * time.Second):
				continue
			}
		}

		if nameFilter != "" || macFilter != "" {
			data = filterClients(data, nameFilter, macFilter)
		}

		fmt.Print("\033[2J\033[H")
		fmt.Printf("Watching clients (every %ds) — %s — Press Ctrl+C to stop\n", interval, time.Now().Format("15:04:05"))
		printClientsTable(data)

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(interval) * time.Second):
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

	network, _ := client.GetNetwork()
	wireless, _ := client.GetWireless()
	mesh, _ := client.GetMesh()
	clients, _ := client.GetClients()

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

		start := time.Now()

		if err := client.EnsureAuthorized(); err != nil {
			fmt.Printf("Error connecting: %v, retrying...\n", err)
			waitOrCancel(ctx, interval)
			continue
		}

		data, err := client.GetClients()
		if err != nil {
			client.Invalidate()
			fmt.Printf("Error getting clients: %v, retrying...\n", err)
			waitOrCancel(ctx, interval)
			continue
		}

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
		totalDown := 0
		totalUp := 0
		for _, c := range data.Clients {
			if c.DownloadKbps > 0 || c.UploadKbps > 0 {
				activeCount++
			}
			totalDown += c.DownloadKbps
			totalUp += c.UploadKbps
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

		start := time.Now()

		if err := client.EnsureAuthorized(); err != nil {
			fmt.Printf("[%s] Error connecting: %v, retrying next cycle...\n", time.Now().Format("15:04:05"), err)
			waitOrCancel(ctx, interval)
			continue
		}

		clientData, clientErr := client.GetClients()
		networkData, networkErr := client.GetNetwork()
		wirelessData, wirelessErr := client.GetWireless()
		meshData, meshErr := client.GetMesh()

		// If all requests failed, session is probably dead
		if clientErr != nil && networkErr != nil && wirelessErr != nil && meshErr != nil {
			client.Invalidate()
		}

		timestamp := time.Now().Format(time.RFC3339)
		cycleCount++

		clientCount := 0
		if clientErr == nil {
			clientCount = len(clientData.Clients)
			for _, c := range clientData.Clients {
				db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps)
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

			db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timestamp, networkData.WAN.IP, networkData.WAN.Gateway, dns1, dns2, networkData.LAN.IP, networkData.LAN.Netmask, cpuPct, memPct)
		} else {
			fmt.Printf("[%s] Error getting network: %v\n", time.Now().Format("15:04:05"), networkErr)
		}

		meshCount := 0
		if meshErr == nil {
			meshCount = len(meshData.Devices)
			for _, d := range meshData.Devices {
				db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, d.Name, d.Role, d.IP, d.MAC, d.Model, d.Firmware, d.Status)
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

				db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, bandName, band.Host.SSID, band.Host.Channel, band.Host.ChannelWidth, hostEnabled, guestEnabled, band.Guest.SSID)
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

	var devices []ReportDevice
	for rows.Next() {
		var mac, name, ip, connection, deviceType sql.NullString
		var sampleCount, totalDown, totalUp, maxDown, maxUp int64

		err := rows.Scan(&mac, &name, &ip, &connection, &deviceType,
			&sampleCount, &totalDown, &totalUp, &maxDown, &maxUp)
		if err != nil {
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

	// Apply filters (check aliases too)
	nameFilter := getFlagString("--name", "-n")
	macFilter := getFlagString("--mac", "-m")
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
	db.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startTime.Format(time.RFC3339)).Scan(&totalSamples)

	report := &Report{
		Period:          periodName,
		StartTime:       startTime.Format(time.RFC3339),
		QueryTime:       time.Now().Format(time.RFC3339),
		IntervalSeconds: 5,
		TotalSamples:    totalSamples,
		Devices:         devices,
	}

	if jsonOut {
		for i := range report.Devices {
			d := &report.Devices[i]
			d.DownloadKB = d.TotalDownload * 5
			d.UploadKB = d.TotalUpload * 5
			d.TotalKB = (d.TotalDownload + d.TotalUpload) * 5
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

	client, _ := connectClient()
	defer client.Logout()

	if err := client.Reboot(); err != nil {
		fmt.Fprintf(os.Stderr, "Error rebooting: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Reboot command sent. The router will restart shortly.")
}

func runBlock(mac string) {
	client, _ := connectClient()
	defer client.Logout()

	if err := client.BlockClient(mac); err != nil {
		fmt.Fprintf(os.Stderr, "Error blocking device: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Blocked device %s\n", mac)
}

func runUnblock(mac string) {
	client, _ := connectClient()
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

func runCompletion(shell string) {
	switch shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		fmt.Fprintf(os.Stderr, "Unsupported shell: %s (use bash, zsh, or fish)\n", shell)
		os.Exit(1)
	}
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

// ==================== COMPLETION SCRIPTS ====================

const bashCompletion = `_deco_completions() {
    local commands="clients network wireless mesh all poll monitor report status purge setup api version reboot block unblock alias completion"
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local prev="${COMP_WORDS[COMP_CWORD-1]}"

    case "$prev" in
        report)
            COMPREPLY=($(compgen -W "today hour all" -- "$cur"))
            return
            ;;
        completion)
            COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
            return
            ;;
    esac

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
    else
        COMPREPLY=($(compgen -W "--json -j --interval -i --force -f --name -n --mac -m --watch -w --remove -r" -- "$cur"))
    fi
}
complete -F _deco_completions deco
`

const zshCompletion = `#compdef deco

_deco() {
    local -a commands
    commands=(
        'clients:List connected devices'
        'network:Show WAN/LAN configuration'
        'wireless:Show WiFi configuration'
        'mesh:Show mesh topology'
        'all:Complete network snapshot'
        'poll:Start bandwidth monitoring'
        'monitor:Full network monitoring'
        'report:Show usage report'
        'status:Show database statistics'
        'purge:Delete all records'
        'setup:Interactive configuration'
        'api:Call a raw API endpoint'
        'version:Show version'
        'reboot:Reboot the router'
        'block:Block a device'
        'unblock:Unblock a device'
        'alias:Manage device aliases'
        'completion:Generate shell completion'
    )

    _arguments '1:command:->cmds' '*:options:->opts'

    case "$state" in
        cmds)
            _describe 'command' commands
            ;;
        opts)
            _arguments \
                '--json[Output as JSON]' \
                '-j[Output as JSON]' \
                '--interval[Polling interval]:seconds:' \
                '-i[Polling interval]:seconds:' \
                '--force[Skip confirmation]' \
                '-f[Skip confirmation]' \
                '--name[Filter by name]:name:' \
                '-n[Filter by name]:name:' \
                '--mac[Filter by MAC]:mac:' \
                '-m[Filter by MAC]:mac:' \
                '--watch[Auto-refresh display]' \
                '-w[Auto-refresh display]'
            ;;
    esac
}

_deco "$@"
`

const fishCompletion = `complete -c deco -f
complete -c deco -n '__fish_use_subcommand' -a 'clients' -d 'List connected devices'
complete -c deco -n '__fish_use_subcommand' -a 'network' -d 'Show WAN/LAN configuration'
complete -c deco -n '__fish_use_subcommand' -a 'wireless' -d 'Show WiFi configuration'
complete -c deco -n '__fish_use_subcommand' -a 'mesh' -d 'Show mesh topology'
complete -c deco -n '__fish_use_subcommand' -a 'all' -d 'Complete network snapshot'
complete -c deco -n '__fish_use_subcommand' -a 'poll' -d 'Start bandwidth monitoring'
complete -c deco -n '__fish_use_subcommand' -a 'monitor' -d 'Full network monitoring'
complete -c deco -n '__fish_use_subcommand' -a 'report' -d 'Show usage report'
complete -c deco -n '__fish_use_subcommand' -a 'status' -d 'Show database statistics'
complete -c deco -n '__fish_use_subcommand' -a 'purge' -d 'Delete all records'
complete -c deco -n '__fish_use_subcommand' -a 'setup' -d 'Interactive configuration'
complete -c deco -n '__fish_use_subcommand' -a 'api' -d 'Call a raw API endpoint'
complete -c deco -n '__fish_use_subcommand' -a 'version' -d 'Show version'
complete -c deco -n '__fish_use_subcommand' -a 'reboot' -d 'Reboot the router'
complete -c deco -n '__fish_use_subcommand' -a 'block' -d 'Block a device'
complete -c deco -n '__fish_use_subcommand' -a 'unblock' -d 'Unblock a device'
complete -c deco -n '__fish_use_subcommand' -a 'alias' -d 'Manage device aliases'
complete -c deco -n '__fish_use_subcommand' -a 'completion' -d 'Generate shell completion'
complete -c deco -n '__fish_seen_subcommand_from report' -a 'today hour all'
complete -c deco -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c deco -l json -s j -d 'Output as JSON'
complete -c deco -l interval -s i -d 'Polling interval' -r
complete -c deco -l force -s f -d 'Skip confirmation'
complete -c deco -l name -s n -d 'Filter by name' -r
complete -c deco -l mac -s m -d 'Filter by MAC' -r
complete -c deco -l watch -s w -d 'Auto-refresh display'
`
