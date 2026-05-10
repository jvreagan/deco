package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	dbpkg "github.com/jvreagan/deco/internal/db"
	"github.com/jvreagan/deco/internal/decoclient"
	"github.com/jvreagan/deco/internal/decolog"
	"github.com/jvreagan/deco/internal/paths"
)

func runSetup() error {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Router IP [192.168.68.1]: ")
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("failed to read input: %v", err)
		}
		return fmt.Errorf("no input provided (stdin closed)")
	}
	host := strings.TrimSpace(scanner.Text())
	if host == "" {
		host = "192.168.68.1"
	}

	fmt.Print("Admin password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // ReadPassword doesn't echo newline
	if err != nil {
		// Fallback to plain text if terminal is not available
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("failed to read password: %v", err)
			}
			return fmt.Errorf("no password provided (stdin closed)")
		}
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

	if err := paths.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config directory: %v", err)
	}
	configFile := paths.CfgPath("deco_config.json")

	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return fmt.Errorf("writing config: %v", err)
	}

	fmt.Printf("\nConfig saved to %s\n", configFile)

	// Test the connection
	fmt.Print("Testing connection... ")
	client := decoclient.NewDecoClient(config.Host, config.Password)
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

// runVersion prints version info to stdout. It does not return error because
// all operations are in-memory (no I/O beyond stdout), and there is nothing
// the caller could recover from if printing fails.
func runVersion() {
	fmt.Println(version)
	if bi, ok := debug.ReadBuildInfo(); ok {
		fmt.Printf("go: %s\n", bi.GoVersion)
	}
	fmt.Printf("os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func connectClient() (*DecoClient, *Config, error) {
	config, err := decoclient.LoadConfig()
	if err != nil {
		return nil, nil, err
	}

	client := decoclient.NewDecoClient(config.Host, config.Password)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var authErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if authErr = client.EnsureAuthorized(ctx); authErr == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("connecting to %s timed out: %v", config.Host, authErr)
		}
		if attempt < 3 {
			time.Sleep(backoff(attempt-1, 1*time.Second, 5*time.Second))
		}
	}
	if authErr != nil {
		return nil, nil, fmt.Errorf("connecting to %s (after 3 attempts): %v\nCheck that the host is correct, or run 'deco setup'", config.Host, authErr)
	}

	return client, config, nil
}

func runClients(jsonOut bool, nameFilter, macFilter string) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetClients(context.Background())
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
// The Client and DB fields are populated by pollLoop before setup/work are called,
// so work closures can capture the config and access them directly.
type pollLoopConfig struct {
	interval       int
	label          string // "Poll", "Monitor", "Watch"
	needsDB        bool
	ctx            context.Context // if nil, pollLoop creates its own via signal.NotifyContext
	maxFailures    int             // if 0, defaults to maxConsecutiveFailures (10)
	configOverride *Config         // if set, skip loadConfig/validateConfig
	setup          func(db *sql.DB, size int64) // optional, called once before the loop
	work           func(cycle int) error

	// Populated by pollLoop — available for use in setup and work closures.
	Client *DecoClient
	DB     *sql.DB
}

// pollLoop runs a polling loop with shared infrastructure: config loading,
// signal handling, client creation, auth with backoff, and DB management.
// It populates cfg.Client and cfg.DB before calling setup/work, so callers
// can capture cfg by pointer and access these fields in their closures.
func pollLoop(cfg *pollLoopConfig) error {
	if cfg.interval < 1 {
		return fmt.Errorf("interval must be at least 1 second, got %d", cfg.interval)
	}

	var config *Config
	if cfg.configOverride != nil {
		config = cfg.configOverride
	} else {
		var err error
		config, err = decoclient.LoadConfig()
		if err != nil {
			return err
		}
		if err := decoclient.ValidateConfig(config); err != nil {
			return err
		}
	}

	if cfg.needsDB {
		var dbErr error
		cfg.DB, dbErr = initDB()
		if dbErr != nil {
			return fmt.Errorf("initializing database: %v", dbErr)
		}
		defer cfg.DB.Close()

		ok, size := checkDBSizeLimit()
		if !ok {
			printDBLimitError(size, cfg.label)
			return fmt.Errorf("database size limit exceeded")
		}
		if cfg.setup != nil {
			cfg.setup(cfg.DB, size)
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
	cfg.ctx = ctx // make resolved context available to work closures

	cfg.Client = decoclient.NewDecoClient(config.Host, config.Password)
	defer cfg.Client.Logout()

	maxFail := maxConsecutiveFailures
	if cfg.maxFailures > 0 {
		maxFail = cfg.maxFailures
	}

	cycle := 0
	consecutiveFailures := 0
	totalFailures := 0
	baseDuration := time.Duration(cfg.interval) * time.Second
	loopStart := time.Now()

	printSummary := func() {
		elapsed := time.Since(loopStart).Truncate(time.Second)
		fmt.Printf("\n%s stopped. Completed %d cycles (%d failures) in %s.\n",
			cfg.label, cycle, totalFailures, elapsed)
	}

	for {
		select {
		case <-ctx.Done():
			printSummary()
			return nil
		default:
		}

		if cfg.needsDB {
			ok, size := checkDBSizeLimit()
			if !ok {
				printDBLimitError(size, cfg.label)
				return fmt.Errorf("database size limit exceeded")
			}
			checkDBCapacity(cfg.DB)
		}

		start := time.Now()

		if err := cfg.Client.EnsureAuthorized(ctx); err != nil {
			consecutiveFailures++
			totalFailures++
			if consecutiveFailures >= maxFail {
				printSummary()
				return fmt.Errorf("%d consecutive failures, giving up", consecutiveFailures)
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] Error connecting: %v, retrying in %s...\n", time.Now().Format("15:04:05"), err, wait)
			select {
			case <-ctx.Done():
				printSummary()
				return nil
			case <-time.After(wait):
			}
			continue
		}

		if err := cfg.work(cycle); err != nil {
			cfg.Client.Invalidate()
			consecutiveFailures++
			totalFailures++
			if consecutiveFailures >= maxFail {
				printSummary()
				return fmt.Errorf("%d consecutive failures, giving up", consecutiveFailures)
			}
			wait := backoff(consecutiveFailures, baseDuration, 5*time.Minute)
			fmt.Printf("[%s] Error: %v, retrying in %s...\n", time.Now().Format("15:04:05"), err, wait)
			select {
			case <-ctx.Done():
				printSummary()
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
				printSummary()
				return nil
			case <-time.After(sleepTime):
			}
		}
	}
}

func runWatch(interval int, nameFilter, macFilter string, maxFailures int) error {
	cfg := &pollLoopConfig{
		interval:    interval,
		label:       "Watch",
		needsDB:     false,
		maxFailures: maxFailures,
	}
	cfg.work = func(cycle int) error {
		data, err := cfg.Client.GetClients(cfg.ctx)
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
	}
	return pollLoop(cfg)
}

func runNetwork(jsonOut bool) error {
	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	data, err := client.GetNetwork(context.Background())
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

	data, err := client.GetWireless(context.Background())
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

	data, err := client.GetMesh(context.Background())
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

	var (
		network  *NetworkInfo
		wireless *WirelessInfo
		mesh     *MeshInfo
		clients  *ClientList
		netErr, wlErr, meshErr, cliErr error
		wg sync.WaitGroup
	)
	wg.Add(4)
	ctx := context.Background()
	go func() { defer wg.Done(); network, netErr = client.GetNetwork(ctx) }()
	go func() { defer wg.Done(); wireless, wlErr = client.GetWireless(ctx) }()
	go func() { defer wg.Done(); mesh, meshErr = client.GetMesh(ctx) }()
	go func() { defer wg.Done(); clients, cliErr = client.GetClients(ctx) }()
	wg.Wait()

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
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Router:    config.Host,
		Network:   network,
		Wireless:  wireless,
		Mesh:      mesh,
		Clients:   clients,
		Partial:   netErr != nil || wlErr != nil || meshErr != nil || cliErr != nil,
	}

	printJSON(data)
	return nil
}

func runAPI(endpoint, body string) error {
	// Reject path traversal and other suspicious characters in the endpoint.
	if strings.Contains(endpoint, "..") {
		return fmt.Errorf("invalid endpoint: path traversal not allowed")
	}

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	var reqData map[string]interface{}
	if err := json.Unmarshal([]byte(body), &reqData); err != nil {
		return fmt.Errorf("invalid JSON body: %v", err)
	}

	resp, err := client.Request(context.Background(), endpoint, reqData)
	if err != nil {
		return fmt.Errorf("API error: %v", err)
	}

	printJSON(resp)
	return nil
}

func runPoll(interval int, maxFailures int) error {
	cfg := &pollLoopConfig{
		interval:    interval,
		label:       "Poll",
		needsDB:     true,
		maxFailures: maxFailures,
		setup: func(db *sql.DB, size int64) {
			fmt.Printf("Starting bandwidth monitor (polling every %ds)\n", interval)
			fmt.Printf("Database: %s\n", dbpkg.DBPath())
			fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
			fmt.Println("Press Ctrl+C to stop")
		},
	}
	cfg.work = func(cycle int) error {
		data, err := cfg.Client.GetClients(cfg.ctx)
		if err != nil {
			return err
		}

		if err := storeBandwidthSamples(cfg.DB, data, time.Now().UTC().Format(time.RFC3339)); err != nil {
			decolog.Warn("store error: %v", err)
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

		decolog.Info("[%s] Sample #%d: %d clients, %d active, %d KB/s down %d KB/s up",
			time.Now().Format("15:04:05"), cycle+1, len(data.Clients), activeCount, totalDown, totalUp)
		return nil
	}
	return pollLoop(cfg)
}

// Thin wrappers delegating to internal/db.

func storeBandwidthSamples(database *sql.DB, clients *ClientList, ts string) error {
	return dbpkg.StoreBandwidthSamples(database, clients, ts)
}
func storeNetworkSnapshot(database *sql.DB, network *NetworkInfo, ts string) error {
	return dbpkg.StoreNetworkSnapshot(database, network, ts)
}
func storeMeshSnapshot(database *sql.DB, mesh *MeshInfo, ts string) error {
	return dbpkg.StoreMeshSnapshot(database, mesh, ts)
}
func storeWirelessSnapshot(database *sql.DB, wireless *WirelessInfo, ts string) error {
	return dbpkg.StoreWirelessSnapshot(database, wireless, ts)
}

// runMonitor starts a full network monitor that polls all data endpoints and stores
// results in the database. Status output (device counts, cycle summaries) goes to
// stdout; warnings and errors go to stderr. This is intentional Unix convention:
// stdout carries the data stream, stderr carries diagnostics.
func runMonitor(interval int, notify bool, alertThreshold int, webhookURL string, maxFailures int) error {
	var knownMACs map[string]bool
	var aliases map[string]string

	cfg := &pollLoopConfig{
		interval:    interval,
		label:       "Monitor",
		needsDB:     true,
		maxFailures: maxFailures,
		setup: func(db *sql.DB, size int64) {
			fmt.Printf("Starting full network monitor (polling every %ds)\n", interval)
			fmt.Printf("Database: %s\n", dbpkg.DBPath())
			fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
			if notify {
				knownMACs = loadKnownMACs(db)
				fmt.Printf("Known MACs: %d (notifications enabled)\n", len(knownMACs))
			}
			if alertThreshold > 0 {
				aliases = loadAliases()
				fmt.Printf("Bandwidth alert threshold: %dKB/s\n", alertThreshold)
			}
			if webhookURL != "" {
				fmt.Printf("Webhook: %s\n", webhookURL)
			}
			fmt.Println("Press Ctrl+C to stop")
		},
	}
	cfg.work = func(cycle int) error {
		client := cfg.Client
		db := cfg.DB

		var (
			clientData   *ClientList
			networkData  *NetworkInfo
			wirelessData *WirelessInfo
			meshData     *MeshInfo
			clientErr, networkErr, wirelessErr, meshErr error
			wg sync.WaitGroup
		)
		wg.Add(4)
		go func() { defer wg.Done(); clientData, clientErr = client.GetClients(cfg.ctx) }()
		go func() { defer wg.Done(); networkData, networkErr = client.GetNetwork(cfg.ctx) }()
		go func() { defer wg.Done(); wirelessData, wirelessErr = client.GetWireless(cfg.ctx) }()
		go func() { defer wg.Done(); meshData, meshErr = client.GetMesh(cfg.ctx) }()
		wg.Wait()

		// If all requests failed, session is probably dead
		if clientErr != nil && networkErr != nil && wirelessErr != nil && meshErr != nil {
			return fmt.Errorf("all requests failed")
		}

		timestamp := time.Now().UTC().Format(time.RFC3339)
		ts := time.Now().Format("15:04:05")

		clientCount := 0
		if clientErr == nil {
			clientCount = len(clientData.Clients)
			if err := storeBandwidthSamples(db, clientData, timestamp); err != nil {
				decolog.Warn("store error: %v", err)
			}
			for _, c := range clientData.Clients {
				if notify && knownMACs != nil {
					macUpper := strings.ToUpper(c.MAC)
					if !knownMACs[macUpper] {
						notifyNewMAC(c.MAC, c.Name, c.IP, webhookURL)
						knownMACs[macUpper] = true
					}
				}
				if alertThreshold > 0 {
					rate := c.DownloadKbps + c.UploadKbps
					if rate > alertThreshold {
						name := c.Name
						// Reload aliases each cycle so changes are picked up while running.
						aliases = loadAliases()
						if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
							name = alias
						}
						fmt.Printf("[%s] ALERT: %s — %dKB/s (threshold: %dKB/s)\n",
							ts, name, rate, alertThreshold)
						sendWebhook(webhookURL, WebhookPayload{
							Event:     "bandwidth_alert",
							Timestamp: time.Now().Format(time.RFC3339),
							Text:      fmt.Sprintf("ALERT: %s — %dKB/s (threshold: %dKB/s)", name, rate, alertThreshold),
							Data:      BandwidthAlertEvent{MAC: c.MAC, Name: name, RateKBps: rate, Threshold: alertThreshold},
						})
					}
				}
			}
		} else {
			decolog.Info("[%s] Error getting clients: %v", ts, clientErr)
		}

		var cpuPct, memPct *float64
		if networkErr == nil {
			cpuPct = networkData.Performance.CPUPercent
			memPct = networkData.Performance.MemPercent
			if err := storeNetworkSnapshot(db, networkData, timestamp); err != nil {
				decolog.Warn("store error: %v", err)
			}
		} else {
			decolog.Info("[%s] Error getting network: %v", ts, networkErr)
		}

		meshCount := 0
		if meshErr == nil {
			meshCount = len(meshData.Devices)
			if err := storeMeshSnapshot(db, meshData, timestamp); err != nil {
				decolog.Warn("store error: %v", err)
			}
		} else {
			decolog.Info("[%s] Error getting mesh: %v", ts, meshErr)
		}

		if wirelessErr == nil {
			if err := storeWirelessSnapshot(db, wirelessData, timestamp); err != nil {
				decolog.Warn("store error: %v", err)
			}
		} else {
			decolog.Info("[%s] Error getting wireless: %v", ts, wirelessErr)
		}

		cpuStr := "?"
		memStr := "?"
		if cpuPct != nil {
			cpuStr = fmt.Sprintf("%.0f%%", *cpuPct)
		}
		if memPct != nil {
			memStr = fmt.Sprintf("%.0f%%", *memPct)
		}

		decolog.Info("[%s] Cycle #%d: %d clients, CPU %s, Mem %s, %d mesh nodes",
			ts, cycle+1, clientCount, cpuStr, memStr, meshCount)
		return nil
	}
	return pollLoop(cfg)
}

func loadKnownMACs(database *sql.DB) map[string]bool { return dbpkg.LoadKnownMACs(database) }

var blockPrivateWebhooks = true // set to false in tests

// privateNets is the list of private/reserved CIDRs for SSRF protection.
var privateNets []*net.IPNet

func init() {
	for _, cidr := range []string{
		"0.0.0.0/8",
		"127.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
	} {
		_, n, _ := net.ParseCIDR(cidr)
		privateNets = append(privateNets, n)
	}
}

// isPrivateIP checks whether an IP is in a private/reserved range.
func isPrivateIP(ip net.IP) bool {
	// Unwrap IPv4-mapped IPv6 (e.g., ::ffff:192.168.1.1) to its IPv4 form.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.Equal(net.IPv6loopback) {
		return true
	}
	if len(ip) == net.IPv6len {
		// fc00::/7 — IPv6 ULA
		if ip[0]&0xfe == 0xfc {
			return true
		}
		// fe80::/10 — IPv6 link-local
		if ip[0] == 0xfe && ip[1]&0xc0 == 0x80 {
			return true
		}
	}
	for _, n := range privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ssrfSafeDialContext rejects connections to private/loopback addresses,
// preventing TOCTOU issues where DNS resolves differently between check and connect.
// When blockPrivateWebhooks is false (tests), the check is skipped.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !blockPrivateWebhooks {
		return dialer.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return nil, fmt.Errorf("webhook blocked: %s resolves to private address %s", host, ip.IP)
		}
	}
	// Connect to the first resolved IP to avoid re-resolution.
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// webhookClient uses a custom transport that validates resolved IPs at connect time,
// eliminating DNS TOCTOU issues. Redirects are disabled to prevent open-redirect SSRF.
var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: ssrfSafeDialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// sendWebhook is fire-and-forget: it logs warnings on failure rather than
// returning an error, because webhook delivery should never abort the caller's
// main operation (monitoring, alerting, etc.). This is intentional.
func sendWebhook(webhookURL string, payload WebhookPayload) {
	if webhookURL == "" {
		return
	}

	u, parseErr := url.Parse(webhookURL)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		decolog.Warn("webhook URL has invalid scheme (must be http or https): %s", webhookURL)
		return
	}

	if u.Scheme == "http" {
		decolog.Warn("webhook URL uses plain HTTP (not HTTPS): %s", webhookURL)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		decolog.Warn("webhook marshal error: %v", err)
		return
	}

	resp, err := webhookClient.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		decolog.Warn("webhook POST error: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		decolog.Warn("webhook returned status %d", resp.StatusCode)
	}
}

// notifyNewMAC is best-effort: it logs and sends notifications but does not
// return error, since notification failure should not interrupt the monitor loop.
func notifyNewMAC(mac, name, ip, webhookURL string) {
	// Sanitize for AppleScript embedding: replace double quotes, strip control chars
	safeName := strings.Map(func(r rune) rune {
		if r == '"' || r == '`' || r == '\\' {
			return '\''
		}
		if r < 32 {
			return -1 // strip control characters
		}
		return r
	}, name)
	msg := fmt.Sprintf("NEW DEVICE: %s (%s) at %s", safeName, mac, ip)
	decolog.Info("%s", msg)

	// Desktop notification (macOS only, best-effort, 5s timeout to avoid blocking monitor)
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`display notification %q with title "Deco: New Device"`, msg)
		notifyCtx, notifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer notifyCancel()
		cmd := exec.CommandContext(notifyCtx, "osascript", "-e", script)
		if err := cmd.Run(); err != nil {
			decolog.Debug("notification failed: %v", err)
		}
	}

	sendWebhook(webhookURL, WebhookPayload{
		Event:     "new_device",
		Timestamp: time.Now().Format(time.RFC3339),
		Text:      msg,
		Data:      NewDeviceEvent{MAC: mac, Name: name, IP: ip},
	})
}

func resolveDeviceMAC(db *sql.DB, identifier string) (string, error) {
	// If it looks like a MAC, normalize and return it
	if validMAC(identifier) {
		return strings.ToUpper(identifier), nil
	}

	// Check aliases (substring match) — collect all matches to detect ambiguity.
	aliases := loadAliases()
	lower := strings.ToLower(identifier)
	var aliasMatches []struct{ mac, alias string }
	for mac, alias := range aliases {
		if strings.Contains(strings.ToLower(alias), lower) {
			aliasMatches = append(aliasMatches, struct{ mac, alias string }{mac, alias})
		}
	}
	if len(aliasMatches) == 1 {
		return aliasMatches[0].mac, nil
	}
	if len(aliasMatches) > 1 {
		var names []string
		for _, m := range aliasMatches {
			names = append(names, fmt.Sprintf("  %s (%s)", m.alias, m.mac))
		}
		sort.Strings(names)
		return "", fmt.Errorf("ambiguous match for %q — multiple devices match:\n%s\nUse a MAC address or more specific name", identifier, strings.Join(names, "\n"))
	}

	// Search bandwidth_samples name field (LIKE) — collect distinct MACs to detect ambiguity.
	escaped := strings.ReplaceAll(strings.ReplaceAll(identifier, "!", "!!"), "%", "!%")
	escaped = strings.ReplaceAll(escaped, "_", "!_")
	rows, err := db.Query(`SELECT mac, name FROM bandwidth_samples WHERE LOWER(name) LIKE ? ESCAPE '!' GROUP BY mac ORDER BY MAX(timestamp) DESC`,
		"%"+strings.ToLower(escaped)+"%")
	if err == nil {
		defer rows.Close()
		var dbMatches []struct{ mac, name string }
		for rows.Next() {
			var m, n string
			if rows.Scan(&m, &n) == nil {
				dbMatches = append(dbMatches, struct{ mac, name string }{strings.ToUpper(m), n})
			}
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("querying device name: %v", err)
		}
		if len(dbMatches) == 1 {
			return dbMatches[0].mac, nil
		}
		if len(dbMatches) > 1 {
			var names []string
			for _, m := range dbMatches {
				names = append(names, fmt.Sprintf("  %s (%s)", m.name, m.mac))
			}
			return "", fmt.Errorf("ambiguous match for %q — multiple devices match:\n%s\nUse a MAC address or more specific name", identifier, strings.Join(names, "\n"))
		}
	}

	return "", fmt.Errorf("device %q not found", identifier)
}

func runReportDevice(identifier, period string, jsonOut, csvOut bool) error {
	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	mac, err := resolveDeviceMAC(db, identifier)
	if err != nil {
		return err
	}

	startTime, periodName, err := parsePeriod(period)
	if err != nil {
		return err
	}
	startStr := startTime.UTC().Format(time.RFC3339)

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	aliases := loadAliases()
	alias := aliases[mac]

	// Query 1: Aggregate totals
	var name sql.NullString
	var firstSeen, lastSeen sql.NullString
	var totalSamples int64
	var totalDown, totalUp float64
	var maxDown, maxUp int64

	err = tx.QueryRow(`
		SELECT
			name,
			MIN(timestamp) as first_seen,
			MAX(timestamp) as last_seen,
			COUNT(*) as samples,
			SUM(download_kbps) as total_down,
			SUM(upload_kbps) as total_up,
			MAX(download_kbps) as max_down,
			MAX(upload_kbps) as max_up
		FROM bandwidth_samples
		WHERE mac = ? AND timestamp >= ?`,
		mac, startStr).Scan(&name, &firstSeen, &lastSeen, &totalSamples, &totalDown, &totalUp, &maxDown, &maxUp)
	if err != nil {
		return fmt.Errorf("query error: %v", err)
	}

	if totalSamples == 0 {
		return fmt.Errorf("no data for device %s in period %s", mac, periodName)
	}

	intervalSec := estimateInterval(db, startTime)

	report := &DeviceReport{
		MAC:             mac,
		Name:            name.String,
		Alias:           alias,
		Period:          periodName,
		StartTime:       startTime.Format(time.RFC3339),
		QueryTime:       time.Now().Format(time.RFC3339),
		FirstSeen:       firstSeen.String,
		LastSeen:        lastSeen.String,
		IntervalSeconds: intervalSec,
		TotalSamples:    totalSamples,
		TotalDownloadKB: totalDown * float64(intervalSec),
		TotalUploadKB:   totalUp * float64(intervalSec),
		MaxDownloadKBps: maxDown,
		MaxUploadKBps:   maxUp,
	}

	// Query 2: Hourly timeline
	rows, err := tx.Query(`
		SELECT
			strftime('%Y-%m-%dT%H:00:00', timestamp, 'localtime') as hour,
			SUM(download_kbps) as down,
			SUM(upload_kbps) as up,
			COUNT(*) as samples
		FROM bandwidth_samples
		WHERE mac = ? AND timestamp >= ?
		GROUP BY hour
		ORDER BY hour`,
		mac, startStr)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b DeviceTimelineBucket
			var down, up float64
			if err := rows.Scan(&b.Timestamp, &down, &up, &b.SampleCount); err == nil {
				b.DownloadKB = down * float64(intervalSec)
				b.UploadKB = up * float64(intervalSec)
				report.Timeline = append(report.Timeline, b)
			}
		}
		if err := rows.Err(); err != nil {
			decolog.Warn("error iterating timeline rows: %v", err)
		}
	}

	// Query 3: Connection type breakdown
	connRows, err := tx.Query(`
		SELECT connection, COUNT(*) as samples
		FROM bandwidth_samples
		WHERE mac = ? AND timestamp >= ?
		GROUP BY connection
		ORDER BY samples DESC`,
		mac, startStr)
	if err == nil {
		defer connRows.Close()
		for connRows.Next() {
			var cb DeviceConnectionBreakdown
			if err := connRows.Scan(&cb.Connection, &cb.Samples); err == nil {
				cb.Percent = float64(cb.Samples) / float64(totalSamples) * 100
				report.Connections = append(report.Connections, cb)
			}
		}
		if err := connRows.Err(); err != nil {
			decolog.Warn("error iterating connection breakdown rows: %v", err)
		}
	}

	// Query 4: IP history
	ipRows, err := tx.Query(`
		SELECT ip, MIN(timestamp) as first_seen, MAX(timestamp) as last_seen, COUNT(*) as samples
		FROM bandwidth_samples
		WHERE mac = ? AND timestamp >= ? AND ip IS NOT NULL AND ip != ''
		GROUP BY ip
		ORDER BY first_seen`,
		mac, startStr)
	if err == nil {
		defer ipRows.Close()
		for ipRows.Next() {
			var iph DeviceIPHistory
			if err := ipRows.Scan(&iph.IP, &iph.FirstSeen, &iph.LastSeen, &iph.Samples); err == nil {
				report.IPHistory = append(report.IPHistory, iph)
			}
		}
		if err := ipRows.Err(); err != nil {
			decolog.Warn("error iterating IP history rows: %v", err)
		}
	}

	if jsonOut {
		printJSON(report)
	} else if csvOut {
		printDeviceReportCSV(report)
	} else {
		printDeviceReport(report)
	}
	return nil
}

func parsePeriod(period string) (time.Time, string, error) { return dbpkg.ParsePeriod(period) }
func estimateInterval(database dbpkg.Querier, since time.Time) int {
	return dbpkg.EstimateInterval(database, since)
}

func runReport(period string, jsonOut, csvOut bool, nameFilter, macFilter, group string) error {
	startTime, periodName, err := parsePeriod(period)
	if err != nil {
		return err
	}

	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	startStr := startTime.UTC().Format(time.RFC3339)

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

	rows, err := tx.Query(query, startStr)
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
		return fmt.Errorf("error iterating bandwidth rows: %v", err)
	}

	// Query connection-type breakdown per device.
	// This is a separate query because the main report query groups by mac only,
	// while the breakdown needs GROUP BY mac, connection. Combining them into a
	// single query would require a self-join or CTE with two different aggregation
	// levels, adding complexity without meaningful performance benefit since both
	// queries scan the same index (idx_timestamp) over the same time range.
	connRows, connErr := tx.Query(`SELECT mac, connection, COUNT(*) as samples
		FROM bandwidth_samples WHERE timestamp >= ? GROUP BY mac, connection`,
		startStr)
	if connErr == nil {
		defer connRows.Close()
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
		if err := connRows.Err(); err != nil {
			decolog.Warn("error iterating connection breakdown: %v", err)
		}
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
				lowerFilter := strings.ToLower(nameFilter)
				origName := strings.ToLower(d.Name)
				aliasName := ""
				if alias, ok := aliases[strings.ToUpper(d.MAC)]; ok {
					aliasName = strings.ToLower(alias)
				}
				if !strings.Contains(origName, lowerFilter) && !strings.Contains(aliasName, lowerFilter) {
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
		if len(tags) == 0 {
			return fmt.Errorf("no tags defined. Use 'deco tag <MAC> <tag>' to create tags")
		}
		var grouped []ReportDevice
		for _, d := range devices {
			for _, t := range tags[strings.ToUpper(d.MAC)] {
				if strings.EqualFold(t, group) {
					grouped = append(grouped, d)
					break
				}
			}
		}
		if len(grouped) == 0 {
			return fmt.Errorf("no devices found with tag %q. Use 'deco tag list' to see all tags", group)
		}
		devices = grouped
	}

	var totalSamples int64
	if err := tx.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startStr).Scan(&totalSamples); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to count samples: %v\n", err)
	}

	intervalSec := estimateInterval(tx, startTime)

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
	startTime, periodName, err := parsePeriod(period)
	if err != nil {
		return err
	}

	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2,
		       COALESCE(cpu_percent, 0), COALESCE(mem_percent, 0)
		FROM network_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp`, startTime.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("query error: %v", err)
	}
	defer rows.Close()

	var entries []NetworkReportEntry

	for rows.Next() {
		var e NetworkReportEntry
		if err := rows.Scan(&e.Timestamp, &e.WANIP, &e.Gateway, &e.DNS1, &e.DNS2, &e.CPU, &e.Memory); err != nil {
			decolog.Warn("scan network row: %v", err)
			continue
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating network rows: %v", err)
	}

	if jsonOut {
		printJSON(entries)
	} else {
		printNetworkReport(entries, periodName)
	}
	return nil
}

func runReportMesh(period string, jsonOut bool) error {
	startTime, periodName, err := parsePeriod(period)
	if err != nil {
		return err
	}

	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT timestamp, name, role, ip, mac, status, firmware
		FROM mesh_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp`, startTime.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("query error: %v", err)
	}
	defer rows.Close()

	var entries []MeshReportEntry
	for rows.Next() {
		var e MeshReportEntry
		if err := rows.Scan(&e.Timestamp, &e.Name, &e.Role, &e.IP, &e.MAC, &e.Status, &e.Firmware); err != nil {
			decolog.Warn("scan mesh row: %v", err)
			continue
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating mesh rows: %v", err)
	}

	if jsonOut {
		printJSON(entries)
	} else {
		printMeshReport(entries, periodName)
	}
	return nil
}

// statusInfo holds database statistics for JSON output.
type statusInfo struct {
	Database      string `json:"database"`
	SizeBytes     int64  `json:"size_bytes"`
	SizeMB        string `json:"size_mb"`
	LimitGB       string `json:"limit_gb"`
	TotalSamples  int64  `json:"total_samples"`
	UniqueDevices int64  `json:"unique_devices"`
	FirstSample   string `json:"first_sample"`
	LastSample    string `json:"last_sample"`
}

func runStatus(jsonOut bool) error {
	if _, err := os.Stat(dbpkg.DBPath()); os.IsNotExist(err) {
		fmt.Println("No database found. Run 'poll' first to collect data.")
		return nil
	}

	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	defer tx.Rollback()

	var totalSamples, uniqueDevices int64
	var firstSample, lastSample sql.NullString

	if err := tx.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples); err != nil {
		return fmt.Errorf("failed to count samples: %v", err)
	}
	if err := tx.QueryRow("SELECT COUNT(DISTINCT mac) FROM bandwidth_samples").Scan(&uniqueDevices); err != nil {
		return fmt.Errorf("failed to count devices: %v", err)
	}
	if err := tx.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM bandwidth_samples").Scan(&firstSample, &lastSample); err != nil {
		return fmt.Errorf("failed to get time range: %v", err)
	}

	size := getDBSize()

	if jsonOut {
		printJSON(statusInfo{
			Database:      dbpkg.DBPath(),
			SizeBytes:     size,
			SizeMB:        fmt.Sprintf("%.2f", float64(size)/(1024*1024)),
			LimitGB:       fmt.Sprintf("%.0f", float64(DBSizeLimitBytes)/(1024*1024*1024)),
			TotalSamples:  totalSamples,
			UniqueDevices: uniqueDevices,
			FirstSample:   firstSample.String,
			LastSample:    lastSample.String,
		})
		return nil
	}

	fmt.Printf("\nDatabase: %s\n", dbpkg.DBPath())
	fmt.Printf("Size: %.2f MB / %.0f GB limit\n", float64(size)/(1024*1024), float64(DBSizeLimitBytes)/(1024*1024*1024))
	fmt.Printf("Total samples: %d\n", totalSamples)
	fmt.Printf("Unique devices: %d\n", uniqueDevices)
	fmt.Printf("First sample: %s\n", firstSample.String)
	fmt.Printf("Last sample: %s\n", lastSample.String)
	return nil
}

func runPurge(force bool, beforeStr string, days int) error {
	if _, err := os.Stat(dbpkg.DBPath()); os.IsNotExist(err) {
		fmt.Println("No database found. Nothing to purge.")
		return nil
	}

	db, err := initDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var cutoff time.Time
	selective := false
	if beforeStr != "" {
		cutoff, err = time.Parse("2006-01-02", beforeStr)
		if err != nil {
			return fmt.Errorf("invalid date %q (use YYYY-MM-DD)", beforeStr)
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
			fmt.Printf("Database: %s\n", dbpkg.DBPath())
			ok, err := confirmAction("Type 'yes' to confirm: ")
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("Purge cancelled.")
				return nil
			}
		}

		deleted, err := purgeByDate(db, cutoff)
		if err != nil {
			return fmt.Errorf("purge failed: %v", err)
		}

		fmt.Printf("Purged %d records older than %s\n", deleted, cutoff.Format("2006-01-02"))
		fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
		return nil
	}

	// Full purge — count all tables
	var totalRecords int64
	for _, table := range allTables {
		var c int64
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&c); err != nil {
			return fmt.Errorf("counting records in %s: %v", table, err)
		}
		totalRecords += c
	}

	if !force {
		fmt.Printf("\nWARNING: This will delete ALL %d records across all tables!\n", totalRecords)
		fmt.Printf("Database: %s\n", dbpkg.DBPath())
		ok, err := confirmAction("Type 'yes' to confirm: ")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Purge cancelled.")
			return nil
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %v", err)
	}
	for _, table := range allTables {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			tx.Rollback()
			return fmt.Errorf("deleting from %s: %v", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge: %v", err)
	}

	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		decolog.Warn("WAL checkpoint failed: %v", err)
	}

	fmt.Printf("Purged all records from database.\n")
	fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
	return nil
}

var allTables = dbpkg.AllTables

func countBeforeDate(database *sql.DB, cutoff time.Time) (int64, error) {
	return dbpkg.CountBeforeDate(database, cutoff)
}
func purgeByDate(database *sql.DB, cutoff time.Time) (int64, error) {
	return dbpkg.PurgeByDate(database, cutoff)
}

func runReboot(force bool) error {
	if !force {
		ok, err := confirmAction("Reboot the router? This will disconnect all devices. Type 'yes' to confirm: ")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Reboot cancelled.")
			return nil
		}
	}

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	if err := client.Reboot(context.Background()); err != nil {
		return fmt.Errorf("rebooting: %v", err)
	}

	fmt.Println("Reboot command sent. The router will restart shortly.")
	return nil
}

// runBlockAction contains the shared logic for blocking/unblocking a device.
func runBlockAction(mac string, block bool) error {
	if !validMAC(mac) {
		// Try resolving as alias or device name
		db, err := initDB()
		if err != nil {
			return fmt.Errorf("invalid MAC address %q (and cannot open DB to resolve name: %v)", mac, err)
		}
		defer db.Close()
		resolved, err := resolveDeviceMAC(db, mac)
		if err != nil {
			return fmt.Errorf("invalid MAC address %q and could not resolve as device name", mac)
		}
		mac = resolved
	}
	mac = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(mac, ":", "-"), ".", "-"))

	client, _, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Logout()

	if block {
		if err := client.BlockClient(context.Background(), mac); err != nil {
			return fmt.Errorf("blocking device: %v", err)
		}
		fmt.Printf("Blocked device %s\n", mac)
	} else {
		if err := client.UnblockClient(context.Background(), mac); err != nil {
			return fmt.Errorf("unblocking device: %v", err)
		}
		fmt.Printf("Unblocked device %s\n", mac)
	}
	return nil
}

func runBlock(mac string) error {
	return runBlockAction(mac, true)
}

func runUnblock(mac string) error {
	return runBlockAction(mac, false)
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
		if err := saveAliases(aliases); err != nil {
			return err
		}
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
	if err := saveAliases(aliases); err != nil {
		return err
	}
	fmt.Printf("Set alias: %s -> %s\n", mac, name)
	return nil
}

// tagCache caches the parsed tags file to avoid re-reading on every call.
var tagMu sync.RWMutex
var tagCache struct {
	modTime time.Time
	data    map[string][]string
}

func loadTags() map[string][]string {
	path := paths.CfgPath("deco_tags.json")
	info, err := os.Stat(path)
	if err != nil {
		return map[string][]string{}
	}

	tagMu.RLock()
	if tagCache.data != nil && info.ModTime().Equal(tagCache.modTime) {
		clone := cloneTags(tagCache.data)
		tagMu.RUnlock()
		return clone
	}
	tagMu.RUnlock()

	// Take write lock and double-check before reading file
	tagMu.Lock()
	defer tagMu.Unlock()
	if tagCache.data != nil && info.ModTime().Equal(tagCache.modTime) {
		return cloneTags(tagCache.data)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return map[string][]string{}
	}

	var tags map[string][]string
	if err := json.Unmarshal(data, &tags); err != nil {
		decolog.Warn("corrupt %s file, ignoring: %v", "deco_tags.json", err)
		return map[string][]string{}
	}

	tagCache.modTime = info.ModTime()
	tagCache.data = tags
	return cloneTags(tags)
}

// resetCaches clears the in-process alias and tag caches. Used by tests.
func resetCaches() {
	aliasMu.Lock()
	aliasCache.data = nil
	aliasCache.modTime = time.Time{}
	aliasMu.Unlock()

	tagMu.Lock()
	tagCache.data = nil
	tagCache.modTime = time.Time{}
	tagMu.Unlock()
}

func cloneTags(m map[string][]string) map[string][]string {
	c := make(map[string][]string, len(m))
	for k, v := range m {
		c[k] = append([]string(nil), v...)
	}
	return c
}

func saveTags(tags map[string][]string) error {
	if err := paths.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config dir: %v", err)
	}

	// Clean up empty tag lists
	for mac, t := range tags {
		if len(t) == 0 {
			delete(tags, mac)
		}
	}

	data, err := json.MarshalIndent(tags, "", "  ")
	if err != nil {
		return fmt.Errorf("saving tags: %v", err)
	}

	if err := atomicWriteFile(paths.CfgPath("deco_tags.json"), data, 0600); err != nil {
		return fmt.Errorf("saving tags: %v", err)
	}
	// Invalidate tag cache so next loadTags picks up the change
	tagMu.Lock()
	tagCache.data = nil
	tagCache.modTime = time.Time{}
	tagMu.Unlock()
	return nil
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
	if err := saveTags(tags); err != nil {
		return err
	}
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
	if err := saveTags(tags); err != nil {
		return err
	}
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

// aliasCache caches the parsed aliases file to avoid re-reading on every call.
var aliasMu sync.RWMutex
var aliasCache struct {
	modTime time.Time
	data    map[string]string
}

func loadAliases() map[string]string {
	path := paths.CfgPath("deco_aliases.json")
	info, err := os.Stat(path)
	if err != nil {
		return map[string]string{}
	}

	aliasMu.RLock()
	if aliasCache.data != nil && info.ModTime().Equal(aliasCache.modTime) {
		clone := maps.Clone(aliasCache.data)
		aliasMu.RUnlock()
		return clone
	}
	aliasMu.RUnlock()

	// Take write lock and double-check before reading file
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if aliasCache.data != nil && info.ModTime().Equal(aliasCache.modTime) {
		return maps.Clone(aliasCache.data)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}

	var aliases map[string]string
	if err := json.Unmarshal(data, &aliases); err != nil {
		decolog.Warn("corrupt %s file, ignoring: %v", "deco_aliases.json", err)
		return map[string]string{}
	}

	aliasCache.modTime = info.ModTime()
	aliasCache.data = aliases
	return maps.Clone(aliases)
}

// atomicWriteFile writes data to a temp file then renames it into place,
// preventing file corruption from interrupted writes.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveAliases(aliases map[string]string) error {
	if err := paths.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config dir: %v", err)
	}

	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return fmt.Errorf("saving aliases: %v", err)
	}

	if err := atomicWriteFile(paths.CfgPath("deco_aliases.json"), data, 0600); err != nil {
		return fmt.Errorf("saving aliases: %v", err)
	}
	// Invalidate alias cache so next loadAliases picks up the change
	aliasMu.Lock()
	aliasCache.data = nil
	aliasCache.modTime = time.Time{}
	aliasMu.Unlock()
	return nil
}

func filterClients(data *ClientList, nameFilter, macFilter string) *ClientList {
	var filtered []ClientInfo
	// loadAliases uses file-based caching (aliasCache); safe to call on each render cycle.
	aliases := loadAliases()

	for _, c := range data.Clients {
		if nameFilter != "" {
			lowerFilter := strings.ToLower(nameFilter)
			origName := strings.ToLower(c.Name)
			aliasName := ""
			if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
				aliasName = strings.ToLower(alias)
			}
			if !strings.Contains(origName, lowerFilter) && !strings.Contains(aliasName, lowerFilter) {
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

// confirmAction prompts for "yes" confirmation. Returns an error if stdin is
// not a terminal (pipe/redirect), directing the user to use --force instead.
func confirmAction(prompt string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("cannot confirm interactively: stdin is not a terminal. Use --force to skip confirmation")
	}
	fmt.Print(prompt)
	var confirm string
	fmt.Scanln(&confirm)
	return strings.ToLower(confirm) == "yes", nil
}

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

