package db

import (
	"database/sql"
	"time"

	"github.com/jvreagan/deco/internal/decoclient"
	"github.com/jvreagan/deco/internal/decolog"
)

// StoreBandwidthSamples inserts bandwidth samples for all connected clients into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreBandwidthSamples(database *sql.DB, clients *decoclient.ClientList, ts string) {
	logTS := time.Now().Format("15:04:05")
	tx, err := database.Begin()
	if err != nil {
		decolog.Warn("[%s] DB error (bandwidth tx begin): %v", logTS, err)
		return
	}
	for _, c := range clients.Clients {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps); err != nil {
			tx.Rollback()
			decolog.Warn("[%s] DB error (bandwidth insert %s): %v", logTS, c.MAC, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		decolog.Warn("[%s] DB error (bandwidth tx commit): %v", logTS, err)
	}
}

// StoreNetworkSnapshot inserts a network snapshot (WAN, LAN, performance) into the database.
// Wrapped in a transaction for consistency with other store functions.
func StoreNetworkSnapshot(database *sql.DB, network *decoclient.NetworkInfo, ts string) {
	logTS := time.Now().Format("15:04:05")
	var dns1, dns2 string
	if len(network.WAN.DNS) >= 2 {
		dns1 = network.WAN.DNS[0]
		dns2 = network.WAN.DNS[1]
	}
	tx, err := database.Begin()
	if err != nil {
		decolog.Warn("[%s] DB error (network tx begin): %v", logTS, err)
		return
	}
	if _, err := tx.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, network.WAN.IP, network.WAN.Gateway, dns1, dns2, network.LAN.IP, network.LAN.Netmask, network.Performance.CPUPercent, network.Performance.MemPercent); err != nil {
		tx.Rollback()
		decolog.Warn("[%s] DB error (network): %v", logTS, err)
		return
	}
	if err := tx.Commit(); err != nil {
		decolog.Warn("[%s] DB error (network tx commit): %v", logTS, err)
	}
}

// StoreMeshSnapshot inserts mesh node snapshots into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreMeshSnapshot(database *sql.DB, mesh *decoclient.MeshInfo, ts string) {
	logTS := time.Now().Format("15:04:05")
	tx, err := database.Begin()
	if err != nil {
		decolog.Warn("[%s] DB error (mesh tx begin): %v", logTS, err)
		return
	}
	for _, d := range mesh.Devices {
		if _, err := tx.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, d.Name, d.Role, d.IP, d.MAC, d.Model, d.Firmware, d.Status); err != nil {
			tx.Rollback()
			decolog.Warn("[%s] DB error (mesh insert %s): %v", logTS, d.MAC, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		decolog.Warn("[%s] DB error (mesh tx commit): %v", logTS, err)
	}
}

// StoreWirelessSnapshot inserts wireless band snapshots into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreWirelessSnapshot(database *sql.DB, wireless *decoclient.WirelessInfo, ts string) {
	logTS := time.Now().Format("15:04:05")
	tx, err := database.Begin()
	if err != nil {
		decolog.Warn("[%s] DB error (wireless tx begin): %v", logTS, err)
		return
	}
	for bandName, band := range wireless.Bands {
		hostEnabled := 0
		if band.Host.Enabled {
			hostEnabled = 1
		}
		guestEnabled := 0
		if band.Guest.Enabled {
			guestEnabled = 1
		}
		if _, err := tx.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, bandName, band.Host.SSID, band.Host.Channel, band.Host.ChannelWidth, hostEnabled, guestEnabled, band.Guest.SSID); err != nil {
			tx.Rollback()
			decolog.Warn("[%s] DB error (wireless insert %s): %v", logTS, bandName, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		decolog.Warn("[%s] DB error (wireless tx commit): %v", logTS, err)
	}
}
