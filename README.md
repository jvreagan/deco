# Deco

CLI tool for TP-Link Deco mesh routers. Communicates with the router's encrypted API to show connected clients, network configuration, mesh topology, WiFi settings, and real-time bandwidth — with optional SQLite logging for historical analysis.

Tested on the **Deco BE63** but may work with other Deco models that use the same API.

## Quick Start

```bash
go build -o deco
./deco setup
./deco clients
```

## Commands

| Command | Description |
|---|---|
| `setup` | Interactive configuration wizard |
| `clients` | List connected devices (name, IP, MAC, connection type, signal) |
| `network` | Show WAN/LAN configuration (IPs, DNS, gateway, CPU/memory usage) |
| `wireless` | Show WiFi configuration (SSIDs, channels, bands, guest network) |
| `mesh` | Show mesh node topology (role, firmware, status) |
| `all` | Complete network snapshot as JSON |
| `poll` | Live bandwidth monitoring per device |
| `monitor` | Full network monitoring — logs all data to SQLite |
| `report [period]` | Show bandwidth usage report (`today`, `hour`, or `all`) |
| `status` | Show database statistics (size, record counts, date range) |
| `purge` | Delete all database records |
| `api <endpoint> [body]` | Call a raw API endpoint |

### Options

| Flag | Description |
|---|---|
| `--json`, `-j` | Output as JSON (works with `clients`, `network`, `wireless`, `mesh`, `report`) |
| `--interval N`, `-i N` | Polling interval in seconds (default: 5 for `poll`, 60 for `monitor`) |
| `--force`, `-f` | Skip confirmation prompt for `purge` |

### Examples

```bash
deco clients --json          # JSON device list
deco poll --interval 10      # Bandwidth every 10s
deco monitor --interval 30   # Full monitoring every 30s
deco report today            # Today's bandwidth by device
deco report hour --json      # Last hour as JSON
deco api 'admin/client?form=client_list' '{"operation":"read"}'
```

## Configuration

Run `deco setup` to create the config file interactively, or create `deco_config.json` manually next to the binary:

```json
{
  "host": "192.168.68.1",
  "password": "your-admin-password"
}
```

The password is your Deco admin/management password (the one you use to log in to the web UI or app).

## Building

```bash
go build -o deco
```

Requires:
- Go 1.21+
- CGO enabled (for SQLite via `mattn/go-sqlite3`)
- A TP-Link Deco router on your network

## How It Works

The Deco API uses RSA + AES encryption for all communication. This tool handles the full handshake: fetching RSA public keys, generating AES session keys, encrypting requests, and decrypting responses. No cloud access or TP-Link account required — everything runs locally against the router's HTTP API.

## License

MIT
