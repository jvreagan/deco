# Deco

CLI tool for TP-Link Deco mesh routers. Communicates with the router's encrypted API to show connected clients, network configuration, mesh topology, WiFi settings, and real-time bandwidth — with optional SQLite logging for historical analysis.

Tested on the **Deco BE63** but may work with other Deco models that use the same API.

## Installation

### From source

```bash
go install github.com/jvreagan/deco@latest
```

### Prebuilt binaries

Download from [Releases](https://github.com/jvreagan/deco/releases) — available for Linux, macOS, and Windows (amd64/arm64).

### Build from source

```bash
git clone https://github.com/jvreagan/deco.git
cd deco
go build -o deco
```

## Quick Start

```bash
deco setup
deco clients
```

## Commands

| Command | Description |
|---|---|
| `setup` | Interactive configuration wizard (password is masked) |
| `clients` | List connected devices (name, IP, MAC, connection type, signal) |
| `network` | Show WAN/LAN configuration (IPs, DNS, gateway, CPU/memory usage) |
| `wireless` | Show WiFi configuration (SSIDs, channels, bands, guest network) |
| `mesh` | Show mesh node topology (role, firmware, status) |
| `all` | Complete network snapshot as JSON |
| `poll` | Live bandwidth monitoring per device (logs bandwidth samples to SQLite) |
| `monitor` | Full network monitoring — logs all data to SQLite (bandwidth, network, mesh, wireless) |
| `dashboard` | Live TUI dashboard (clients, network, mesh, activity log) |
| `report [period]` | Show bandwidth usage report (`today`, `hour`, `1h`, `6h`, `12h`, `24h`, `7d`, `30d`, `all`) |
| `report device <MAC\|name>` | Detailed history for a single device |
| `report network [period]` | WAN IP history and CPU/memory trends |
| `report mesh [period]` | Mesh node uptime history |
| `status` | Show database statistics (size, record counts, date range) |
| `purge` | Delete database records (all, or by `--days`/`--before`) |
| `chat [question]` | Ask questions about your network using a local Ollama LLM |
| `api <endpoint> [body]` | Call a raw API endpoint |
| `version` | Show version |
| `reboot` | Reboot the router (confirms first, use `--force` to skip) |
| `block <MAC>` | Block a device by MAC address |
| `unblock <MAC>` | Unblock a device by MAC address |
| `alias` | Manage device aliases |
| `completion <shell>` | Generate shell completion script (`bash`, `zsh`, `fish`, `powershell`) |

### Options

| Flag | Description |
|---|---|
| `--verbose`, `-v` | Show debug output on stderr (global, works with any command) |
| `--json`, `-j` | Output as JSON (works with `clients`, `network`, `wireless`, `mesh`, `status`, `report`, `report device`, `report network`, `report mesh`) |
| `--csv` | Output as CSV (use with `report`, `report device`) |
| `--interval N`, `-i N` | Polling interval in seconds (default: 5 for `poll`/`clients --watch`, 10 for `dashboard`, 60 for `monitor`) |
| `--force`, `-f` | Skip confirmation prompt for `purge` and `reboot` |
| `--name`, `-n <name>` | Filter by device name — substring, case-insensitive (`clients`, `report`) |
| `--mac`, `-m <MAC>` | Filter by MAC address — exact match, case-insensitive (`clients`, `report`) |
| `--watch`, `-w` | Auto-refresh client list (use with `clients`) |
| `--notify` | Alert on new MAC addresses (desktop notification on macOS, stdout on other platforms; use with `monitor`) |
| `--alert N` | Alert when any device exceeds N KB/s (use with `monitor`) |
| `--webhook <url>` | POST JSON notifications to a webhook URL (use with `monitor`) |
| `--days N` | Purge records older than N days (use with `purge`) |
| `--before YYYY-MM-DD` | Purge records before date (use with `purge`) |
| `--model <name>` | Ollama model to use (default: `llama3.2`, use with `chat`) |
| `--ollama-url <url>` | Ollama API base URL (use with `chat`) |
| `--compact` | Use smaller context window for chat (use with `chat`) |
| `--list-models` | List available Ollama models and exit (use with `chat`) |
| `--show-context` | Print system prompt to stderr and exit (use with `chat`) |
| `--max-failures N` | Exit after N consecutive API errors (default: 10; use with `poll`, `monitor`, `clients --watch`) |
| `--group`, `-g <tag>` | Filter by device tag (use with `report`) |

### Device Aliases

Set friendly names for devices that override the router-reported name:

```bash
deco alias                              # List all aliases
deco alias AA-BB-CC-DD-EE-FF "TV"       # Set an alias
deco alias --remove AA-BB-CC-DD-EE-FF   # Remove an alias (or -r)
```

Aliases are stored in `~/.config/deco/deco_aliases.json` and are applied in `clients`, `report`, and `chat` output.

### Device Tags

Group devices with tags for filtered reporting:

```bash
deco alias tag AA-BB-CC-DD-EE-FF gaming    # Tag a device
deco alias tag AA-BB-CC-DD-EE-FF kids      # Multiple tags per device
deco alias untag AA-BB-CC-DD-EE-FF gaming  # Remove a tag
deco alias tags                             # List all tags and devices
deco report --group gaming                  # Report filtered by tag
```

Tags are stored in `~/.config/deco/deco_tags.json`.

### Dashboard

The `dashboard` command launches a live TUI showing clients, network status, mesh nodes, and an activity log in a single view:

```bash
deco dashboard               # default 10s refresh
deco dashboard --interval 5  # refresh every 5s
```

Press `q` or `Ctrl+C` to quit. CPU/memory usage is color-coded (green < 60%, yellow 60-80%, red >= 80%).

### Webhooks

The `monitor` command can POST JSON notifications to a webhook URL when events occur:

```bash
deco monitor --notify --webhook https://example.com/hook   # new device alerts
deco monitor --alert 5000 --webhook https://example.com/hook  # bandwidth alerts
```

Webhook payloads:

```json
{
  "event": "new_device",
  "timestamp": "2025-01-15T10:30:00Z",
  "text": "New device: Xbox (AA-BB-CC-DD-EE-FF) at 192.168.68.71",
  "data": { "mac": "AA-BB-CC-DD-EE-FF", "name": "Xbox", "ip": "192.168.68.71" }
}
```

```json
{
  "event": "bandwidth_alert",
  "timestamp": "2025-01-15T10:30:00Z",
  "text": "High bandwidth: Xbox at 6200 KB/s (threshold: 5000)",
  "data": { "mac": "AA-BB-CC-DD-EE-FF", "name": "Xbox", "rate_kbps": 6200, "threshold_kbps": 5000 }
}
```

### Examples

```bash
# Device listing
deco clients --json                    # JSON device list
deco clients --name xbox               # Filter by name
deco clients --mac AA-BB-CC-DD-EE-FF   # Filter by MAC
deco clients --watch                   # Auto-refresh every 5s

# Live monitoring
deco poll --interval 10                # Bandwidth every 10s
deco monitor --interval 30             # Full monitoring every 30s
deco monitor --notify                  # Desktop alert on new devices
deco monitor --alert 5000              # Alert when any device exceeds 5000 KB/s
deco monitor --notify --webhook URL    # Send alerts to a webhook
deco dashboard                         # TUI dashboard

# Reports
deco report today                      # Today's bandwidth by device
deco report hour --json                # Last hour as JSON
deco report today --csv                # CSV export for spreadsheets
deco report --group gaming             # Report for tagged devices only
deco report device xbox                # Single device history
deco report device xbox --csv          # Single device CSV export
deco report network                    # WAN IP changes and CPU/memory trends
deco report mesh                       # Mesh node uptime history

# Management
deco reboot --force                    # Reboot without confirmation
deco block AA-BB-CC-DD-EE-FF           # Block a device
deco purge --days 30                   # Delete records older than 30 days
deco purge --before 2025-01-01         # Delete records before a date

# AI chat
deco chat "how many devices?"          # Single question
deco chat                              # Interactive session
deco chat --compact "status?"          # Smaller context for small models
deco chat --show-context               # Debug: print what the AI sees

# Raw API
deco api 'admin/client?form=client_list' '{"operation":"read"}'

# Debug
deco clients --verbose                 # Show encrypted request/response details
```

## Configuration

Run `deco setup` to create the config file interactively, or create it manually at `~/.config/deco/deco_config.json`:

```json
{
  "host": "192.168.68.1",
  "password": "your-admin-password"
}
```

The password is your Deco admin/management password (the one you use to log in to the web UI or app).

Config files are stored in `~/.config/deco/` (respects `$XDG_CONFIG_HOME`). Legacy files next to the binary are auto-migrated on first run.

### AI Chat

The `chat` command requires [Ollama](https://ollama.ai) running locally:

```bash
brew install ollama
brew services start ollama
ollama pull llama3.2
deco chat "which device used the most bandwidth today?"
```

The AI has access to live router data (connected devices, mesh status, WiFi config) and historical data from the SQLite database (bandwidth trends, WAN IP history, mesh uptime, all known devices). Set `OLLAMA_HOST` to use a remote Ollama instance.

## Testing

### Quick test run

```bash
scripts/test.sh          # gotestsum with testdox output
make test                # same via Makefile (falls back to go test if gotestsum missing)
```

### Generate an HTML test report

```bash
scripts/test-report.sh          # run tests + generate Allure report
scripts/test-report.sh --open   # same, opens report in browser
make test-report                # same via Makefile
make test-report-open           # same via Makefile, opens browser
```

### View the last report

```bash
scripts/view-report.sh   # open the most recent report in your browser
```

The report includes pass/fail/skip counts, individual test details, and trend charts across runs. History is tracked in `reporting/history.jsonl`.

### E2E tests

```bash
scripts/e2e.sh            # run tests tagged with e2e
```

### Prerequisites

- **gotestsum**: `go install gotest.tools/gotestsum@latest`
- **allure** (for HTML reports): `brew install allure`
- **jq** (for history tracking): `brew install jq`

## Building

```bash
go build -o deco        # or: make build
go vet ./...            # or: make vet
```

Pure Go — no CGO required. Cross-compiles to any OS/arch.

To embed a version string:

```bash
go build -ldflags "-X main.version=v1.0.0" -o deco
```

### Makefile targets

| Target | Description |
|---|---|
| `make build` | Build the `deco` binary |
| `make vet` | Run `go vet` |
| `make test` | Run tests with gotestsum (falls back to `go test`) |
| `make test-report` | Run tests and generate Allure HTML report |
| `make test-report-open` | Same, and open the report in your browser |

### Shell Completion

```bash
eval "$(deco completion bash)"       # bash
eval "$(deco completion zsh)"        # zsh
deco completion fish | source        # fish
deco completion powershell | Out-String | Invoke-Expression  # powershell
```

Add the appropriate line to your shell profile for persistent completion.

## CI/CD

GitHub Actions runs on every push and PR to `main`, testing across Ubuntu, macOS, and Windows:

- `go vet` on all platforms
- `staticcheck` on Ubuntu
- Tests via `gotestsum` with race detection
- Coverage report on Ubuntu
- JUnit XML test results uploaded as artifacts

See [`.github/workflows/ci.yml`](.github/workflows/ci.yml). Releases are built via [`.github/workflows/release.yml`](.github/workflows/release.yml).

## How It Works

The Deco API uses RSA + AES encryption for all communication. This tool handles the full handshake: fetching RSA public keys, generating AES session keys, encrypting requests, and decrypting responses. No cloud access or TP-Link account required — everything runs locally against the router's HTTP API.

## License

MIT
