package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func printJSON(data interface{}) {
	out, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(out))
}

func printClientsTable(data map[string]interface{}) {
	clients := data["clients"].([]map[string]interface{})

	aliases := loadAliases()

	fmt.Printf("\n%-25s %-16s %-18s %-14s %-12s %-8s %-8s\n",
		"NAME", "IP", "MAC", "CONNECTION", "TYPE", "DOWN", "UP")
	fmt.Println(strings.Repeat("-", 110))

	for _, c := range clients {
		name := fmt.Sprintf("%v", c["name"])
		// Apply alias if one exists
		if mac, ok := c["mac"].(string); ok {
			if alias, ok := aliases[strings.ToUpper(mac)]; ok {
				name = alias
			}
		}
		if len(name) > 24 {
			name = name[:24]
		}

		down := "-"
		up := "-"
		if d := toInt(c["download_kbps"]); d > 0 {
			down = fmt.Sprintf("%dKB/s", d)
		}
		if u := toInt(c["upload_kbps"]); u > 0 {
			up = fmt.Sprintf("%dKB/s", u)
		}

		fmt.Printf("%-25s %-16v %-18v %-14v %-12v %-8s %-8s\n",
			name, c["ip"], c["mac"], c["connection"], c["type"], down, up)
	}

	fmt.Printf("\nTotal: %v clients\n", data["count"])
}

func printNetworkTable(data map[string]interface{}) {
	wan := data["wan"].(map[string]interface{})
	lan := data["lan"].(map[string]interface{})
	perf := data["performance"].(map[string]interface{})

	fmt.Println("\n=== WAN ===")
	fmt.Printf("  IP:      %v\n", wan["ip"])
	fmt.Printf("  Gateway: %v\n", wan["gateway"])
	fmt.Printf("  Netmask: %v\n", wan["netmask"])
	fmt.Printf("  MAC:     %v\n", wan["mac"])

	fmt.Println("\n=== LAN ===")
	fmt.Printf("  IP:      %v\n", lan["ip"])
	fmt.Printf("  Netmask: %v\n", lan["netmask"])
	fmt.Printf("  MAC:     %v\n", lan["mac"])

	fmt.Println("\n=== Performance ===")
	fmt.Printf("  CPU:    %v%%\n", perf["cpu_percent"])
	fmt.Printf("  Memory: %v%%\n", perf["mem_percent"])
}

func printWirelessTable(data map[string]interface{}) {
	bands := data["bands"].(map[string]interface{})

	fmt.Print("\n=== Wireless Networks ===\n\n")
	for bandName, b := range bands {
		if b == nil {
			continue
		}
		band := b.(map[string]interface{})
		host := band["host"].(map[string]interface{})
		guest := band["guest"].(map[string]interface{})

		status := "x"
		if enabled, ok := host["enabled"].(bool); ok && enabled {
			status = "o"
		}
		fmt.Printf("[%s] %s: %v\n", status, bandName, host["ssid"])
		fmt.Printf("    Channel: %v | Width: %v\n", host["channel"], host["channel_width"])

		if enabled, ok := guest["enabled"].(bool); ok && enabled {
			fmt.Printf("    Guest: %v\n", guest["ssid"])
		}
		fmt.Println()
	}
}

func printMeshTable(data map[string]interface{}) {
	devices := data["devices"].([]map[string]interface{})

	fmt.Printf("\n=== Mesh Devices (%v) ===\n\n", data["count"])
	for _, d := range devices {
		role := "  "
		if d["role"] == "master" {
			role = "* "
		}
		fmt.Printf("%s%v (%v)\n", role, d["name"], d["model"])
		fmt.Printf("    Role: %v | IP: %v | MAC: %v\n", d["role"], d["ip"], d["mac"])
		fmt.Printf("    Firmware: %v | Status: %v\n", d["firmware"], d["status"])
		fmt.Println()
	}
}

func printReport(report map[string]interface{}) {
	aliases := loadAliases()

	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("BANDWIDTH USAGE REPORT - %v\n", report["period"])
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("From: %v\n", report["start_time"])
	fmt.Printf("To:   %v\n", report["query_time"])
	fmt.Printf("Samples: %v (every %vs)\n", report["total_samples"], report["interval_seconds"])

	devices := report["devices"].([]map[string]interface{})
	if len(devices) == 0 {
		fmt.Println("\nNo data recorded for this period.")
		return
	}

	interval := 5

	fmt.Printf("\n%-24s %-16s %-12s %-12s %-12s %-12s\n",
		"NAME", "IP", "CONNECTION", "DOWNLOAD", "UPLOAD", "TOTAL")
	fmt.Println(strings.Repeat("-", 90))

	var grandDown, grandUp int64

	for _, d := range devices {
		totalDown := d["total_download"].(int64) * int64(interval)
		totalUp := d["total_upload"].(int64) * int64(interval)
		total := totalDown + totalUp

		if total == 0 {
			continue
		}

		grandDown += totalDown
		grandUp += totalUp

		name := fmt.Sprintf("%v", d["name"])
		// Apply alias if one exists
		if mac, ok := d["mac"].(string); ok {
			if alias, ok := aliases[strings.ToUpper(mac)]; ok {
				name = alias
			}
		}
		if len(name) > 23 {
			name = name[:23]
		}
		if name == "" {
			name = "Unknown"
		}

		fmt.Printf("%-24s %-16v %-12v %-12s %-12s %-12s\n",
			name, d["ip"], d["connection"],
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
