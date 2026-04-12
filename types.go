package main

import "github.com/jvreagan/deco/internal/decoclient"

// Type aliases for API response types that moved to decoclient.
type ClientInfo = decoclient.ClientInfo
type ClientList = decoclient.ClientList
type NetworkInfo = decoclient.NetworkInfo
type WANInfo = decoclient.WANInfo
type LANInfo = decoclient.LANInfo
type PerformanceInfo = decoclient.PerformanceInfo
type WirelessInfo = decoclient.WirelessInfo
type BandInfo = decoclient.BandInfo
type HostInfo = decoclient.HostInfo
type GuestInfo = decoclient.GuestInfo
type MeshInfo = decoclient.MeshInfo
type MeshDevice = decoclient.MeshDevice
type AllInfo = decoclient.AllInfo

// Config and DecoClient aliases
type Config = decoclient.Config
type DecoClient = decoclient.DecoClient

// Report/webhook types stay in package main

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
