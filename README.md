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
| `poll` | Live bandwidth monitoring per device |
| `monitor` | Full network monitoring — logs all data to SQLite |
| `report [period]` | Show bandwidth usage report (`today`, `hour`, or `all`) |
| `status` | Show database statistics (size, record counts, date range) |
| `purge` | Delete database records (all, or by `--days`/`--before`) |
| `chat [question]` | Ask questions about your network using a local Ollama LLM |
| `api <endpoint> [body]` | Call a raw API endpoint |
| `version` | Show version |
| `reboot` | Reboot the router (confirms first, use `--force` to skip) |
| `block <MAC>` | Block a device by MAC address |
| `unblock <MAC>` | Unblock a device by MAC address |
| `alias` | Manage device aliases |
| `completion <shell>` | Generate shell completion script (`bash`, `zsh`, `fish`) |

### Options

| Flag | Description |
|---|---|
| `--json`, `-j` | Output as JSON (works with `clients`, `network`, `wireless`, `mesh`, `report`) |
| `--interval N`, `-i N` | Polling interval in seconds (default: 5 for `poll`, 60 for `monitor`) |
| `--force`, `-f` | Skip confirmation prompt for `purge` and `reboot` |
| `--name`, `-n <name>` | Filter by device name — substring, case-insensitive (`clients`, `report`) |
| `--mac`, `-m <MAC>` | Filter by MAC address — exact match, case-insensitive (`clients`, `report`) |
| `--watch`, `-w` | Auto-refresh client list (use with `clients`) |
| `--notify` | Alert on new MAC addresses (use with `monitor`) |
| `--days N` | Purge records older than N days (use with `purge`) |
| `--before YYYY-MM-DD` | Purge records before date (use with `purge`) |
| `--model <name>` | Ollama model to use (default: `llama3.2`, use with `chat`) |
| `--ollama-url <url>` | Ollama API base URL (use with `chat`) |
| `--compact` | Use smaller context window for chat (use with `chat`) |

### Device Aliases

Set friendly names for devices that override the router-reported name:

```bash
deco alias                              # List all aliases
deco alias AA-BB-CC-DD-EE-FF "TV"       # Set an alias
deco alias --remove AA-BB-CC-DD-EE-FF   # Remove an alias
```

Aliases are stored in `~/.config/deco/deco_aliases.json` and are applied in `clients`, `report`, and `chat` output.

### Examples

```bash
deco clients --json          # JSON device list
deco clients --name xbox     # Filter by name
deco clients --mac AA-BB-CC-DD-EE-FF  # Filter by MAC
deco clients --watch         # Auto-refresh every 5s
deco poll --interval 10      # Bandwidth every 10s
deco monitor --interval 30   # Full monitoring every 30s
deco report today            # Today's bandwidth by device
deco report hour --json      # Last hour as JSON
deco reboot --force          # Reboot without confirmation
deco block AA-BB-CC-DD-EE-FF # Block a device
deco purge --days 30           # Delete records older than 30 days
deco purge --before 2025-01-01 # Delete records before a date
deco chat "how many devices?"  # Single AI question
deco chat                      # Interactive AI chat session
deco chat --compact "status?"  # Use smaller context for small models
deco api 'admin/client?form=client_list' '{"operation":"read"}'
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

## Building

```bash
go build -o deco
```

Pure Go — no CGO required. Cross-compiles to any OS/arch.

To embed a version string:

```bash
go build -ldflags "-X main.version=v1.0.0" -o deco
```

### Shell Completion

```bash
eval "$(deco completion bash)"   # bash
eval "$(deco completion zsh)"    # zsh
deco completion fish | source    # fish
```

Add the appropriate line to your shell profile for persistent completion.

## How It Works

The Deco API uses RSA + AES encryption for all communication. This tool handles the full handshake: fetching RSA public keys, generating AES session keys, encrypting requests, and decrypting responses. No cloud access or TP-Link account required — everything runs locally against the router's HTTP API.

## License

MIT
