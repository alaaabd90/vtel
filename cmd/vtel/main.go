// Command vtel runs either half of a Telegram-Bot-API-backed SOCKS5
// tunnel. One binary, two subcommands - gdrive's split into a dual-purpose
// binary plus a separate fleet-management wrapper exists only because of
// production features (fleet orchestration, service install, OAuth
// wizard) this v1 doesn't have; with none of that, there's no asymmetry
// between the client and exit roles worth separate binaries for.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/alaaabd90/vtel/internal/vtel"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vtel:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("missing subcommand")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch args[0] {
	case "serve-client":
		return runServeClient(ctx, args[1:])
	case "serve-exit":
		return runServeExit(ctx, args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage:
  vtel serve-client -config <path> [-listen <addr>]
  vtel serve-exit   -config <path>`)
}

func runServeClient(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve-client", flag.ContinueOnError)
	configPath := fs.String("config", "vtel.json", "path to config JSON")
	listen := fs.String("listen", "", "SOCKS5 listen address (overrides the config's \"listen\")")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := vtel.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	addr := cfg.Listen
	if *listen != "" {
		addr = *listen
	}
	if addr == "" {
		return fmt.Errorf("no SOCKS5 listen address: set \"listen\" in the config or pass -listen")
	}

	bots, err := vtel.BotsFromConfig(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("bots from config: %w", err)
	}

	tunnel, err := vtel.NewTunnel(bots, cfg)
	if err != nil {
		return fmt.Errorf("new tunnel: %w", err)
	}
	tunnel.Logger = logger

	logger.Printf("vtel: serving SOCKS5 on %s (%d bot lane(s))", addr, len(bots))
	return tunnel.ServeClient(ctx, addr)
}

func runServeExit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve-exit", flag.ContinueOnError)
	configPath := fs.String("config", "vtel.json", "path to config JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := vtel.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	bots, err := vtel.BotsFromConfig(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("bots from config: %w", err)
	}

	tunnel, err := vtel.NewTunnel(bots, cfg)
	if err != nil {
		return fmt.Errorf("new tunnel: %w", err)
	}
	tunnel.Logger = logger

	logger.Printf("vtel: serving exit role (%d bot lane(s))", len(bots))
	return tunnel.ServeExit(ctx)
}
