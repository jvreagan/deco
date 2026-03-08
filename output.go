package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

func printJSON(data interface{}) {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		return
	}
	fmt.Println(string(out))
}

var macRegexp = regexp.MustCompile(`^([0-9A-Fa-f]{2}[-:]){5}[0-9A-Fa-f]{2}$`)

func validMAC(mac string) bool {
	return macRegexp.MatchString(mac)
}

func printClientsTable(data *ClientList) {
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

	fmt.Printf("\n%-24s %-16s %-12s %-12s %-12s %-12s\n",
		"NAME", "IP", "CONNECTION", "DOWNLOAD", "UPLOAD", "TOTAL")
	fmt.Println(strings.Repeat("-", 90))

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
		if len(name) > 23 {
			name = name[:23]
		}
		if name == "" {
			name = "Unknown"
		}

		fmt.Printf("%-24s %-16s %-12s %-12s %-12s %-12s\n",
			name, d.IP, d.Connection,
			formatBytes(float64(totalDown)), formatBytes(float64(totalUp)), formatBytes(float64(total)))
	}

	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-24s %-16s %-12s %-12s %-12s %-12s\n",
		"TOTAL", "", "",
		formatBytes(float64(grandDown)), formatBytes(float64(grandUp)), formatBytes(float64(grandDown+grandUp)))
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

func getMap(data map[string]interface{}, key string) map[string]interface{} {
	if v, ok := data[key].(map[string]interface{}); ok {
		return v
	}
	return map[string]interface{}{}
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return 0
	}
}

func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}

func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
