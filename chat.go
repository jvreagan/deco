package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

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
func gatherNetworkContext(compact bool) string {
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
	if db, err := initDB(); err == nil {
		defer db.Close()
		now := time.Now()
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todayStr := startOfDay.Format(time.RFC3339)

		// Top bandwidth today
		if rows, err := db.Query(fmt.Sprintf(`
			SELECT mac, name,
				SUM(download_kbps) as total_download,
				SUM(upload_kbps) as total_upload
			FROM bandwidth_samples
			WHERE timestamp >= ?
			GROUP BY mac
			ORDER BY (total_download + total_upload) DESC
			LIMIT %d
		`, bandwidthLimit), todayStr); err == nil {
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
			if len(entries) > 0 {
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
		}

		// WAN IP history + performance (today)
		if rows, err := db.Query(fmt.Sprintf(`
			SELECT timestamp, wan_ip,
				COALESCE(cpu_percent, 0), COALESCE(mem_percent, 0)
			FROM network_snapshots
			WHERE timestamp >= ?
			ORDER BY timestamp
			LIMIT %d`, netSnapLimit), todayStr); err == nil {
			defer rows.Close()
			type netSnap struct {
				ts, ip string
				cpu, mem float64
			}
			var snaps []netSnap
			for rows.Next() {
				var s netSnap
				if err := rows.Scan(&s.ts, &s.ip, &s.cpu, &s.mem); err == nil {
					snaps = append(snaps, s)
				}
			}
			if len(snaps) > 0 {
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

				// CPU/Memory summary
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
		}

		// Mesh node uptime (today)
		if rows, err := db.Query(fmt.Sprintf(`
			SELECT name, role, mac, status, firmware
			FROM mesh_snapshots
			WHERE timestamp >= ?
			ORDER BY timestamp
			LIMIT %d`, meshSnapLimit), todayStr); err == nil {
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
			if len(nodes) > 0 {
				sb.WriteString("\n=== MESH NODE UPTIME (today) ===\n")
				sb.WriteString(fmt.Sprintf("%-20s %-8s %-18s %-12s %s\n", "NAME", "ROLE", "MAC", "FIRMWARE", "UPTIME"))
				for _, mac := range nodeOrder {
					ns := nodes[mac]
					uptime := float64(ns.online) / float64(ns.total) * 100
					sb.WriteString(fmt.Sprintf("%-20s %-8s %-18s %-12s %.1f%%\n",
						ns.name, ns.role, mac, ns.firmware, uptime))
				}
			}
		}

		// All known MACs ever seen (for "have you seen device X?" type questions)
		if rows, err := db.Query(fmt.Sprintf(`
			SELECT mac, name, MAX(timestamp) as last_seen, COUNT(*) as samples
			FROM bandwidth_samples
			GROUP BY mac
			ORDER BY last_seen DESC
			LIMIT %d`, knownLimit)); err == nil {
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
			if len(devs) > 0 {
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
		}
	}

	return sb.String()
}

// streamOllamaChat sends a chat request to Ollama and streams the response to w.
// Returns the full assembled response text.
func streamOllamaChat(ollamaURL string, req ollamaChatRequest, w io.Writer) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %v", err)
	}

	fmt.Fprint(os.Stderr, "Thinking...")

	resp, err := http.Post(ollamaURL+"/api/chat", "application/json", strings.NewReader(string(body)))
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
			continue
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

// runChat is the main entry point for the chat command.
func runChat(model, ollamaURL, query string, compact bool) error {
	// Resolve OLLAMA_HOST env var as fallback
	if ollamaURL == "http://localhost:11434" {
		if host := os.Getenv("OLLAMA_HOST"); host != "" {
			ollamaURL = host
		}
	}

	if err := checkOllama(ollamaURL); err != nil {
		return err
	}

	fmt.Fprint(os.Stderr, "Gathering network data... ")
	systemPrompt := gatherNetworkContext(compact)
	fmt.Fprintln(os.Stderr, "done.")

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
		_, err := streamOllamaChat(ollamaURL, req, os.Stdout)
		fmt.Println()
		return err
	}

	// REPL mode
	fmt.Println("Network AI Chat (type 'exit' to quit, 'refresh' to reload network data)")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			// EOF (Ctrl+D)
			fmt.Println()
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			break
		}
		if input == "refresh" {
			fmt.Fprint(os.Stderr, "Refreshing network data... ")
			systemPrompt = gatherNetworkContext(compact)
			messages = []ollamaMessage{
				{Role: "system", Content: systemPrompt},
			}
			fmt.Fprintln(os.Stderr, "done.")
			continue
		}

		messages = append(messages, ollamaMessage{Role: "user", Content: input})
		req := ollamaChatRequest{
			Model:    model,
			Messages: messages,
			Stream:   true,
		}

		fmt.Print("\nassistant> ")
		response, err := streamOllamaChat(ollamaURL, req, os.Stdout)
		fmt.Print("\n\n")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			continue
		}

		messages = append(messages, ollamaMessage{Role: "assistant", Content: response})
	}

	return nil
}
