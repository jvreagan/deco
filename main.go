package main

import (
	"fmt"
	"os"
	"strings"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "clients":
		if hasFlag("--watch", "-w") {
			interval := getFlagInt("--interval", "-i", 5)
			runWatch(interval)
		} else {
			jsonOut := hasFlag("--json", "-j")
			runClients(jsonOut)
		}
	case "network":
		jsonOut := hasFlag("--json", "-j")
		runNetwork(jsonOut)
	case "wireless":
		jsonOut := hasFlag("--json", "-j")
		runWireless(jsonOut)
	case "mesh":
		jsonOut := hasFlag("--json", "-j")
		runMesh(jsonOut)
	case "all":
		runAll()
	case "poll":
		interval := getFlagInt("--interval", "-i", 5)
		runPoll(interval)
	case "monitor":
		interval := getFlagInt("--interval", "-i", 60)
		runMonitor(interval)
	case "report":
		period := "today"
		if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
			period = os.Args[2]
		}
		jsonOut := hasFlag("--json", "-j")
		runReport(period, jsonOut)
	case "status":
		runStatus()
	case "purge":
		force := hasFlag("--force", "-f")
		runPurge(force)
	case "setup":
		runSetup()
	case "api":
		if len(os.Args) < 3 {
			fmt.Println("Usage: deco api <endpoint> [json_body]")
			fmt.Println("Example: deco api 'admin/client?form=client_list' '{\"operation\":\"read\"}'")
			os.Exit(1)
		}
		endpoint := os.Args[2]
		body := "{}"
		if len(os.Args) > 3 {
			body = os.Args[3]
		}
		runAPI(endpoint, body)
	case "version":
		runVersion()
	case "reboot":
		runReboot()
	case "block":
		if len(os.Args) < 3 {
			fmt.Println("Usage: deco block <MAC>")
			os.Exit(1)
		}
		runBlock(os.Args[2])
	case "unblock":
		if len(os.Args) < 3 {
			fmt.Println("Usage: deco unblock <MAC>")
			os.Exit(1)
		}
		runUnblock(os.Args[2])
	case "alias":
		runAlias()
	case "completion":
		if len(os.Args) < 3 {
			fmt.Println("Usage: deco completion <bash|zsh|fish>")
			fmt.Println("\nAdd to your shell profile:")
			fmt.Println("  eval \"$(deco completion bash)\"   # bash")
			fmt.Println("  eval \"$(deco completion zsh)\"    # zsh")
			fmt.Println("  deco completion fish | source     # fish")
			os.Exit(1)
		}
		runCompletion(os.Args[2])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Deco Network Tool

Commands:
  setup                Interactive configuration wizard
  clients              List connected devices
  network              Show WAN/LAN configuration
  wireless             Show WiFi configuration
  mesh                 Show mesh topology
  all                  Complete network snapshot (JSON)
  poll                 Start bandwidth monitoring
  monitor              Full network monitoring to SQLite
  report [period]      Show usage report (today/hour/all)
  status               Show database statistics
  purge                Delete all records
  api <endpoint>       Call a raw API endpoint
  version              Show version
  reboot               Reboot the router
  block <MAC>          Block a device by MAC address
  unblock <MAC>        Unblock a device by MAC address
  alias                Manage device aliases
  completion <shell>   Generate shell completion (bash/zsh/fish)

Options:
  --json, -j           Output as JSON
  --interval, -i N     Polling interval in seconds (default: 5 for poll, 60 for monitor)
  --force, -f          Skip confirmation for purge/reboot
  --name, -n <name>    Filter by device name (clients, report)
  --mac, -m <MAC>      Filter by MAC address (clients, report)
  --watch, -w          Auto-refresh client list (use with clients)

Alias usage:
  deco alias                    List all aliases
  deco alias <MAC> <name>       Set an alias
  deco alias --remove <MAC>     Remove an alias

Examples:
  deco setup
  deco clients
  deco clients --json
  deco clients --name xbox
  deco clients --watch
  deco poll --interval 10
  deco monitor
  deco monitor --interval 30
  deco report today
  deco report hour --json
  deco purge --force
  deco alias AA-BB-CC-DD-EE-FF "Living Room TV"
  deco reboot
  eval "$(deco completion bash)"`)
}

func hasFlag(flags ...string) bool {
	for _, arg := range os.Args {
		for _, flag := range flags {
			if arg == flag {
				return true
			}
		}
	}
	return false
}

func getFlagInt(long, short string, defaultVal int) int {
	for i, arg := range os.Args {
		if (arg == long || arg == short) && i+1 < len(os.Args) {
			var val int
			fmt.Sscanf(os.Args[i+1], "%d", &val)
			if val > 0 {
				return val
			}
		}
	}
	return defaultVal
}

func getFlagString(long, short string) string {
	for i, arg := range os.Args {
		if (arg == long || arg == short) && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}
