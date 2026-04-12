package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jvreagan/deco/internal/decoclient"
)

// activityEntry is a timestamped log line in the dashboard.
type activityEntry struct {
	Time string
	Text string
}

// dashboardModel is the bubbletea model for the live dashboard.
// The host and password fields are immutable after initialization and safe to
// read from goroutines (e.g., fetchData commands).
// The decoClient field is a shared pointer that is created once and reused
// across ticks. If a request fails, the client is re-authorized automatically.
type dashboardModel struct {
	clients    *ClientList
	network    *NetworkInfo
	mesh       *MeshInfo
	wireless   *WirelessInfo
	activity   []activityEntry
	lastUpdate time.Time
	err        error
	host       string       // immutable after init; read by fetchData goroutines
	password   string       // immutable after init; read by fetchData goroutines
	decoClient *DecoClient  // shared; created once, reused across ticks
	interval   int
	width      int
	height     int
	quitting   bool
	knownMACs  map[string]bool
}

// tickMsg triggers a data fetch.
type tickMsg struct{}

// dataMsg carries fetched data back to the model.
type dataMsg struct {
	clients  *ClientList
	network  *NetworkInfo
	mesh     *MeshInfo
	wireless *WirelessInfo
	err      error
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func fetchData(dc *DecoClient) tea.Cmd {
	return func() tea.Msg {
		// Ensure we have a valid session; re-authorize if needed.
		ctx := context.Background()
		if err := dc.EnsureAuthorized(ctx); err != nil {
			return dataMsg{err: err}
		}

		clients, cErr := dc.GetClients(ctx)
		network, nErr := dc.GetNetwork(ctx)
		mesh, mErr := dc.GetMesh(ctx)
		wireless, wErr := dc.GetWireless(ctx)

		// If all failed, the session likely expired — invalidate so next tick re-auths.
		if cErr != nil && nErr != nil && mErr != nil && wErr != nil {
			dc.Invalidate()
			return dataMsg{err: cErr}
		}

		return dataMsg{
			clients:  clients,
			network:  network,
			mesh:     mesh,
			wireless: wireless,
		}
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return tickCmd(0)
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		return m, tea.Batch(
			fetchData(m.decoClient),
			tickCmd(time.Duration(m.interval)*time.Second),
		)

	case dataMsg:
		m.lastUpdate = time.Now()
		if msg.err != nil {
			m.err = msg.err
			m = addActivity(m, "Error: "+msg.err.Error())
			return m, nil
		}
		m.err = nil

		// Detect new devices
		if msg.clients != nil {
			for _, c := range msg.clients.Clients {
				mac := strings.ToUpper(c.MAC)
				if !m.knownMACs[mac] {
					if len(m.knownMACs) > 0 { // skip first load
						name := c.Name
						m = addActivity(m, fmt.Sprintf("New: %s (%s)", name, mac))
					}
					// Cap knownMACs to prevent unbounded growth in long-running sessions.
					if len(m.knownMACs) >= 1000 {
						// Evict arbitrary entries to make room.
						for k := range m.knownMACs {
							delete(m.knownMACs, k)
							if len(m.knownMACs) < 900 {
								break
							}
						}
					}
					m.knownMACs[mac] = true
				}
			}
		}

		m.clients = msg.clients
		m.network = msg.network
		m.mesh = msg.mesh
		m.wireless = msg.wireless
		m = addActivity(m, "Updated")
	}

	return m, nil
}

func addActivity(m dashboardModel, text string) dashboardModel {
	m.activity = append(m.activity, activityEntry{
		Time: time.Now().Format("15:04:05"),
		Text: text,
	})
	if len(m.activity) > 50 {
		m.activity = m.activity[len(m.activity)-50:]
	}
	return m
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func (m dashboardModel) View() string {
	if m.quitting {
		return ""
	}

	w := m.width
	if w < 40 {
		w = 80
	}

	panelW := w/2 - 2
	if panelW < 40 {
		panelW = 40
	}

	title := titleStyle.Render(" Deco Dashboard ")
	if !m.lastUpdate.IsZero() {
		title += dimStyle.Render("  Updated: " + m.lastUpdate.Format("15:04:05"))
	}
	title += dimStyle.Render("  Press q to quit")

	if m.clients == nil && m.network == nil {
		return title + "\n\nLoading...\n"
	}

	// Top-left: Clients
	clientPanel := m.renderClients(panelW)

	// Top-right: Network
	networkPanel := m.renderNetwork(panelW)

	// Bottom-left: Mesh
	meshPanel := m.renderMesh(panelW)

	// Bottom-right: Activity
	activityPanel := m.renderActivity(panelW)

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, clientPanel, " ", networkPanel)
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top, meshPanel, " ", activityPanel)

	return title + "\n" + topRow + "\n" + bottomRow + "\n"
}

func (m dashboardModel) renderClients(width int) string {
	// loadAliases is called on every render cycle, but uses file-based caching
	// (aliasCache): it only re-reads the file when the mod time changes.
	aliases := loadAliases()
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("CLIENTS"))
	sb.WriteString("\n")

	if m.clients == nil {
		sb.WriteString("No data")
		return panelStyle.Width(width).Render(sb.String())
	}

	sb.WriteString(fmt.Sprintf("%-18s %-14s %-10s %6s %6s\n",
		"NAME", "IP", "CONN", "DOWN", "UP"))

	for _, c := range m.clients.Clients {
		name := c.Name
		if alias, ok := aliases[strings.ToUpper(c.MAC)]; ok {
			name = alias
		}
		if len(name) > 17 {
			name = name[:17]
		}

		conn := connAbbrev(c.Connection)
		down := "-"
		up := "-"
		if c.DownloadKbps > 0 {
			down = fmt.Sprintf("%d", c.DownloadKbps)
		}
		if c.UploadKbps > 0 {
			up = fmt.Sprintf("%d", c.UploadKbps)
		}

		sb.WriteString(fmt.Sprintf("%-18s %-14s %-10s %6s %6s\n",
			name, c.IP, conn, down, up))
	}
	sb.WriteString(fmt.Sprintf("\nTotal: %d", m.clients.Count))

	return panelStyle.Width(width).Render(sb.String())
}

func (m dashboardModel) renderNetwork(width int) string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("NETWORK"))
	sb.WriteString("\n")

	if m.network == nil {
		sb.WriteString("No data")
		return panelStyle.Width(width).Render(sb.String())
	}

	sb.WriteString(fmt.Sprintf("WAN IP:   %s\n", m.network.WAN.IP))
	sb.WriteString(fmt.Sprintf("Gateway:  %s\n", m.network.WAN.Gateway))

	if m.network.Performance.CPUPercent != nil {
		pct := *m.network.Performance.CPUPercent
		label := colorPct(pct, fmt.Sprintf("%.0f%%", pct))
		sb.WriteString(fmt.Sprintf("CPU:      %s\n", label))
	}
	if m.network.Performance.MemPercent != nil {
		pct := *m.network.Performance.MemPercent
		label := colorPct(pct, fmt.Sprintf("%.0f%%", pct))
		sb.WriteString(fmt.Sprintf("Memory:   %s\n", label))
	}

	if m.err != nil {
		sb.WriteString(redStyle.Render("\nError: " + m.err.Error()))
	}

	return panelStyle.Width(width).Render(sb.String())
}

func (m dashboardModel) renderMesh(width int) string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("MESH NODES"))
	sb.WriteString("\n")

	if m.mesh == nil {
		sb.WriteString("No data")
		return panelStyle.Width(width).Render(sb.String())
	}

	sb.WriteString(fmt.Sprintf("%-16s %-8s %-16s %s\n", "NAME", "ROLE", "IP", "STATUS"))
	for _, d := range m.mesh.Devices {
		status := d.Status
		if status == "online" {
			status = greenStyle.Render("online")
		} else {
			status = redStyle.Render(status)
		}
		sb.WriteString(fmt.Sprintf("%-16s %-8s %-16s %s\n", d.Name, d.Role, d.IP, status))
	}

	return panelStyle.Width(width).Render(sb.String())
}

func (m dashboardModel) renderActivity(width int) string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("ACTIVITY LOG"))
	sb.WriteString("\n")

	if len(m.activity) == 0 {
		sb.WriteString("Waiting for data...")
		return panelStyle.Width(width).Render(sb.String())
	}

	// Show last 8 entries
	start := 0
	if len(m.activity) > 8 {
		start = len(m.activity) - 8
	}
	for _, e := range m.activity[start:] {
		sb.WriteString(dimStyle.Render(e.Time) + " " + e.Text + "\n")
	}

	return panelStyle.Width(width).Render(sb.String())
}

func colorPct(pct float64, label string) string {
	if pct >= 80 {
		return redStyle.Render(label)
	}
	if pct >= 60 {
		return yellowStyle.Render(label)
	}
	return greenStyle.Render(label)
}

func runDashboard(interval int) error {
	config, err := decoclient.LoadConfig()
	if err != nil {
		return err
	}
	if err := decoclient.ValidateConfig(config); err != nil {
		return err
	}

	dc := decoclient.NewDecoClient(config.Host, config.Password)

	m := dashboardModel{
		host:       config.Host,
		password:   config.Password,
		decoClient: dc,
		interval:   interval,
		knownMACs:  map[string]bool{},
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
