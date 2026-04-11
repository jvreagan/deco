package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:           "deco",
	Short:         "Deco Network Tool",
	Long:          "CLI tool for TP-Link Deco mesh router management",
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		verbose, _ := cmd.Flags().GetBool("verbose")
		SetVerbose(verbose)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Show debug output on stderr")

	rootCmd.AddCommand(
		clientsCmd(),
		networkCmd(),
		wirelessCmd(),
		meshCmd(),
		allCmd(),
		pollCmd(),
		monitorCmd(),
		reportCmd(),
		statusCmd(),
		purgeCmd(),
		setupCmd(),
		apiCmd(),
		versionCmd(),
		rebootCmd(),
		blockCmd(),
		unblockCmd(),
		aliasCmd(),
		chatCmd(),
		dashboardCmd(),
		completionCmd(),
	)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func clientsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "List connected devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			watch, _ := cmd.Flags().GetBool("watch")
			interval, _ := cmd.Flags().GetInt("interval")
			nameFilter, _ := cmd.Flags().GetString("name")
			macFilter, _ := cmd.Flags().GetString("mac")
			if watch {
				if interval == 0 {
					interval = 5
				}
				return runWatch(interval, nameFilter, macFilter)
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runClients(jsonOut, nameFilter, macFilter)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	cmd.Flags().BoolP("watch", "w", false, "Auto-refresh client list")
	cmd.Flags().IntP("interval", "i", 5, "Refresh interval in seconds")
	cmd.Flags().StringP("name", "n", "", "Filter by device name")
	cmd.Flags().StringP("mac", "m", "", "Filter by MAC address")
	return cmd
}

func networkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Show WAN/LAN configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runNetwork(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func wirelessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wireless",
		Short: "Show WiFi configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runWireless(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func meshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh",
		Short: "Show mesh topology",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runMesh(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func allCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "all",
		Short: "Complete network snapshot (JSON)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAll()
		},
	}
}

func pollCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Start bandwidth monitoring",
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, _ := cmd.Flags().GetInt("interval")
			return runPoll(interval)
		},
	}
	cmd.Flags().IntP("interval", "i", 5, "Polling interval in seconds")
	return cmd
}

func monitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Full network monitoring to SQLite",
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, _ := cmd.Flags().GetInt("interval")
			notify, _ := cmd.Flags().GetBool("notify")
			alert, _ := cmd.Flags().GetInt("alert")
			webhook, _ := cmd.Flags().GetString("webhook")
			return runMonitor(interval, notify, alert, webhook)
		},
	}
	cmd.Flags().IntP("interval", "i", 60, "Polling interval in seconds")
	cmd.Flags().Bool("notify", false, "Alert on new MAC addresses")
	cmd.Flags().Int("alert", 0, "Alert when any device exceeds this KB/s (0 = disabled)")
	cmd.Flags().String("webhook", "", "Webhook URL for POST notifications")
	return cmd
}

func reportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report [period]",
		Short: "Show usage reports (bandwidth/network/mesh)",
		Long: `Show usage reports.

  deco report [period]           Bandwidth report (default)
  deco report network [period]   WAN IP history and CPU/memory trends
  deco report mesh [period]      Mesh node uptime history

Periods: today (default), hour, all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			csvOut, _ := cmd.Flags().GetBool("csv")
			nameFilter, _ := cmd.Flags().GetString("name")
			macFilter, _ := cmd.Flags().GetString("mac")
			group, _ := cmd.Flags().GetString("group")
			return runReport(period, jsonOut, csvOut, nameFilter, macFilter, group)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	cmd.Flags().Bool("csv", false, "Output as CSV")
	cmd.Flags().StringP("name", "n", "", "Filter by device name")
	cmd.Flags().StringP("mac", "m", "", "Filter by MAC address")
	cmd.Flags().StringP("group", "g", "", "Filter by device tag")
	cmd.AddCommand(reportNetworkCmd(), reportMeshCmd(), reportDeviceCmd())
	return cmd
}

func reportNetworkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network [period]",
		Short: "WAN IP history and CPU/memory trends",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runReportNetwork(period, jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func reportMeshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh [period]",
		Short: "Mesh node uptime history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			return runReportMesh(period, jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func reportDeviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device <MAC|name> [period]",
		Short: "Detailed history for a single device",
		Long: `Show detailed bandwidth history for a single device.

  deco report device Xbox today
  deco report device AA-BB-CC-DD-EE-FF --json
  deco report device MyPhone hour --csv

The device can be specified by MAC address, alias, or name substring.
Periods: today (default), hour, all`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			period := "today"
			if len(args) > 1 {
				period = args[1]
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			csvOut, _ := cmd.Flags().GetBool("csv")
			return runReportDevice(identifier, period, jsonOut, csvOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	cmd.Flags().Bool("csv", false, "Output as CSV")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show database statistics",
		Run: func(cmd *cobra.Command, args []string) {
			runStatus()
		},
	}
}

func purgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Delete records (all, or by --days/--before)",
		Run: func(cmd *cobra.Command, args []string) {
			force, _ := cmd.Flags().GetBool("force")
			beforeStr, _ := cmd.Flags().GetString("before")
			days, _ := cmd.Flags().GetInt("days")
			runPurge(force, beforeStr, days)
		},
	}
	cmd.Flags().BoolP("force", "f", false, "Skip confirmation")
	cmd.Flags().Int("days", 0, "Purge records older than N days")
	cmd.Flags().String("before", "", "Purge records before date (YYYY-MM-DD)")
	return cmd
}

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive configuration wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup()
		},
	}
}

func apiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api <endpoint> [json_body]",
		Short: "Call a raw API endpoint",
		Long: `Call a raw API endpoint.

Example: deco api 'admin/client?form=client_list' '{"operation":"read"}'`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := args[0]
			body := "{}"
			if len(args) > 1 {
				body = args[1]
			}
			return runAPI(endpoint, body)
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Run: func(cmd *cobra.Command, args []string) {
			runVersion()
		},
	}
}

func rebootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "Reboot the router",
		RunE: func(cmd *cobra.Command, args []string) error {
			force, _ := cmd.Flags().GetBool("force")
			return runReboot(force)
		},
	}
	cmd.Flags().BoolP("force", "f", false, "Skip confirmation")
	return cmd
}

func blockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "block <MAC>",
		Short: "Block a device by MAC address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBlock(args[0])
		},
	}
}

func unblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <MAC>",
		Short: "Unblock a device by MAC address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnblock(args[0])
		},
	}
}

func aliasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias [MAC] [name]",
		Short: "Manage device aliases",
		Long: `Manage device aliases.

Usage:
  deco alias                    List all aliases
  deco alias <MAC> <name>       Set an alias
  deco alias --remove <MAC>     Remove an alias`,
		RunE: func(cmd *cobra.Command, args []string) error {
			remove, _ := cmd.Flags().GetBool("remove")
			return runAlias(remove, args)
		},
		Args: cobra.ArbitraryArgs,
	}
	cmd.Flags().BoolP("remove", "r", false, "Remove an alias")
	cmd.AddCommand(aliasTagCmd(), aliasUntagCmd(), aliasTagsCmd())
	return cmd
}

func aliasTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <MAC> <tag>",
		Short: "Add a tag to a device",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAliasTag(args)
		},
	}
}

func aliasUntagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untag <MAC> <tag>",
		Short: "Remove a tag from a device",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAliasUntag(args)
		},
	}
}

func aliasTagsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tags",
		Short: "List all device tags",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAliasTags()
		},
	}
}

func chatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat [question]",
		Short: "Ask questions about your network using AI",
		Long: `Ask natural language questions about your network using a local Ollama LLM.

  deco chat "how many devices are connected?"   Single question
  deco chat                                      Interactive chat session

Requires Ollama running locally (ollama serve). Set OLLAMA_HOST to override URL.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model, _ := cmd.Flags().GetString("model")
			url, _ := cmd.Flags().GetString("ollama-url")
			compact, _ := cmd.Flags().GetBool("compact")
			listModels, _ := cmd.Flags().GetBool("list-models")
			showContext, _ := cmd.Flags().GetBool("show-context")

			// Resolve OLLAMA_HOST early for --list-models
			url = resolveOllamaURL(url)

			if listModels {
				return listOllamaModels(url)
			}

			query := ""
			if len(args) > 0 {
				query = args[0]
			}
			return runChat(model, url, query, compact, showContext)
		},
	}
	cmd.Flags().String("model", "llama3.2", "Ollama model to use")
	cmd.Flags().String("ollama-url", "http://localhost:11434", "Ollama API base URL")
	cmd.Flags().Bool("compact", false, "Use smaller context window (fewer devices/snapshots)")
	cmd.Flags().Bool("list-models", false, "List available Ollama models and exit")
	cmd.Flags().Bool("show-context", false, "Print system prompt to stderr and exit")
	return cmd
}

func dashboardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Live TUI dashboard",
		Long:  "Launch a live terminal dashboard showing clients, network status, mesh nodes, and activity.",
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, _ := cmd.Flags().GetInt("interval")
			return runDashboard(interval)
		},
	}
	cmd.Flags().IntP("interval", "i", 10, "Refresh interval in seconds")
	return cmd
}

func completionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate completion scripts for your shell.

  eval "$(deco completion bash)"
  eval "$(deco completion zsh)"
  deco completion fish | source
  deco completion powershell | Out-String | Invoke-Expression`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell: %s", args[0])
			}
		},
	}
}
