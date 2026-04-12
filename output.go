package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jvreagan/deco/internal/decolog"
)

func printJSON(data interface{}) {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		decolog.Error("Error marshaling JSON: %v", err)
		return
	}
	fmt.Println(string(out))
}

var macRegexp = regexp.MustCompile(`^([0-9A-Fa-f]{2}[-:]){5}[0-9A-Fa-f]{2}$`)

func validMAC(mac string) bool {
	return macRegexp.MatchString(mac)
}

func printClientsTable(data *ClientList) {
	// loadAliases uses file-based caching (aliasCache); safe to call frequently.
	aliases := loadAliases()

	fmt.Printf("\n%-25s %-16s %-18s %-14s %-12s %-8s %-8s\n",
		"NAME", "IP", "MAC", "CONNECTION", "TYPE", "DOWN", "UP")
	fmt.Println(strings.Repeat("-", 110))

	for _, c := range data.Clients {
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

		fmt.Printf("%-25s %-16s %-18s %-14s %-12s %-8s %-8s\n",
			name, c.IP, c.MAC, c.Connection, c.Type, down, up)
	}

	fmt.Printf("\nTotal: %d clients\n", data.Count)
}

func printNetworkTable(data *NetworkInfo) {
	fmt.Println("\n=== WAN ===")
	fmt.Printf("  IP:      %s\n", data.WAN.IP)
	fmt.Printf("  Gateway: %s\n", data.WAN.Gateway)
	fmt.Printf("  Netmask: %s\n", data.WAN.Netmask)
	fmt.Printf("  MAC:     %s\n", data.WAN.MAC)

	fmt.Println("\n=== LAN ===")
	fmt.Printf("  IP:      %s\n", data.LAN.IP)
	fmt.Printf("  Netmask: %s\n", data.LAN.Netmask)
	fmt.Printf("  MAC:     %s\n", data.LAN.MAC)

	fmt.Println("\n=== Performance ===")
	if data.Performance.CPUPercent != nil {
		fmt.Printf("  CPU:    %.0f%%\n", *data.Performance.CPUPercent)
	} else {
		fmt.Println("  CPU:    N/A")
	}
	if data.Performance.MemPercent != nil {
		fmt.Printf("  Memory: %.0f%%\n", *data.Performance.MemPercent)
	} else {
		fmt.Println("  Memory: N/A")
	}
}

func printWirelessTable(data *WirelessInfo) {
	fmt.Print("\n=== Wireless Networks ===\n\n")
	bandNames := make([]string, 0, len(data.Bands))
	for k := range data.Bands {
		bandNames = append(bandNames, k)
	}
	sort.Strings(bandNames)
	for _, bandName := range bandNames {
		band := data.Bands[bandName]
		status := "x"
		if band.Host.Enabled {
			status = "o"
		}
		fmt.Printf("[%s] %s: %s\n", status, bandName, band.Host.SSID)
		fmt.Printf("    Channel: %s | Width: %s\n", band.Host.Channel, band.Host.ChannelWidth)

		if band.Guest.Enabled {
			fmt.Printf("    Guest: %s\n", band.Guest.SSID)
		}
		fmt.Println()
	}
}

func printMeshTable(data *MeshInfo) {
	fmt.Printf("\n=== Mesh Devices (%d) ===\n\n", data.Count)
	for _, d := range data.Devices {
		role := "  "
		if d.Role == "master" {
			role = "* "
		}
		fmt.Printf("%s%s (%s)\n", role, d.Name, d.Model)
		fmt.Printf("    Role: %s | IP: %s | MAC: %s\n", d.Role, d.IP, d.MAC)
		fmt.Printf("    Firmware: %s | Status: %s\n", d.Firmware, d.Status)
		fmt.Println()
	}
}

func printReport(report *Report) {
	// loadAliases uses file-based caching (aliasCache); safe to call frequently.
	aliases := loadAliases()

	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("BANDWIDTH USAGE REPORT - %s\n", report.Period)
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("From: %s\n", report.StartTime)
	fmt.Printf("To:   %s\n", report.QueryTime)
	fmt.Printf("Samples: %d (every %ds)\n", report.TotalSamples, report.IntervalSeconds)

	if len(report.Devices) == 0 {
		fmt.Println("\nNo data recorded for this period.")
		return
	}

	interval := int64(report.IntervalSeconds)

	fmt.Printf("\n%-22s %-16s %-16s %-12s %-12s %-12s\n",
		"NAME", "IP", "CONNECTIONS", "DOWNLOAD", "UPLOAD", "TOTAL")
	fmt.Println(strings.Repeat("-", 92))

	var grandDown, grandUp int64

	for _, d := range report.Devices {
		totalDown := d.TotalDownload * interval
		totalUp := d.TotalUpload * interval
		total := totalDown + totalUp

		if total == 0 {
			continue
		}

		grandDown += totalDown
		grandUp += totalUp

		name := d.Name
		if alias, ok := aliases[strings.ToUpper(d.MAC)]; ok {
			name = alias
		}
		if len(name) > 21 {
			name = name[:21]
		}
		if name == "" {
			name = "Unknown"
		}

		connCol := d.Connection
		if len(d.ConnectionBreakdown) > 1 {
			connCol = formatConnBreakdown(d.ConnectionBreakdown)
		} else if len(d.ConnectionBreakdown) == 1 {
			for k := range d.ConnectionBreakdown {
				connCol = connAbbrev(k) + ":100%"
			}
		}

		fmt.Printf("%-22s %-16s %-16s %-12s %-12s %-12s\n",
			name, d.IP, connCol,
			formatBytes(float64(totalDown)), formatBytes(float64(totalUp)), formatBytes(float64(total)))
	}

	fmt.Println(strings.Repeat("-", 92))
	fmt.Printf("%-22s %-16s %-16s %-12s %-12s %-12s\n",
		"TOTAL", "", "",
		formatBytes(float64(grandDown)), formatBytes(float64(grandUp)), formatBytes(float64(grandDown+grandUp)))
}

func printReportCSV(report *Report) {
	// loadAliases uses file-based caching (aliasCache); safe to call frequently.
	aliases := loadAliases()
	interval := int64(report.IntervalSeconds)

	fmt.Println("mac,name,ip,connection,samples,download_kb,upload_kb,total_kb")
	for _, d := range report.Devices {
		totalDown := d.TotalDownload * interval
		totalUp := d.TotalUpload * interval
		total := totalDown + totalUp
		if total == 0 {
			continue
		}

		name := d.Name
		if alias, ok := aliases[strings.ToUpper(d.MAC)]; ok {
			name = alias
		}
		// Escape for CSV
		if strings.ContainsAny(name, ",\"") {
			name = "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
		}

		fmt.Printf("%s,%s,%s,%s,%d,%d,%d,%d\n",
			d.MAC, name, d.IP, d.Connection, d.SampleCount, totalDown, totalUp, total)
	}
}

func printNetworkReport(entries []NetworkReportEntry, period string) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("NETWORK REPORT - %s\n", period)
	fmt.Println(strings.Repeat("=", 70))

	if len(entries) == 0 {
		fmt.Println("\nNo network snapshots recorded for this period.")
		return
	}

	// WAN IP changes
	fmt.Println("\n--- WAN IP History ---")
	currentIP := ""
	firstSeen := ""
	for _, e := range entries {
		if e.WANIP != currentIP {
			if currentIP != "" {
				fmt.Printf("  %s  %s -> %s\n", currentIP, firstSeen, e.Timestamp)
			}
			currentIP = e.WANIP
			firstSeen = e.Timestamp
		}
	}
	if currentIP != "" {
		fmt.Printf("  %s  %s -> now\n", currentIP, firstSeen)
	}

	// CPU/Memory summary
	var sumCPU, sumMem, maxCPU, maxMem float64
	for _, e := range entries {
		sumCPU += e.CPU
		sumMem += e.Memory
		if e.CPU > maxCPU {
			maxCPU = e.CPU
		}
		if e.Memory > maxMem {
			maxMem = e.Memory
		}
	}
	n := float64(len(entries))
	fmt.Printf("\n--- Performance (%d snapshots) ---\n", len(entries))
	fmt.Printf("  CPU:    avg %.1f%%, max %.1f%%\n", sumCPU/n, maxCPU)
	fmt.Printf("  Memory: avg %.1f%%, max %.1f%%\n", sumMem/n, maxMem)
}

func printMeshReport(entries []MeshReportEntry, period string) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("MESH REPORT - %s\n", period)
	fmt.Println(strings.Repeat("=", 70))

	if len(entries) == 0 {
		fmt.Println("\nNo mesh snapshots recorded for this period.")
		return
	}

	// Group by MAC to compute uptime per node
	type nodeStats struct {
		name     string
		role     string
		ip       string
		firmware string
		total    int
		online   int
	}
	nodes := map[string]*nodeStats{}
	var nodeOrder []string
	for _, e := range entries {
		mac := strings.ToUpper(e.MAC)
		ns, ok := nodes[mac]
		if !ok {
			ns = &nodeStats{name: e.Name, role: e.Role, ip: e.IP, firmware: e.Firmware}
			nodes[mac] = ns
			nodeOrder = append(nodeOrder, mac)
		}
		ns.total++
		if e.Status == "online" {
			ns.online++
		}
		// Track firmware changes
		if e.Firmware != ns.firmware {
			ns.firmware = e.Firmware
		}
	}

	fmt.Printf("\n%-20s %-8s %-16s %-18s %-10s %-10s\n",
		"NAME", "ROLE", "IP", "MAC", "FIRMWARE", "UPTIME")
	fmt.Println(strings.Repeat("-", 90))

	for _, mac := range nodeOrder {
		ns := nodes[mac]
		uptime := float64(ns.online) / float64(ns.total) * 100
		fmt.Printf("%-20s %-8s %-16s %-18s %-10s %.1f%%\n",
			ns.name, ns.role, ns.ip, mac, ns.firmware, uptime)
	}

	fmt.Printf("\nSnapshots: %d\n", len(entries))
}

func printDeviceReport(report *DeviceReport) {
	name := report.Name
	if report.Alias != "" {
		name = report.Alias
	}

	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("DEVICE REPORT - %s\n", name)
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("MAC:    %s\n", report.MAC)
	if report.Alias != "" {
		fmt.Printf("Alias:  %s\n", report.Alias)
	}
	fmt.Printf("Period: %s\n", report.Period)
	fmt.Printf("First:  %s\n", report.FirstSeen)
	fmt.Printf("Last:   %s\n", report.LastSeen)
	fmt.Printf("Samples: %d (every %ds)\n", report.TotalSamples, report.IntervalSeconds)

	fmt.Printf("\n--- Bandwidth Summary ---\n")
	fmt.Printf("  Download: %s (peak %d KB/s)\n", formatBytes(report.TotalDownloadKB), report.MaxDownloadKBps)
	fmt.Printf("  Upload:   %s (peak %d KB/s)\n", formatBytes(report.TotalUploadKB), report.MaxUploadKBps)
	fmt.Printf("  Total:    %s\n", formatBytes(report.TotalDownloadKB+report.TotalUploadKB))

	if len(report.Connections) > 0 {
		fmt.Printf("\n--- Connection Types ---\n")
		for _, c := range report.Connections {
			fmt.Printf("  %-20s %d samples (%.1f%%)\n", c.Connection, c.Samples, c.Percent)
		}
	}

	if len(report.IPHistory) > 0 {
		fmt.Printf("\n--- IP History ---\n")
		fmt.Printf("  %-16s %-24s %-24s %s\n", "IP", "FIRST SEEN", "LAST SEEN", "SAMPLES")
		fmt.Printf("  %s\n", strings.Repeat("-", 75))
		for _, ip := range report.IPHistory {
			fmt.Printf("  %-16s %-24s %-24s %d\n", ip.IP, ip.FirstSeen, ip.LastSeen, ip.Samples)
		}
	}

	if len(report.Timeline) > 0 {
		fmt.Printf("\n--- Hourly Bandwidth ---\n")
		fmt.Printf("  %-22s %-12s %-12s %s\n", "HOUR", "DOWNLOAD", "UPLOAD", "SAMPLES")
		fmt.Printf("  %s\n", strings.Repeat("-", 55))
		for _, b := range report.Timeline {
			fmt.Printf("  %-22s %-12s %-12s %d\n",
				b.Timestamp, formatBytes(b.DownloadKB), formatBytes(b.UploadKB), b.SampleCount)
		}
	}
}

func printDeviceReportCSV(report *DeviceReport) {
	fmt.Println("hour,download_kb,upload_kb,samples")
	for _, b := range report.Timeline {
		fmt.Printf("%s,%.0f,%.0f,%d\n", b.Timestamp, b.DownloadKB, b.UploadKB, b.SampleCount)
	}
}

// connAbbrev returns a short label for a connection type.
func connAbbrev(conn string) string {
	switch conn {
	case "WiFi 2.4GHz":
		return "2.4"
	case "WiFi 5GHz":
		return "5"
	case "WiFi 6GHz":
		return "6"
	case "Wired":
		return "W"
	default:
		return conn
	}
}

// formatConnBreakdown formats a connection breakdown map as e.g. "5:80% W:20%".
func formatConnBreakdown(breakdown map[string]int64) string {
	if len(breakdown) == 0 {
		return ""
	}

	var total int64
	for _, v := range breakdown {
		total += v
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(breakdown))
	for k := range breakdown {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		pct := breakdown[k] * 100 / total
		parts = append(parts, fmt.Sprintf("%s:%d%%", connAbbrev(k), pct))
	}
	return strings.Join(parts, " ")
}

// ==================== HELPERS ====================

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}

func formatBytes(kb float64) string {
	if kb < 1024 {
		return fmt.Sprintf("%.1f KB", kb)
	} else if kb < 1024*1024 {
		return fmt.Sprintf("%.2f MB", kb/1024)
	}
	return fmt.Sprintf("%.2f GB", kb/(1024*1024))
}
