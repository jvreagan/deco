package db

import (
	"database/sql"
	"fmt"

	"github.com/jvreagan/deco/internal/decoclient"
)

// StoreBandwidthSamples inserts bandwidth samples for all connected clients into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreBandwidthSamples(database *sql.DB, clients *decoclient.ClientList, ts string) error {
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("bandwidth tx begin: %v", err)
	}
	for _, c := range clients.Clients {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, c.MAC, c.Name, c.IP, c.Connection, c.Type, c.DownloadKbps, c.UploadKbps); err != nil {
			tx.Rollback()
			return fmt.Errorf("bandwidth insert %s: %v", c.MAC, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bandwidth tx commit: %v", err)
	}
	return nil
}

// StoreNetworkSnapshot inserts a network snapshot (WAN, LAN, performance) into the database.
// Wrapped in a transaction for consistency with other store functions.
func StoreNetworkSnapshot(database *sql.DB, network *decoclient.NetworkInfo, ts string) error {
	var dns1, dns2 string
	if len(network.WAN.DNS) >= 1 {
		dns1 = network.WAN.DNS[0]
	}
	if len(network.WAN.DNS) >= 2 {
		dns2 = network.WAN.DNS[1]
	}
	var cpu, mem sql.NullFloat64
	if network.Performance.CPUPercent != nil {
		cpu = sql.NullFloat64{Float64: *network.Performance.CPUPercent, Valid: true}
	}
	if network.Performance.MemPercent != nil {
		mem = sql.NullFloat64{Float64: *network.Performance.MemPercent, Valid: true}
	}
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("network tx begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, network.WAN.IP, network.WAN.Gateway, dns1, dns2, network.LAN.IP, network.LAN.Netmask, cpu, mem); err != nil {
		tx.Rollback()
		return fmt.Errorf("network snapshot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("network tx commit: %v", err)
	}
	return nil
}

// StoreMeshSnapshot inserts mesh node snapshots into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreMeshSnapshot(database *sql.DB, mesh *decoclient.MeshInfo, ts string) error {
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("mesh tx begin: %v", err)
	}
	for _, d := range mesh.Devices {
		if _, err := tx.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, d.Name, d.Role, d.IP, d.MAC, d.Model, d.Firmware, d.Status); err != nil {
			tx.Rollback()
			return fmt.Errorf("mesh insert %s: %v", d.MAC, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh tx commit: %v", err)
	}
	return nil
}

// StoreWirelessSnapshot inserts wireless band snapshots into the database.
// All inserts are wrapped in a single transaction for performance and atomicity.
func StoreWirelessSnapshot(database *sql.DB, wireless *decoclient.WirelessInfo, ts string) error {
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("wireless tx begin: %v", err)
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
			return fmt.Errorf("wireless insert %s: %v", bandName, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("wireless tx commit: %v", err)
	}
	return nil
}
