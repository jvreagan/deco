package main

// ClientInfo represents a connected device
type ClientInfo struct {
	Name         string `json:"name"`
	IP           string `json:"ip"`
	MAC          string `json:"mac"`
	Connection   string `json:"connection"`
	Type         string `json:"type"`
	DownloadKbps int    `json:"download_kbps"`
	UploadKbps   int    `json:"upload_kbps"`
}

// ClientList represents the result of GetClients
type ClientList struct {
	Clients []ClientInfo `json:"clients"`
	Count   int          `json:"count"`
}

// NetworkInfo represents network configuration
type NetworkInfo struct {
	WAN         WANInfo         `json:"wan"`
	LAN         LANInfo         `json:"lan"`
	Performance PerformanceInfo `json:"performance"`
}

// WANInfo represents WAN configuration
type WANInfo struct {
	IP      string   `json:"ip"`
	Gateway string   `json:"gateway"`
	Netmask string   `json:"netmask"`
	MAC     string   `json:"mac"`
	DNS     []string `json:"dns"`
}

// LANInfo represents LAN configuration
type LANInfo struct {
	IP      string `json:"ip"`
	Netmask string `json:"netmask"`
	MAC     string `json:"mac"`
}

// PerformanceInfo represents router performance metrics
type PerformanceInfo struct {
	CPUPercent *float64 `json:"cpu_percent"`
	MemPercent *float64 `json:"mem_percent"`
}

// WirelessInfo represents wireless configuration
type WirelessInfo struct {
	Bands map[string]BandInfo `json:"bands"`
}

// BandInfo represents a single wireless band
type BandInfo struct {
	Band  string    `json:"band"`
	Host  HostInfo  `json:"host"`
	Guest GuestInfo `json:"guest"`
}

// HostInfo represents host wireless settings
type HostInfo struct {
	Enabled      bool   `json:"enabled"`
	SSID         string `json:"ssid"`
	Channel      string `json:"channel"`
	ChannelWidth string `json:"channel_width"`
}

// GuestInfo represents guest wireless settings
type GuestInfo struct {
	Enabled bool   `json:"enabled"`
	SSID    string `json:"ssid"`
}

// MeshInfo represents mesh topology
type MeshInfo struct {
	Devices []MeshDevice `json:"devices"`
	Count   int          `json:"count"`
}

// MeshDevice represents a single mesh node
type MeshDevice struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Firmware string `json:"firmware"`
	Status   string `json:"status"`
}

// ReportDevice represents a device in a bandwidth report
type ReportDevice struct {
	MAC                 string           `json:"mac"`
	Name                string           `json:"name"`
	IP                  string           `json:"ip"`
	Connection          string           `json:"connection"`
	DeviceType          string           `json:"device_type"`
	SampleCount         int64            `json:"sample_count"`
	TotalDownload       int64            `json:"total_download"`
	TotalUpload         int64            `json:"total_upload"`
	MaxDownload         int64            `json:"max_download"`
	MaxUpload           int64            `json:"max_upload"`
	DownloadKB          int64            `json:"download_kb,omitempty"`
	UploadKB            int64            `json:"upload_kb,omitempty"`
	TotalKB             int64            `json:"total_kb,omitempty"`
	ConnectionBreakdown map[string]int64 `json:"connection_breakdown,omitempty"`
}

// Report represents a bandwidth usage report
type Report struct {
	Period          string         `json:"period"`
	StartTime       string         `json:"start_time"`
	QueryTime       string         `json:"query_time"`
	IntervalSeconds int            `json:"interval_seconds"`
	TotalSamples    int64          `json:"total_samples"`
	Devices         []ReportDevice `json:"devices"`
}

// NetworkReportEntry represents a row in the network report.
type NetworkReportEntry struct {
	Timestamp string  `json:"timestamp"`
	WANIP     string  `json:"wan_ip"`
	Gateway   string  `json:"gateway"`
	DNS1      string  `json:"dns1"`
	DNS2      string  `json:"dns2"`
	CPU       float64 `json:"cpu_percent"`
	Memory    float64 `json:"mem_percent"`
}

// MeshReportEntry represents a row in the mesh report.
type MeshReportEntry struct {
	Timestamp string `json:"timestamp"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Status    string `json:"status"`
	Firmware  string `json:"firmware"`
}

// WebhookPayload is the JSON body sent to webhook URLs.
type WebhookPayload struct {
	Event     string      `json:"event"`
	Timestamp string      `json:"timestamp"`
	Text      string      `json:"text"`
	Data      any         `json:"data"`
}

// NewDeviceEvent is the data payload for a "new_device" webhook event.
type NewDeviceEvent struct {
	MAC  string `json:"mac"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// BandwidthAlertEvent is the data payload for a "bandwidth_alert" webhook event.
type BandwidthAlertEvent struct {
	MAC       string `json:"mac"`
	Name      string `json:"name"`
	RateKBps  int    `json:"rate_kbps"`
	Threshold int    `json:"threshold_kbps"`
}

// DeviceTimelineBucket represents one hour of bandwidth data for a device.
type DeviceTimelineBucket struct {
	Timestamp  string  `json:"timestamp"`
	DownloadKB float64 `json:"download_kb"`
	UploadKB   float64 `json:"upload_kb"`
	SampleCount int64  `json:"sample_count"`
}

// DeviceConnectionBreakdown represents connection type usage for a device.
type DeviceConnectionBreakdown struct {
	Connection string  `json:"connection"`
	Samples    int64   `json:"samples"`
	Percent    float64 `json:"percent"`
}

// DeviceIPHistory represents an IP address used by a device over time.
type DeviceIPHistory struct {
	IP        string `json:"ip"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Samples   int64  `json:"samples"`
}

// DeviceReport is a detailed drill-down report for a single device.
type DeviceReport struct {
	MAC             string                      `json:"mac"`
	Name            string                      `json:"name"`
	Alias           string                      `json:"alias,omitempty"`
	Period          string                      `json:"period"`
	StartTime       string                      `json:"start_time"`
	QueryTime       string                      `json:"query_time"`
	FirstSeen       string                      `json:"first_seen"`
	LastSeen        string                      `json:"last_seen"`
	IntervalSeconds int                         `json:"interval_seconds"`
	TotalSamples    int64                       `json:"total_samples"`
	TotalDownloadKB float64                     `json:"total_download_kb"`
	TotalUploadKB   float64                     `json:"total_upload_kb"`
	MaxDownloadKBps int64                       `json:"max_download_kbps"`
	MaxUploadKBps   int64                       `json:"max_upload_kbps"`
	Timeline        []DeviceTimelineBucket      `json:"timeline"`
	Connections     []DeviceConnectionBreakdown `json:"connections"`
	IPHistory       []DeviceIPHistory           `json:"ip_history"`
}

// AllInfo represents a complete network snapshot
type AllInfo struct {
	Timestamp string       `json:"timestamp"`
	Router    string       `json:"router"`
	Network   *NetworkInfo `json:"network"`
	Wireless  *WirelessInfo `json:"wireless"`
	Mesh      *MeshInfo    `json:"mesh"`
	Clients   *ClientList  `json:"clients"`
}
