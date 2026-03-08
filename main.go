package main

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "deco",
	Short: "Deco Network Tool",
	Long:  "CLI tool for TP-Link Deco mesh router management",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		verbose, _ := cmd.Flags().GetBool("verbose")
		SetVerbose(verbose)
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
	)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func clientsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "List connected devices",
		Run: func(cmd *cobra.Command, args []string) {
			watch, _ := cmd.Flags().GetBool("watch")
			interval, _ := cmd.Flags().GetInt("interval")
			nameFilter, _ := cmd.Flags().GetString("name")
			macFilter, _ := cmd.Flags().GetString("mac")
			if watch {
				if interval == 0 {
					interval = 5
				}
				runWatch(interval, nameFilter, macFilter)
			} else {
				jsonOut, _ := cmd.Flags().GetBool("json")
				runClients(jsonOut, nameFilter, macFilter)
			}
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
		Run: func(cmd *cobra.Command, args []string) {
			jsonOut, _ := cmd.Flags().GetBool("json")
			runNetwork(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func wirelessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wireless",
		Short: "Show WiFi configuration",
		Run: func(cmd *cobra.Command, args []string) {
			jsonOut, _ := cmd.Flags().GetBool("json")
			runWireless(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func meshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh",
		Short: "Show mesh topology",
		Run: func(cmd *cobra.Command, args []string) {
			jsonOut, _ := cmd.Flags().GetBool("json")
			runMesh(jsonOut)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	return cmd
}

func allCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "all",
		Short: "Complete network snapshot (JSON)",
		Run: func(cmd *cobra.Command, args []string) {
			runAll()
		},
	}
}

func pollCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Start bandwidth monitoring",
		Run: func(cmd *cobra.Command, args []string) {
			interval, _ := cmd.Flags().GetInt("interval")
			runPoll(interval)
		},
	}
	cmd.Flags().IntP("interval", "i", 5, "Polling interval in seconds")
	return cmd
}

func monitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Full network monitoring to SQLite",
		Run: func(cmd *cobra.Command, args []string) {
			interval, _ := cmd.Flags().GetInt("interval")
			runMonitor(interval)
		},
	}
	cmd.Flags().IntP("interval", "i", 60, "Polling interval in seconds")
	return cmd
}

func reportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report [period]",
		Short: "Show usage report (today/hour/all)",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			nameFilter, _ := cmd.Flags().GetString("name")
			macFilter, _ := cmd.Flags().GetString("mac")
			runReport(period, jsonOut, nameFilter, macFilter)
		},
	}
	cmd.Flags().BoolP("json", "j", false, "Output as JSON")
	cmd.Flags().StringP("name", "n", "", "Filter by device name")
	cmd.Flags().StringP("mac", "m", "", "Filter by MAC address")
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
		Run: func(cmd *cobra.Command, args []string) {
			runSetup()
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
		Run: func(cmd *cobra.Command, args []string) {
			endpoint := args[0]
			body := "{}"
			if len(args) > 1 {
				body = args[1]
			}
			runAPI(endpoint, body)
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
		Run: func(cmd *cobra.Command, args []string) {
			force, _ := cmd.Flags().GetBool("force")
			runReboot(force)
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
		Run: func(cmd *cobra.Command, args []string) {
			runBlock(args[0])
		},
	}
}

func unblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <MAC>",
		Short: "Unblock a device by MAC address",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runUnblock(args[0])
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
		Run: func(cmd *cobra.Command, args []string) {
			remove, _ := cmd.Flags().GetBool("remove")
			runAlias(remove, args)
		},
		Args: cobra.ArbitraryArgs,
	}
	cmd.Flags().BoolP("remove", "r", false, "Remove an alias")
	return cmd
}

