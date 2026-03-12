package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDashboardModelInit(t *testing.T) {
	m := dashboardModel{
		host:      "192.168.68.1",
		password:  "test",
		interval:  10,
		knownMACs: map[string]bool{},
	}

	cmd := m.Init()
	if cmd == nil {
		t.Error("Init should return a command")
	}
}

func TestDashboardModelUpdateQuit(t *testing.T) {
	m := dashboardModel{
		knownMACs: map[string]bool{},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	model := updated.(dashboardModel)
	if !model.quitting {
		t.Error("pressing 'q' should set quitting=true")
	}
	if cmd == nil {
		t.Error("quit should return a command")
	}
}

func TestDashboardModelUpdateWindowSize(t *testing.T) {
	m := dashboardModel{
		knownMACs: map[string]bool{},
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model := updated.(dashboardModel)
	if model.width != 120 {
		t.Errorf("width = %d, want 120", model.width)
	}
	if model.height != 40 {
		t.Errorf("height = %d, want 40", model.height)
	}
}

func TestDashboardModelUpdateDataMsg(t *testing.T) {
	m := dashboardModel{
		knownMACs: map[string]bool{
			"AA-BB-CC-DD-EE-FF": true,
		},
	}

	cpu := float64(25)
	mem := float64(50)
	msg := dataMsg{
		clients: &ClientList{
			Clients: []ClientInfo{
				{Name: "OldDevice", MAC: "AA-BB-CC-DD-EE-FF", IP: "192.168.68.100"},
				{Name: "NewDevice", MAC: "11-22-33-44-55-66", IP: "192.168.68.101"},
			},
			Count: 2,
		},
		network: &NetworkInfo{
			WAN:         WANInfo{IP: "1.2.3.4", Gateway: "1.2.3.1"},
			Performance: PerformanceInfo{CPUPercent: &cpu, MemPercent: &mem},
		},
		mesh: &MeshInfo{
			Devices: []MeshDevice{
				{Name: "Main", Role: "master", IP: "192.168.68.1", Status: "online"},
			},
			Count: 1,
		},
	}

	updated, _ := m.Update(msg)
	model := updated.(dashboardModel)

	if model.clients == nil {
		t.Fatal("clients should be set after dataMsg")
	}
	if model.clients.Count != 2 {
		t.Errorf("clients count = %d, want 2", model.clients.Count)
	}
	if model.network == nil {
		t.Fatal("network should be set")
	}
	if model.network.WAN.IP != "1.2.3.4" {
		t.Errorf("WAN IP = %q, want 1.2.3.4", model.network.WAN.IP)
	}

	// New device should be in activity
	foundNew := false
	for _, a := range model.activity {
		if strings.Contains(a.Text, "New:") && strings.Contains(a.Text, "11-22-33-44-55-66") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("new device should be in activity log")
	}
}

func TestDashboardViewEmpty(t *testing.T) {
	m := dashboardModel{
		knownMACs: map[string]bool{},
		width:     80,
		height:    24,
	}

	view := m.View()
	if !strings.Contains(view, "Loading...") {
		t.Error("empty view should contain 'Loading...'")
	}
	if !strings.Contains(view, "Dashboard") {
		t.Error("view should contain title")
	}
}

func TestDashboardViewWithData(t *testing.T) {
	cpu := float64(25)
	mem := float64(50)
	m := dashboardModel{
		knownMACs: map[string]bool{},
		width:     120,
		height:    40,
		clients: &ClientList{
			Clients: []ClientInfo{
				{Name: "TestDevice", MAC: "AA-BB-CC-DD-EE-FF", IP: "192.168.68.100", Connection: "WiFi 5GHz"},
			},
			Count: 1,
		},
		network: &NetworkInfo{
			WAN:         WANInfo{IP: "1.2.3.4", Gateway: "1.2.3.1"},
			Performance: PerformanceInfo{CPUPercent: &cpu, MemPercent: &mem},
		},
		mesh: &MeshInfo{
			Devices: []MeshDevice{
				{Name: "Main", Role: "master", IP: "192.168.68.1", Status: "online"},
			},
		},
	}

	view := m.View()
	if !strings.Contains(view, "TestDevice") {
		t.Error("view should contain device name")
	}
	if !strings.Contains(view, "1.2.3.4") {
		t.Error("view should contain WAN IP")
	}
	if !strings.Contains(view, "Main") {
		t.Error("view should contain mesh node name")
	}
}

func TestDashboardAddActivityCap(t *testing.T) {
	m := dashboardModel{
		knownMACs: map[string]bool{},
	}

	for i := 0; i < 60; i++ {
		m.addActivity("event")
	}

	if len(m.activity) != 50 {
		t.Errorf("activity log should be capped at 50, got %d", len(m.activity))
	}
}

func TestDashboardCmdExists(t *testing.T) {
	cmd := dashboardCmd()
	if cmd.Use != "dashboard" {
		t.Errorf("expected 'dashboard' command, got %q", cmd.Use)
	}

	interval, err := cmd.Flags().GetInt("interval")
	if err != nil {
		t.Fatalf("interval flag error: %v", err)
	}
	if interval != 10 {
		t.Errorf("expected default interval=10, got %d", interval)
	}
}
