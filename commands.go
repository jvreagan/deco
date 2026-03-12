package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func runSetup() error {
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
		return fmt.Errorf("password is required")
	}

	config := Config{Host: host, Password: password}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %v", err)
	}

	if err := ensureConfigDir(); err != nil {
		return fmt.Errorf("creating config directory: %v", err)
	}
	configFile := cfgPath("deco_config.json")

	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return fmt.Errorf("writing config: %v", err)
	}

	fmt.Printf("\nConfig saved to %s\n", configFile)

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
	return nil
}

func runVersion() {
	fmt.Println(version)
}

func connectClient() (*DecoClient, *Config, error) {
	config, err := loadConfig()
	if err != nil {
		return nil, nil, err
	}

	if err := validateConfig(config); err != nil {
		return nil, nil, err
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		return nil, nil, fmt.Errorf("connecting: %v", err)
	}

	return client, config, nil
}

func runClients(jsonOut bool, nameFilter, macFilter string) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetClients()
	if err != nil {
		return err
	}

	if nameFilter != "" || macFilter != "" {
		data = filterClients(data, nameFilter, macFilter)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printClientsTable(data)
	}
	return nil
}

// pollLoopConfig defines the behavior of a shared polling loop.
type pollLoopConfig struct {
	interval       int
	label          string // "Poll", "Monitor", "Watch"
	needsDB        bool
	ctx            context.Context // if nil, pollLoop creates its own via signal.NotifyContext
	maxFailures    int             // if 0, defaults to maxConsecutiveFailures (10)
	configOverride *Config         // if set, skip loadConfig/validateConfig
	setup          func(db *sql.DB, size int64) // optional, called once before the loop
	work           func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error
}

// pollLoop runs a polling loop with shared infrastructure: config loading,
// signal handling, client creation, auth with backoff, and DB management.
func pollLoop(cfg pollLoopConfig) error {
	var config *Config
	if cfg.configOverride != nil {
		config = cfg.configOverride
	} else {
		var err error
		config, err = loadConfig()
		if err != nil {
			return err
		}
		if err := validateConfig(config); err != nil {
			return err
		}
	}

	var db *sql.DB
	if cfg.needsDB {
		var dbErr error
		db, dbErr = initDB()
		if dbErr != nil {
			return fmt.Errorf("initializing database: %v", dbErr)
		}
		defer db.Close()

		ok, size := checkDBSizeLimit()
		if !ok {
			printDBLimitError(size, cfg.label)
			return fmt.Errorf("database size limit exceeded")
		}
		if cfg.setup != nil {
			cfg.setup(db, size)
		}
	} else if cfg.setup != nil {
		cfg.setup(nil, 0)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if cfg.ctx != nil {
		ctx = cfg.ctx
		cancel = func() {} // no-op; caller owns the context
	} else {
		ctx, cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	}
	defer cancel()

	client := NewDecoClient(config.Host, config.Password)
	defer client.Logout()

	maxFail := maxConsecutiveFailures
	if cfg.maxFailures > 0 {
		maxFail = cfg.maxFailures
	}

	cycle := 0
	consecutiveFailures := 0
	baseDuration := time.Duration(cfg.interval) * time.Second

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\n%s stopped. Completed %d cycles.\n", cfg.label, cycle)
			return nil
		default:
		}

		if cfg.needsDB {
			ok, size := checkDBSizeLimit()
			if !ok {
				printDBLimitError(size, cfg.label)
				return fmt.Errorf("database size limit exceeded")
			}
			checkDBCapacity(db)
		}

		start := time.Now()

		if err := client.EnsureAuthorized(); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= maxFail {
				return fmt.Errorf("%d consecutive failures, giving up", consecutiveFailures)
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] Error connecting: %v, retrying in %s...\n", time.Now().Format("15:04:05"), err, wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\n%s stopped. Completed %d cycles.\n", cfg.label, cycle)
				return nil
			case <-time.After(wait):
			}
			continue
		}

		if err := cfg.work(ctx, client, db, cycle); err != nil {
			client.Invalidate()
			consecutiveFailures++
			if consecutiveFailures >= maxFail {
				return fmt.Errorf("%d consecutive failures, giving up", consecutiveFailures)
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] Error: %v, retrying in %s...\n", time.Now().Format("15:04:05"), err, wait)
			select {
			case <-ctx.Done():
				fmt.Printf("\n%s stopped. Completed %d cycles.\n", cfg.label, cycle)
				return nil
			case <-time.After(wait):
			}
			continue
		}

		consecutiveFailures = 0
		cycle++

		elapsed := time.Since(start)
		sleepTime := baseDuration - elapsed
		if sleepTime > 0 {
			select {
			case <-ctx.Done():
				fmt.Printf("\n%s stopped. Completed %d cycles.\n", cfg.label, cycle)
				return nil
			case <-time.After(sleepTime):
			}
		}
	}
}

func runWatch(interval int, nameFilter, macFilter string) error {
	return pollLoop(pollLoopConfig{
		interval: interval,
		label:    "Watch",
		needsDB:  false,
		work: func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error {
			data, err := client.GetClients()
			if err != nil {
				return err
			}
			if nameFilter != "" || macFilter != "" {
				data = filterClients(data, nameFilter, macFilter)
			}
			fmt.Print("\033[2J\033[H")
			fmt.Printf("Watching clients (every %ds) — %s — Press Ctrl+C to stop\n", interval, time.Now().Format("15:04:05"))
			printClientsTable(data)
			return nil
		},
	})
}

func runNetwork(jsonOut bool) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetNetwork()
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(data)
	} else {
		printNetworkTable(data)
	}
	return nil
}

func runWireless(jsonOut bool) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetWireless()
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(data)
	} else {
		printWirelessTable(data)
	}
	return nil
}

func runMesh(jsonOut bool) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetMesh()
	if err != nil {
		return err
	}

	if jsonOut {
		printJSON(data)
	} else {
		printMeshTable(data)
	}
	return nil
}

func runAll() error {
	client, config, err := connectClient()
	if err != nil {
		return err
	}
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
	return nil
}

func runAPI(endpoint, body string) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	var reqData map[string]interface{}
	if err := json.Unmarshal([]byte(body), &reqData); err != nil {
		return fmt.Errorf("invalid JSON body: %v", err)
	}

	resp, err := client.Request(endpoint, reqData)
	if err != nil {
		return fmt.Errorf("API error: %v", err)
	}

	printJSON(resp)
	return nil
}

func runPoll(interval int) error {
	return pollLoop(pollLoopConfig{
		interval: interval,
		label:    "Poll",
		needsDB:  true,
		setup: func(db *sql.DB, size int64) {
			fmt.Printf("Starting bandwidth monitor (polling every %ds)\n", interval)
			fmt.Printf("Database: %s\n", dbPath)
			fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
			fmt.Println("Press Ctrl+C to stop")
		},
		work: func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error {
			data, err := client.GetClients()
			if err != nil {
				return err
			}

			timestamp := time.Now().Format(time.RFC3339)
			for _, c := range data.Clients {
				if _, err := db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					timestamp, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps); err != nil {
					fmt.Printf("DB error: %v\n", err)
				}
			}

			activeCount := 0
			var totalDown, totalUp int64
			for _, c := range data.Clients {
				if c.DownloadKbps > 0 || c.UploadKbps > 0 {
					activeCount++
				}
				totalDown += int64(c.DownloadKbps)
				totalUp += int64(c.UploadKbps)
			}

			fmt.Printf("[%s] Sample #%d: %d clients, %d active, %d KB/s down %d KB/s up\n",
				time.Now().Format("15:04:05"), cycle+1, len(data.Clients), activeCount, totalDown, totalUp)
			return nil
		},
	})
}

func runMonitor(interval int, notify bool, alertThreshold int) error {
	var knownMACs map[string]bool
	var aliases map[string]string

	return pollLoop(pollLoopConfig{
		interval: interval,
		label:    "Monitor",
		needsDB:  true,
		setup: func(db *sql.DB, size int64) {
			fmt.Printf("Starting full network monitor (polling every %ds)\n", interval)
			fmt.Printf("Database: %s\n", dbPath)
			fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
			if notify {
				knownMACs = loadKnownMACs(db)
				fmt.Printf("Known MACs: %d (notifications enabled)\n", len(knownMACs))
			}
			if alertThreshold > 0 {
				aliases = loadAliases()
				fmt.Printf("Bandwidth alert threshold: %dKB/s\n", alertThreshold)
			}
			fmt.Println("Press Ctrl+C to stop")
		},
		work: func(ctx context.Context, client *DecoClient, db *sql.DB, cycle int) error {
			clientData, clientErr := client.GetClients()
			networkData, networkErr := client.GetNetwork()
			wirelessData, wirelessErr := client.GetWireless()
			meshData, meshErr := client.GetMesh()

			// If all requests failed, session is probably dead
			if clientErr != nil && networkErr != nil && wirelessErr != nil && meshErr != nil {
				return fmt.Errorf("all requests failed")
			}

			timestamp := time.Now().Format(time.RFC3339)
			ts := time.Now().Format("15:04:05")

			clientCount := 0
			if clientErr == nil {
				clientCount = len(clientData.Clients)
				for _, c := range clientData.Clients {
					if _, err := db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps); err != nil {
						fmt.Fprintf(os.Stderr, "[%s] DB error (bandwidth): %v\n", ts, err)
					}
					if notify && knownMACs != nil {
						macUpper := strings.ToUpper(c.MAC)
						if !knownMACs[macUpper] {
							notifyNewMAC(c.MAC, c.Name, c.IP)
							knownMACs[macUpper] = true
						}
					}
					if alertThreshold > 0 {
						rate := c.DownloadKbps + c.UploadKbps
						if rate > alertThreshold {
							name := c.Name
							if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
								name = alias
							}
							fmt.Printf("[%s] ALERT: %s — %dKB/s (threshold: %dKB/s)\n",
								ts, name, rate, alertThreshold)
						}
					}
				}
			} else {
				fmt.Printf("[%s] Error getting clients: %v\n", ts, clientErr)
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
					fmt.Fprintf(os.Stderr, "[%s] DB error (network): %v\n", ts, err)
				}
			} else {
				fmt.Printf("[%s] Error getting network: %v\n", ts, networkErr)
			}

			meshCount := 0
			if meshErr == nil {
				meshCount = len(meshData.Devices)
				for _, d := range meshData.Devices {
					if _, err := db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, d.Name, d.Role, d.IP, d.MAC, d.Model, d.Firmware, d.Status); err != nil {
						fmt.Fprintf(os.Stderr, "[%s] DB error (mesh): %v\n", ts, err)
					}
				}
			} else {
				fmt.Printf("[%s] Error getting mesh: %v\n", ts, meshErr)
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
						fmt.Fprintf(os.Stderr, "[%s] DB error (wireless): %v\n", ts, err)
					}
				}
			} else {
				fmt.Printf("[%s] Error getting wireless: %v\n", ts, wirelessErr)
			}

			cpuStr := "?"
			memStr := "?"
			if cpuPct != nil {
				cpuStr = fmt.Sprintf("%.0f%%", *cpuPct)
			}
			if memPct != nil {
				memStr = fmt.Sprintf("%.0f%%", *memPct)
			}

			fmt.Printf("[%s] Cycle #%d: %d clients, CPU %s, Mem %s, %d mesh nodes\n",
				ts, cycle+1, clientCount, cpuStr, memStr, meshCount)
			return nil
		},
	})
}

func loadKnownMACs(db *sql.DB) map[string]bool {
	known := map[string]bool{}
	rows, err := db.Query("SELECT DISTINCT mac FROM bandwidth_samples")
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
	return known
}

func notifyNewMAC(mac, name, ip string) {
	msg := fmt.Sprintf("NEW DEVICE: %s (%s) at %s", name, mac, ip)
	logInfo("%s", msg)

	// macOS desktop notification (best-effort)
	cmd := exec.Command("osascript", "-e",
		fmt.Sprintf(`display notification "%s" with title "Deco: New Device"`, msg))
	cmd.Run()
}

func parsePeriod(period string) (time.Time, string) {
	switch period {
	case "today":
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), "Today"
	case "hour":
		return time.Now().Add(-1 * time.Hour), "Last hour"
	default:
		return time.Time{}, "All time"
	}
}

// estimateInterval derives the polling interval from sample timestamps.
// Returns the median gap between consecutive distinct timestamps, or 5 as fallback.
func estimateInterval(db *sql.DB, since time.Time) int {
	rows, err := db.Query(`SELECT DISTINCT timestamp FROM bandwidth_samples
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
	return gaps[len(gaps)/2] // median
}

func runReport(period string, jsonOut, csvOut bool, nameFilter, macFilter, group string) error {
	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	startTime, periodName := parsePeriod(period)

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
		return fmt.Errorf("query error: %v", err)
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

	// Query connection-type breakdown per device
	connRows, connErr := db.Query(`SELECT mac, connection, COUNT(*) as samples
		FROM bandwidth_samples WHERE timestamp >= ? GROUP BY mac, connection`,
		startTime.Format(time.RFC3339))
	if connErr == nil {
		breakdown := map[string]map[string]int64{}
		for connRows.Next() {
			var m, conn string
			var count int64
			if err := connRows.Scan(&m, &conn, &count); err == nil {
				if breakdown[m] == nil {
					breakdown[m] = map[string]int64{}
				}
				breakdown[m][conn] = count
			}
		}
		connRows.Close()
		for i := range devices {
			if bd, ok := breakdown[devices[i].MAC]; ok {
				devices[i].ConnectionBreakdown = bd
			}
		}
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

	if group != "" {
		tags := loadTags()
		var grouped []ReportDevice
		for _, d := range devices {
			for _, t := range tags[strings.ToUpper(d.MAC)] {
				if strings.EqualFold(t, group) {
					grouped = append(grouped, d)
					break
				}
			}
		}
		devices = grouped
	}

	var totalSamples int64
	if err := db.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startTime.Format(time.RFC3339)).Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to count samples: %v\n", err)
	}

	intervalSec := estimateInterval(db, startTime)

	report := &Report{
		Period:          periodName,
		StartTime:       startTime.Format(time.RFC3339),
		QueryTime:       time.Now().Format(time.RFC3339),
		IntervalSeconds: intervalSec,
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
	} else if csvOut {
		printReportCSV(report)
	} else {
		printReport(report)
	}
	return nil
}

func runReportNetwork(period string, jsonOut bool) error {
	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	startTime, periodName := parsePeriod(period)

	rows, err := db.Query(`
		SELECT timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2,
		       COALESCE(cpu_percent, 0), COALESCE(mem_percent, 0)
		FROM network_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp`, startTime.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("query error: %v", err)
	}
	defer rows.Close()

	var entries []NetworkReportEntry
	for rows.Next() {
		var e NetworkReportEntry
		if err := rows.Scan(&e.Timestamp, &e.WANIP, &e.Gateway, &e.DNS1, &e.DNS2, &e.CPU, &e.Memory); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	if jsonOut {
		printJSON(entries)
	} else {
		printNetworkReport(entries, periodName)
	}
	return nil
}

func runReportMesh(period string, jsonOut bool) error {
	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	startTime, periodName := parsePeriod(period)

	rows, err := db.Query(`
		SELECT timestamp, name, role, ip, mac, status, firmware
		FROM mesh_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp`, startTime.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("query error: %v", err)
	}
	defer rows.Close()

	var entries []MeshReportEntry
	for rows.Next() {
		var e MeshReportEntry
		if err := rows.Scan(&e.Timestamp, &e.Name, &e.Role, &e.IP, &e.MAC, &e.Status, &e.Firmware); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	if jsonOut {
		printJSON(entries)
	} else {
		printMeshReport(entries, periodName)
	}
	return nil
}

func runStatus() {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Run 'poll' first to collect data.")
		return
	}

	db, err := initDB()
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

	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	defer db.Close()

	var cutoff time.Time
	selective := false
	if beforeStr != "" {
		cutoff, err = time.Parse("2006-01-02", beforeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid date %q (use YYYY-MM-DD)\n", beforeStr)
			return
		}
		selective = true
	} else if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
		selective = true
	}

	if selective {
		count, _ := countBeforeDate(db, cutoff)

		if !force {
			fmt.Printf("\nWill delete %d records older than %s\n", count, cutoff.Format("2006-01-02"))
			fmt.Printf("Database: %s\n", dbPath)
			fmt.Print("Type 'yes' to confirm: ")

			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "yes" {
				fmt.Println("Purge cancelled.")
				return
			}
		}

		purgeByDate(db, cutoff)

		fmt.Printf("Purged %d records older than %s\n", count, cutoff.Format("2006-01-02"))
		fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
		return
	}

	// Full purge
	var totalSamples int64
	if err := db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Error counting records: %v\n", err)
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
			return
		}
	}

	for _, table := range allTables {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting from %s: %v\n", table, err)
		}
	}

	db.Exec("VACUUM")

	fmt.Printf("Purged all records from database.\n")
	fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
}

var allTables = []string{"bandwidth_samples", "network_snapshots", "mesh_snapshots", "wireless_snapshots"}

func countBeforeDate(db *sql.DB, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.Format(time.RFC3339)
	var total int64
	for _, table := range allTables {
		var c int64
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE timestamp < ?", cutoffStr).Scan(&c); err != nil {
			return total, err
		}
		total += c
	}
	return total, nil
}

func purgeByDate(db *sql.DB, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.Format(time.RFC3339)
	var total int64
	for _, table := range allTables {
		result, err := db.Exec("DELETE FROM "+table+" WHERE timestamp < ?", cutoffStr)
		if err != nil {
			return total, fmt.Errorf("error deleting from %s: %v", table, err)
		}
		n, _ := result.RowsAffected()
		total += n
	}
	db.Exec("VACUUM")
	return total, nil
}

func runReboot(force bool) error {
	if !force {
		fmt.Print("Reboot the router? This will disconnect all devices. Type 'yes' to confirm: ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "yes" {
			fmt.Println("Reboot cancelled.")
			return nil
		}
	}

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	if err := client.Reboot(); err != nil {
		return fmt.Errorf("rebooting: %v", err)
	}

	fmt.Println("Reboot command sent. The router will restart shortly.")
	return nil
}

func runBlock(mac string) error {
	if !validMAC(mac) {
		return fmt.Errorf("invalid MAC address %q", mac)
	}

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	if err := client.BlockClient(mac); err != nil {
		return fmt.Errorf("blocking device: %v", err)
	}

	fmt.Printf("Blocked device %s\n", mac)
	return nil
}

func runUnblock(mac string) error {
	if !validMAC(mac) {
		return fmt.Errorf("invalid MAC address %q", mac)
	}

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	if err := client.UnblockClient(mac); err != nil {
		return fmt.Errorf("unblocking device: %v", err)
	}

	fmt.Printf("Unblocked device %s\n", mac)
	return nil
}

func runAlias(remove bool, args []string) error {
	aliases := loadAliases()

	if remove {
		if len(args) < 1 {
			return fmt.Errorf("usage: deco alias --remove <MAC>")
		}
		mac := strings.ToUpper(args[0])
		if !validMAC(mac) {
			return fmt.Errorf("invalid MAC address %q", mac)
		}
		if _, ok := aliases[mac]; !ok {
			return fmt.Errorf("no alias found for %s", mac)
		}
		delete(aliases, mac)
		saveAliases(aliases)
		fmt.Printf("Removed alias for %s\n", mac)
		return nil
	}

	// No args: list aliases
	if len(args) == 0 {
		if len(aliases) == 0 {
			fmt.Println("No aliases set. Usage: deco alias <MAC> <name>")
			return nil
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
		return nil
	}

	// Set alias: deco alias <MAC> <name>
	if len(args) < 2 {
		return fmt.Errorf("usage: deco alias <MAC> <name>")
	}

	mac := strings.ToUpper(args[0])
	if !validMAC(mac) {
		return fmt.Errorf("invalid MAC address %q", mac)
	}
	name := strings.Join(args[1:], " ")

	aliases[mac] = name
	saveAliases(aliases)
	fmt.Printf("Set alias: %s -> %s\n", mac, name)
	return nil
}

func loadTags() map[string][]string {
	data, err := os.ReadFile(cfgPath("deco_tags.json"))
	if err != nil {
		return map[string][]string{}
	}

	var tags map[string][]string
	if err := json.Unmarshal(data, &tags); err != nil {
		return map[string][]string{}
	}
	return tags
}

func saveTags(tags map[string][]string) {
	if err := ensureConfigDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config dir: %v\n", err)
		return
	}

	// Clean up empty tag lists
	for mac, t := range tags {
		if len(t) == 0 {
			delete(tags, mac)
		}
	}

	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving tags: %v\n", err)
		return
	}

	if err := os.WriteFile(cfgPath("deco_tags.json"), data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving tags: %v\n", err)
	}
}

func runAliasTag(args []string) error {
	mac := strings.ToUpper(args[0])
	if !validMAC(mac) {
		return fmt.Errorf("invalid MAC address %q", mac)
	}
	tag := strings.ToLower(args[1])

	tags := loadTags()
	for _, t := range tags[mac] {
		if t == tag {
			fmt.Printf("%s already has tag %q\n", mac, tag)
			return nil
		}
	}
	tags[mac] = append(tags[mac], tag)
	saveTags(tags)
	fmt.Printf("Tagged %s with %q\n", mac, tag)
	return nil
}

func runAliasUntag(args []string) error {
	mac := strings.ToUpper(args[0])
	if !validMAC(mac) {
		return fmt.Errorf("invalid MAC address %q", mac)
	}
	tag := strings.ToLower(args[1])

	tags := loadTags()
	found := false
	var newTags []string
	for _, t := range tags[mac] {
		if t == tag {
			found = true
		} else {
			newTags = append(newTags, t)
		}
	}
	if !found {
		return fmt.Errorf("%s does not have tag %q", mac, tag)
	}
	tags[mac] = newTags
	saveTags(tags)
	fmt.Printf("Removed tag %q from %s\n", tag, mac)
	return nil
}

func runAliasTags() error {
	tags := loadTags()
	aliases := loadAliases()

	if len(tags) == 0 {
		fmt.Println("No tags set. Usage: deco alias tag <MAC> <tag>")
		return nil
	}

	// Group by tag
	tagDevices := map[string][]string{} // tag -> list of "name (MAC)"
	for mac, macTags := range tags {
		name := mac
		if alias, ok := aliases[mac]; ok {
			name = alias
		}
		for _, t := range macTags {
			tagDevices[t] = append(tagDevices[t], fmt.Sprintf("%s (%s)", name, mac))
		}
	}

	tagNames := make([]string, 0, len(tagDevices))
	for t := range tagDevices {
		tagNames = append(tagNames, t)
	}
	sort.Strings(tagNames)

	for _, t := range tagNames {
		fmt.Printf("\n[%s]\n", t)
		sort.Strings(tagDevices[t])
		for _, d := range tagDevices[t] {
			fmt.Printf("  %s\n", d)
		}
	}
	return nil
}

func loadAliases() map[string]string {
	data, err := os.ReadFile(cfgPath("deco_aliases.json"))
	if err != nil {
		return map[string]string{}
	}

	var aliases map[string]string
	if err := json.Unmarshal(data, &aliases); err != nil {
		return map[string]string{}
	}
	return aliases
}

func saveAliases(aliases map[string]string) {
	if err := ensureConfigDir(); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config dir: %v\n", err)
		return
	}

	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
		return
	}

	if err := os.WriteFile(cfgPath("deco_aliases.json"), data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving aliases: %v\n", err)
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

