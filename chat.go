package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chzyer/readline"
)

const defaultOllamaModel = "llama3.2"
const defaultOllamaURL = "http://localhost:11434"

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaChatChunk struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

// isTerminal reports whether f is connected to a terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ollamaModelInfo represents a model from the Ollama API.
type ollamaModelInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

// listOllamaModels queries the Ollama API for available models and prints them.
func listOllamaModels(ollamaURL string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("cannot reach Ollama at %s: %v\nIs Ollama running? Start it with: ollama serve", ollamaURL, err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []ollamaModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse model list: %v", err)
	}

	if len(result.Models) == 0 {
		fmt.Printf("No models installed. Pull one with: ollama pull %s\n", defaultOllamaModel)
		return nil
	}

	fmt.Printf("%-30s %-10s %s\n", "NAME", "SIZE", "MODIFIED")
	for _, m := range result.Models {
		size := formatSize(m.Size)
		modified := m.ModifiedAt
		if t, err := time.Parse(time.RFC3339Nano, m.ModifiedAt); err == nil {
			modified = t.Format("2006-01-02 15:04")
		}
		fmt.Printf("%-30s %-10s %s\n", m.Name, size, modified)
	}
	return nil
}

// resolveModel checks if the requested model is available on Ollama.
// If not found, it auto-selects the first available model and prints a notice.
func resolveModel(ollamaURL, model string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return model, nil // can't check, proceed with requested model
	}
	defer resp.Body.Close()

	var result struct {
		Models []ollamaModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return model, nil
	}

	if len(result.Models) == 0 {
		return "", fmt.Errorf("no models installed. Pull one with: ollama pull %s", model)
	}

	// Check if requested model is available (exact or prefix match)
	for _, m := range result.Models {
		if m.Name == model || strings.HasPrefix(m.Name, model+":") {
			return model, nil
		}
	}

	// Model not found — auto-select first available
	selected := result.Models[0].Name
	names := make([]string, len(result.Models))
	for i, m := range result.Models {
		names[i] = m.Name
	}
	fmt.Fprintf(os.Stderr, "Model %q not found. Using %q instead.\n", model, selected)
	fmt.Fprintf(os.Stderr, "Available: %s\n", strings.Join(names, ", "))
	fmt.Fprintf(os.Stderr, "Use --list-models to see all, or --model <name> to specify.\n")
	return selected, nil
}

// checkOllama verifies that an Ollama instance is reachable.
func checkOllama(ollamaURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("cannot reach Ollama at %s: %v\nIs Ollama running? Start it with: ollama serve", ollamaURL, err)
	}
	resp.Body.Close()
	return nil
}

// Context limits to keep the system prompt within reasonable token budgets.
const (
	maxBandwidthDevices = 15
	maxKnownDevices     = 50
	maxNetworkSnapshots = 200
	maxMeshSnapshots    = 200
	// Compact mode limits
	compactBandwidthDevices = 10
	compactKnownDevices     = 25
	compactNetworkSnapshots = 50
	compactMeshSnapshots    = 50
)

// gatherNetworkContext builds a structured text prompt with live router data and/or DB history.
// When compact is true, limits are tighter to fit smaller context windows.
// If db is non-nil it is used for historical queries; otherwise the function opens its own connection.
func gatherNetworkContext(compact bool, db ...*sql.DB) string {
	bandwidthLimit := maxBandwidthDevices
	knownLimit := maxKnownDevices
	netSnapLimit := maxNetworkSnapshots
	meshSnapLimit := maxMeshSnapshots
	if compact {
		bandwidthLimit = compactBandwidthDevices
		knownLimit = compactKnownDevices
		netSnapLimit = compactNetworkSnapshots
		meshSnapLimit = compactMeshSnapshots
	}
	var sb strings.Builder
	sb.WriteString("You are a network assistant for a TP-Link Deco mesh router system.\n")
	sb.WriteString("Answer questions about the network data below. Be concise.\n")
	sb.WriteString("IMPORTANT: Only state facts present in the data below. Do not guess or invent device model names, MAC addresses, IP addresses, or any other details not explicitly shown.\n")

	aliases := loadAliases()

	// Try live router data
	liveOK := false
	client, _, err := connectClient()
	if err == nil {
		defer client.Logout()
		liveOK = true

		// Clients
		if clients, err := client.GetClients(); err == nil && clients != nil {
			sb.WriteString(fmt.Sprintf("\n=== CONNECTED DEVICES (%d) ===\n", clients.Count))
			sb.WriteString(fmt.Sprintf("%-25s %-16s %-18s %-14s %-10s %-10s\n",
				"NAME", "IP", "MAC", "CONNECTION", "DOWN", "UP"))
			for _, c := range clients.Clients {
				name := c.Name
				if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
					name = alias
				}
				if len(name) > 24 {
					name = name[:24]
				}
				down := "-"
				up := "-"
				if c.DownloadKbps > 0 {
					down = fmt.Sprintf("%dKB/s", c.DownloadKbps)
				}
				if c.UploadKbps > 0 {
					up = fmt.Sprintf("%dKB/s", c.UploadKbps)
				}
				sb.WriteString(fmt.Sprintf("%-25s %-16s %-18s %-14s %-10s %-10s\n",
					name, c.IP, c.MAC, c.Connection, down, up))
			}
		}

		// Network
		if net, err := client.GetNetwork(); err == nil && net != nil {
			sb.WriteString("\n=== NETWORK ===\n")
			sb.WriteString(fmt.Sprintf("WAN IP: %s | Gateway: %s", net.WAN.IP, net.WAN.Gateway))
			if net.Performance.CPUPercent != nil {
				sb.WriteString(fmt.Sprintf(" | CPU: %.0f%%", *net.Performance.CPUPercent))
			}
			if net.Performance.MemPercent != nil {
				sb.WriteString(fmt.Sprintf(" | Memory: %.0f%%", *net.Performance.MemPercent))
			}
			sb.WriteString("\n")
		}

		// Mesh
		if mesh, err := client.GetMesh(); err == nil && mesh != nil {
			sb.WriteString(fmt.Sprintf("\n=== MESH NODES (%d) ===\n", mesh.Count))
			for _, d := range mesh.Devices {
				sb.WriteString(fmt.Sprintf("%s (%s) - %s - %s - %s\n",
					d.Name, d.Role, d.IP, d.Status, d.Firmware))
			}
		}

		// Wireless
		if wifi, err := client.GetWireless(); err == nil && wifi != nil {
			sb.WriteString("\n=== WIRELESS ===\n")
			bandNames := make([]string, 0, len(wifi.Bands))
			for k := range wifi.Bands {
				bandNames = append(bandNames, k)
			}
			sort.Strings(bandNames)
			for _, bName := range bandNames {
				b := wifi.Bands[bName]
				status := "off"
				if b.Host.Enabled {
					status = "on"
				}
				sb.WriteString(fmt.Sprintf("%s: %s (%s, ch %s, %s)\n",
					bName, b.Host.SSID, status, b.Host.Channel, b.Host.ChannelWidth))
			}
		}
	}

	if !liveOK {
		sb.WriteString("\n[NOTE: Live router data unavailable — showing historical data only]\n")
	}

	// Known device aliases
	if len(aliases) > 0 {
		sb.WriteString("\n=== KNOWN DEVICES (aliases) ===\n")
		sb.WriteString(fmt.Sprintf("%-18s  %s\n", "MAC", "NAME"))
		macs := make([]string, 0, len(aliases))
		for mac := range aliases {
			macs = append(macs, mac)
		}
		sort.Strings(macs)
		for _, mac := range macs {
			sb.WriteString(fmt.Sprintf("%-18s  %s\n", mac, aliases[mac]))
		}
	}

	// Historical data from DB
	var histDB *sql.DB
	var closeDB bool
	if len(db) > 0 && db[0] != nil {
		histDB = db[0]
	} else if d, err := initDB(); err == nil {
		histDB = d
		closeDB = true
	}
	if histDB != nil {
		if closeDB {
			defer histDB.Close()
		}
		now := time.Now()
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todayStr := startOfDay.Format(time.RFC3339)

		appendBandwidthHistory(&sb, histDB, aliases, bandwidthLimit, startOfDay, todayStr)
		appendNetworkHistory(&sb, histDB, netSnapLimit, todayStr)
		appendMeshHistory(&sb, histDB, meshSnapLimit, todayStr)
		appendKnownDevices(&sb, histDB, aliases, knownLimit)
	}

	return sb.String()
}

// appendBandwidthHistory adds the "TOP BANDWIDTH TODAY" section to the context.
func appendBandwidthHistory(sb *strings.Builder, db *sql.DB, aliases map[string]string, limit int, startOfDay time.Time, todayStr string) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT mac, name,
			SUM(download_kbps) as total_download,
			SUM(upload_kbps) as total_upload
		FROM bandwidth_samples
		WHERE timestamp >= ?
		GROUP BY mac
		ORDER BY (total_download + total_upload) DESC
		LIMIT %d
	`, limit), todayStr)
	if err != nil {
		return
	}
	defer rows.Close()

	var entries []struct {
		mac, name string
		down, up  int64
	}
	for rows.Next() {
		var mac, name sql.NullString
		var down, up int64
		if err := rows.Scan(&mac, &name, &down, &up); err == nil {
			if down+up > 0 {
				entries = append(entries, struct {
					mac, name string
					down, up  int64
				}{mac.String, name.String, down, up})
			}
		}
	}
	if len(entries) == 0 {
		return
	}

	interval := estimateInterval(db, startOfDay)
	sb.WriteString("\n=== TOP BANDWIDTH TODAY ===\n")
	sb.WriteString(fmt.Sprintf("%-25s %-14s %-14s\n", "NAME", "DOWNLOAD", "UPLOAD"))
	for _, e := range entries {
		name := e.name
		if alias, ok := aliases[strings.ToUpper(e.mac)]; ok {
			name = alias
		}
		if len(name) > 24 {
			name = name[:24]
		}
		if name == "" {
			name = e.mac
		}
		totalDown := float64(e.down * int64(interval))
		totalUp := float64(e.up * int64(interval))
		sb.WriteString(fmt.Sprintf("%-25s %-14s %-14s\n",
			name, formatBytes(totalDown), formatBytes(totalUp)))
	}
}

// appendNetworkHistory adds the "WAN IP HISTORY" and performance summary sections.
func appendNetworkHistory(sb *strings.Builder, db *sql.DB, limit int, todayStr string) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT timestamp, wan_ip,
			COALESCE(cpu_percent, 0), COALESCE(mem_percent, 0)
		FROM network_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp
		LIMIT %d`, limit), todayStr)
	if err != nil {
		return
	}
	defer rows.Close()

	type netSnap struct {
		ts, ip   string
		cpu, mem float64
	}
	var snaps []netSnap
	for rows.Next() {
		var s netSnap
		if err := rows.Scan(&s.ts, &s.ip, &s.cpu, &s.mem); err == nil {
			snaps = append(snaps, s)
		}
	}
	if len(snaps) == 0 {
		return
	}

	sb.WriteString("\n=== WAN IP HISTORY (today) ===\n")
	currentIP := ""
	firstSeen := ""
	for _, s := range snaps {
		if s.ip != currentIP {
			if currentIP != "" {
				sb.WriteString(fmt.Sprintf("%s  %s -> %s\n", currentIP, firstSeen, s.ts))
			}
			currentIP = s.ip
			firstSeen = s.ts
		}
	}
	if currentIP != "" {
		sb.WriteString(fmt.Sprintf("%s  %s -> now\n", currentIP, firstSeen))
	}

	var sumCPU, sumMem, maxCPU, maxMem float64
	for _, s := range snaps {
		sumCPU += s.cpu
		sumMem += s.mem
		if s.cpu > maxCPU {
			maxCPU = s.cpu
		}
		if s.mem > maxMem {
			maxMem = s.mem
		}
	}
	n := float64(len(snaps))
	sb.WriteString(fmt.Sprintf("\nPerformance (%d snapshots): CPU avg %.1f%% max %.1f%% | Memory avg %.1f%% max %.1f%%\n",
		len(snaps), sumCPU/n, maxCPU, sumMem/n, maxMem))
}

// appendMeshHistory adds the "MESH NODE UPTIME" section.
func appendMeshHistory(sb *strings.Builder, db *sql.DB, limit int, todayStr string) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT name, role, mac, status, firmware
		FROM mesh_snapshots
		WHERE timestamp >= ?
		ORDER BY timestamp
		LIMIT %d`, limit), todayStr)
	if err != nil {
		return
	}
	defer rows.Close()

	type nodeStats struct {
		name, role, firmware string
		total, online        int
	}
	nodes := map[string]*nodeStats{}
	var nodeOrder []string
	for rows.Next() {
		var name, role, mac, status, firmware sql.NullString
		if err := rows.Scan(&name, &role, &mac, &status, &firmware); err == nil {
			key := mac.String
			ns, ok := nodes[key]
			if !ok {
				ns = &nodeStats{name: name.String, role: role.String, firmware: firmware.String}
				nodes[key] = ns
				nodeOrder = append(nodeOrder, key)
			}
			ns.total++
			if status.String == "online" {
				ns.online++
			}
		}
	}
	if len(nodes) == 0 {
		return
	}

	sb.WriteString("\n=== MESH NODE UPTIME (today) ===\n")
	sb.WriteString(fmt.Sprintf("%-20s %-8s %-18s %-12s %s\n", "NAME", "ROLE", "MAC", "FIRMWARE", "UPTIME"))
	for _, mac := range nodeOrder {
		ns := nodes[mac]
		uptime := float64(ns.online) / float64(ns.total) * 100
		sb.WriteString(fmt.Sprintf("%-20s %-8s %-18s %-12s %.1f%%\n",
			ns.name, ns.role, mac, ns.firmware, uptime))
	}
}

// appendKnownDevices adds the "ALL KNOWN DEVICES" section.
func appendKnownDevices(sb *strings.Builder, db *sql.DB, aliases map[string]string, limit int) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT mac, name, MAX(timestamp) as last_seen, COUNT(*) as samples
		FROM bandwidth_samples
		GROUP BY mac
		ORDER BY last_seen DESC
		LIMIT %d`, limit))
	if err != nil {
		return
	}
	defer rows.Close()

	type knownDev struct {
		mac, name, lastSeen string
		samples             int64
	}
	var devs []knownDev
	for rows.Next() {
		var mac, name sql.NullString
		var lastSeen string
		var samples int64
		if err := rows.Scan(&mac, &name, &lastSeen, &samples); err == nil {
			devs = append(devs, knownDev{mac.String, name.String, lastSeen, samples})
		}
	}
	if len(devs) == 0 {
		return
	}

	sb.WriteString("\n=== ALL KNOWN DEVICES (from history) ===\n")
	sb.WriteString(fmt.Sprintf("%-25s %-18s %-22s %s\n", "NAME", "MAC", "LAST SEEN", "SAMPLES"))
	for _, d := range devs {
		name := d.name
		if alias, ok := aliases[strings.ToUpper(d.mac)]; ok {
			name = alias
		}
		if name == "" {
			name = "(unknown)"
		}
		if len(name) > 24 {
			name = name[:24]
		}
		sb.WriteString(fmt.Sprintf("%-25s %-18s %-22s %d\n",
			name, d.mac, d.lastSeen, d.samples))
	}
}

// streamOllamaChat sends a chat request to Ollama and streams the response to w.
// The provided context controls the lifetime of the request. A 5-minute timeout
// is applied on top of the caller's context so streaming cannot hang forever.
// Returns the full assembled response text.
func streamOllamaChat(ctx context.Context, ollamaURL string, req ollamaChatRequest, w io.Writer) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %v", err)
	}

	fmt.Fprint(os.Stderr, "Thinking...")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaURL+"/api/chat", strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprint(os.Stderr, "\r            \r")
		return "", fmt.Errorf("creating request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		fmt.Fprint(os.Stderr, "\r            \r")
		return "", fmt.Errorf("ollama request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprint(os.Stderr, "\r            \r")
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	firstToken := true
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chunk ollamaChatChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			return full.String(), fmt.Errorf("decoding stream chunk: %v", err)
		}
		if chunk.Message.Content != "" {
			if firstToken {
				fmt.Fprint(os.Stderr, "\r            \r")
				firstToken = false
			}
			fmt.Fprint(w, chunk.Message.Content)
			full.WriteString(chunk.Message.Content)
		}
		if chunk.Done {
			if firstToken {
				fmt.Fprint(os.Stderr, "\r            \r")
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("reading stream: %v", err)
	}

	return full.String(), nil
}

// resolveOllamaURL returns the effective Ollama URL, preferring the OLLAMA_HOST
// environment variable when the flag value is the default localhost URL.
func resolveOllamaURL(flagURL string) string {
	if flagURL == defaultOllamaURL {
		if host := os.Getenv("OLLAMA_HOST"); host != "" {
			return host
		}
	}
	return flagURL
}

// runChat is the main entry point for the chat command.
func runChat(model, ollamaURL, query string, compact, showContext bool) error {
	ollamaURL = resolveOllamaURL(ollamaURL)

	if err := checkOllama(ollamaURL); err != nil {
		return err
	}

	// Auto-detect model
	resolved, err := resolveModel(ollamaURL, model)
	if err != nil {
		return err
	}
	model = resolved

	// Open DB once and reuse for context gathering (and refresh calls)
	chatDB, _ := initDB()
	if chatDB != nil {
		defer chatDB.Close()
	}

	fmt.Fprint(os.Stderr, "Gathering network data... ")
	systemPrompt := gatherNetworkContext(compact, chatDB)
	fmt.Fprintln(os.Stderr, "done.")

	if showContext {
		fmt.Fprintln(os.Stderr, systemPrompt)
		return nil
	}

	messages := []ollamaMessage{
		{Role: "system", Content: systemPrompt},
	}

	// Single-query mode
	if query != "" {
		messages = append(messages, ollamaMessage{Role: "user", Content: query})
		req := ollamaChatRequest{
			Model:    model,
			Messages: messages,
			Stream:   true,
		}
		_, err := streamOllamaChat(context.Background(), ollamaURL, req, os.Stdout)
		fmt.Println()
		return err
	}

	// Track current snapshot for refresh diff
	currentSnapshot := parseNetworkSnapshot(systemPrompt)

	// REPL mode — use readline for terminal, fall back to basic scanner for pipes/tests
	if !isTerminal(os.Stdin) {
		return runChatBasicREPL(ollamaURL, model, compact, messages, currentSnapshot, chatDB)
	}
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "you> ",
		HistoryFile:     cfgPath("chat_history"),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return runChatBasicREPL(ollamaURL, model, compact, messages, currentSnapshot, chatDB)
	}
	defer rl.Close()

	fmt.Println("Network AI Chat (type 'exit' to quit, 'refresh' to reload, 'save' to export)")
	fmt.Println()

	state := &chatState{
		messages:        messages,
		currentSnapshot: currentSnapshot,
		compact:         compact,
		chatDB:          chatDB,
		ollamaURL:       ollamaURL,
		model:           model,
	}

	for {
		input, err := rl.Readline()
		if err != nil {
			// EOF (Ctrl+D) or Ctrl+C
			fmt.Println()
			break
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		shouldExit, err := processChatCommand(input, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}
		if shouldExit {
			break
		}
	}

	return nil
}

// runChatBasicREPL is a fallback REPL when readline is unavailable (e.g., piped stdin in tests).
func runChatBasicREPL(ollamaURL, model string, compact bool, messages []ollamaMessage, currentSnapshot networkSnapshot, chatDB *sql.DB) error {
	fmt.Println("Network AI Chat (type 'exit' to quit, 'refresh' to reload, 'save' to export)")
	fmt.Println()

	state := &chatState{
		messages:        messages,
		currentSnapshot: currentSnapshot,
		compact:         compact,
		chatDB:          chatDB,
		ollamaURL:       ollamaURL,
		model:           model,
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			fmt.Println()
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		shouldExit, err := processChatCommand(input, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}
		if shouldExit {
			break
		}
	}

	return nil
}

// chatState holds the mutable state shared across REPL iterations.
// It is passed to processChatCommand so the readline and basic-scanner
// REPL loops can share command-processing logic.
type chatState struct {
	messages        []ollamaMessage
	currentSnapshot networkSnapshot
	compact         bool
	chatDB          *sql.DB
	ollamaURL       string
	model           string
}

// processChatCommand handles a single line of user input in the REPL.
// It returns (shouldExit, error). When shouldExit is true the caller
// should break out of the read loop.
func processChatCommand(input string, state *chatState) (bool, error) {
	if input == "exit" || input == "quit" {
		return true, nil
	}
	if input == "refresh" {
		fmt.Fprint(os.Stderr, "Refreshing network data... ")
		oldSnapshot := state.currentSnapshot
		newPrompt := gatherNetworkContext(state.compact, state.chatDB)
		state.currentSnapshot = parseNetworkSnapshot(newPrompt)
		state.messages = []ollamaMessage{
			{Role: "system", Content: newPrompt},
		}
		fmt.Fprintln(os.Stderr, "done.")
		fmt.Print(diffNetworkSnapshots(oldSnapshot, state.currentSnapshot))
		return false, nil
	}
	if input == "save" || strings.HasPrefix(input, "save ") {
		path := strings.TrimPrefix(input, "save ")
		if path == "save" {
			path = ""
		}
		if err := saveConversation(state.messages, path); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving: %v\n", err)
		}
		return false, nil
	}

	state.messages = append(state.messages, ollamaMessage{Role: "user", Content: input})
	req := ollamaChatRequest{
		Model:    state.model,
		Messages: state.messages,
		Stream:   true,
	}

	fmt.Print("\nassistant> ")
	response, err := streamOllamaChat(context.Background(), state.ollamaURL, req, os.Stdout)
	fmt.Print("\n\n")
	if err != nil {
		return false, err
	}

	state.messages = append(state.messages, ollamaMessage{Role: "assistant", Content: response})
	return false, nil
}

// networkSnapshot holds a snapshot of connected devices for diffing on refresh.
type networkSnapshot struct {
	DeviceMACs map[string]string // MAC -> name
	WAN_IP     string
	DeviceCount int
}

// parseNetworkSnapshot extracts key data from a system prompt for diffing.
func parseNetworkSnapshot(prompt string) networkSnapshot {
	snap := networkSnapshot{DeviceMACs: make(map[string]string)}

	lines := strings.Split(prompt, "\n")
	inDevices := false
	for _, line := range lines {
		if strings.Contains(line, "=== CONNECTED DEVICES") {
			inDevices = true
			// Parse count from header like "=== CONNECTED DEVICES (14) ==="
			if start := strings.Index(line, "("); start >= 0 {
				if end := strings.Index(line[start:], ")"); end >= 0 {
					fmt.Sscanf(line[start+1:start+end], "%d", &snap.DeviceCount)
				}
			}
			continue
		}
		if inDevices && strings.HasPrefix(line, "===") {
			inDevices = false
		}
		if inDevices && line != "" && !strings.HasPrefix(line, "NAME") {
			fields := strings.Fields(line)
			// Find the MAC field (XX-XX-XX-XX-XX-XX pattern)
			for i, f := range fields {
				if len(f) == 17 && strings.Count(f, "-") == 5 {
					// Name is everything before the IP field (which is right before MAC)
					nameEnd := i - 1 // skip the IP field
					if nameEnd < 0 {
						nameEnd = 0
					}
					name := strings.Join(fields[:nameEnd], " ")
					snap.DeviceMACs[f] = name
					break
				}
			}
		}
		if strings.Contains(line, "WAN IP:") {
			parts := strings.Split(line, "|")
			if len(parts) > 0 {
				wan := strings.TrimPrefix(parts[0], "WAN IP: ")
				snap.WAN_IP = strings.TrimSpace(wan)
			}
		}
	}
	return snap
}

// diffNetworkSnapshots compares two snapshots and returns a human-readable diff.
func diffNetworkSnapshots(old, new networkSnapshot) string {
	var sb strings.Builder

	// Device count change
	if old.DeviceCount != new.DeviceCount {
		sb.WriteString(fmt.Sprintf("Devices: %d → %d\n", old.DeviceCount, new.DeviceCount))
	} else if new.DeviceCount > 0 {
		sb.WriteString(fmt.Sprintf("Devices: %d (no change)\n", new.DeviceCount))
	}

	// New devices
	for mac, name := range new.DeviceMACs {
		if _, existed := old.DeviceMACs[mac]; !existed {
			if name == "" {
				name = mac
			}
			sb.WriteString(fmt.Sprintf("  + %s (%s)\n", name, mac))
		}
	}

	// Departed devices
	for mac, name := range old.DeviceMACs {
		if _, exists := new.DeviceMACs[mac]; !exists {
			if name == "" {
				name = mac
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", name, mac))
		}
	}

	// WAN IP change
	if old.WAN_IP != "" && new.WAN_IP != "" && old.WAN_IP != new.WAN_IP {
		sb.WriteString(fmt.Sprintf("WAN IP: %s → %s\n", old.WAN_IP, new.WAN_IP))
	}

	result := sb.String()
	if result == "" {
		return "No changes detected.\n"
	}
	return result
}

// saveConversation writes the conversation history to a markdown file.
func saveConversation(messages []ollamaMessage, path string) error {
	if path == "" {
		path = fmt.Sprintf("deco-chat-%s.md", time.Now().Format("2006-01-02-150405"))
	}

	// Resolve relative to home directory if not absolute
	if !filepath.IsAbs(path) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Deco Chat — %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	for _, m := range messages {
		switch m.Role {
		case "system":
			// Skip system prompt in export
			continue
		case "user":
			sb.WriteString(fmt.Sprintf("**You:** %s\n\n", m.Content))
		case "assistant":
			sb.WriteString(fmt.Sprintf("**Assistant:** %s\n\n", m.Content))
		}
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Conversation saved to %s\n", path)
	return nil
}
