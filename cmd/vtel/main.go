package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alaaabd90/vtel/tunnel"
	"github.com/alaaabd90/vtel/vtelconfig"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		runMenu()
		return
	}

	switch os.Args[1] {
	case "menu":
		runMenu()
	case "status":
		cmdStatus()
	case "restart":
		cmdRestart()
	case "logs":
		cmdLogs()
	case "install":
		if err := cmdInstall(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := cmdUninstall(os.Args[2:]); err != nil && err != errUninstallCancelled {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "update":
		if err := cmdUpdate(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "rollback":
		if err := cmdRollback(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "links":
		if err := cmdLinks(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "account":
		if err := cmdAccount(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "config":
		if err := cmdConfigShow(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "export":
		if err := cmdExport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Printf("vtel %s commit=%s date=%s\n", version, commit, date)
	case "help", "--help", "-h":
		printUsage()
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			// e.g. `-config /path/to/config.json` - run the actual tunnel
			// (this is what systemd's ExecStart invokes).
			runTunnel()
			return
		}
		printUsage()
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`vtel - SOCKS5-over-Telegram tunnel manager

  vtel                      interactive menu
  vtel -config <path>       run client/server (systemd ExecStart)
  vtel status                service + config summary
  vtel restart                restart the systemd service
  vtel logs                   follow live journal
  vtel links                  list configured links
  vtel links add               add a link interactively
  vtel links remove <N>        remove link #N
  vtel account login -phone <+1...>  log a real account into MTProto (one-time, interactive)
  vtel config                 show current config (secret redacted)
  vtel config --reveal-secret  show current config with the real secret
  vtel export [file]          print (or write) the full config JSON
  vtel update                 download and install the latest release
  vtel rollback <tag>         install a specific previous release
  vtel install                 (re)create and start the systemd service
  vtel uninstall [--force]    permanently remove vtel: service, config, binary
  vtel version                 print version
`)
}

// runTunnel is the original (pre-CLI) entry point: parse -config and run as
// a client or server, unchanged from before this CLI subsystem existed.
func runTunnel() {
	cfg := ParseConfig()

	specs, err := vtelconfig.BuildLinkSpecs(&cfg, func(i int, ownID int64, err error) {
		kind, idLabel := "bot", "bot ID"
		if cfg.Links[i].IsAccount() {
			kind, idLabel = "account", "user ID"
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to verify %s link %d: %v\n", kind, i, err)
			return
		}
		fmt.Printf("[vtel] link %d: %s %d\n", i, idLabel, ownID)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("[vtel] mode: %s, %d link(s)\n", cfg.Mode, len(specs))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	switch cfg.Mode {
	case "client":
		c := tunnel.NewClient(specs, cfg.Listen, cfg.RejectIPv6)
		go func() {
			<-sigCh
			fmt.Println("\n[vtel] shutting down...")
			c.Stop()
			os.Exit(0)
		}()
		if err := c.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Client error: %v\n", err)
			os.Exit(1)
		}

	case "server":
		s := tunnel.NewServer(specs)
		go func() {
			<-sigCh
			fmt.Println("\n[vtel] shutting down...")
			s.Stop()
			os.Exit(0)
		}()
		s.Run()
	}
}
