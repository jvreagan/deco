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
	MAC           string `json:"mac"`
	Name          string `json:"name"`
	IP            string `json:"ip"`
	Connection    string `json:"connection"`
	DeviceType    string `json:"device_type"`
	SampleCount   int64  `json:"sample_count"`
	TotalDownload int64  `json:"total_download"`
	TotalUpload   int64  `json:"total_upload"`
	MaxDownload   int64  `json:"max_download"`
	MaxUpload     int64  `json:"max_upload"`
	DownloadKB    int64  `json:"download_kb,omitempty"`
	UploadKB      int64  `json:"upload_kb,omitempty"`
	TotalKB       int64  `json:"total_kb,omitempty"`
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

// AllInfo represents a complete network snapshot
type AllInfo struct {
	Timestamp string       `json:"timestamp"`
	Router    string       `json:"router"`
	Network   *NetworkInfo `json:"network"`
	Wireless  *WirelessInfo `json:"wireless"`
	Mesh      *MeshInfo    `json:"mesh"`
	Clients   *ClientList  `json:"clients"`
}
