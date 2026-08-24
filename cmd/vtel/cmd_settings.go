package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/alaaabd90/vtel/protocol"
)

func cmdConfigShow(args []string) error {
	cfg, err := loadConfigForCLI()
	if err != nil {
		return err
	}
	reveal := false
	for _, a := range args {
		if a == "--reveal-secret" {
			reveal = true
		}
	}
	secret := "********"
	if reveal {
		secret = cfg.Secret
	}
	fmt.Printf("  Config file:       %s\n", cliConfigPath())
	fmt.Printf("  Mode:              %s\n", nonEmpty(cfg.Mode, "(not set)"))
	fmt.Printf("  Listen:            %s\n", nonEmpty(cfg.Listen, "127.0.0.1:1080"))
	fmt.Printf("  Secret:            %s\n", secret)
	fmt.Printf("  Compression level: %s\n", nonEmpty(cfg.CompressionLevel, "fastest"))
	fmt.Printf("  Reject IPv6:       %v\n", cfg.RejectIPv6)
	if cfg.QuietHours != nil {
		fmt.Printf("  Quiet hours:       %02d:00-%02d:00 (%s)\n",
			cfg.QuietHours.StartHour, cfg.QuietHours.EndHour, nonEmpty(cfg.QuietHours.Timezone, "UTC"))
	} else {
		fmt.Println("  Quiet hours:       disabled")
	}
	fmt.Printf("  Links:             %d\n", len(cfg.Links))
	return nil
}

func cmdExport(args []string) error {
	cfg, err := loadConfigForCLI()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] != "" {
		if err := os.WriteFile(args[0], data, 0600); err != nil {
			return err
		}
		fmt.Printf("  Wrote %s\n", args[0])
		return nil
	}
	fmt.Println(string(data))
	return nil
}

// settingsMenu is the interactive "Change settings" submenu. Errors are
// printed rather than returned since it's driven entirely by the menu loop,
// which doesn't propagate per-item errors (matching linksMenu/gdrive's own
// sniHostMenu/ipChangeMenu pattern).
func settingsMenu(reader *bufio.Reader) {
	fmt.Println("  a) Change secret")
	fmt.Println("  b) Change compression level (fastest|default|better|best)")
	fmt.Println("  c) Toggle reject_ipv6")
	fmt.Println("  d) Set/clear quiet hours")
	fmt.Println("  e) Change SOCKS5 listen address (client mode)")
	fmt.Print("Choice: ")
	ch, _ := reader.ReadString('\n')

	cfg, err := loadConfigForCLI()
	if err != nil {
		fmt.Printf("  error loading config: %v\n", err)
		return
	}

	switch strings.TrimSpace(ch) {
	case "a":
		fmt.Print("  New secret: ")
		v, _ := reader.ReadString('\n')
		cfg.Secret = strings.TrimSpace(v)
	case "b":
		fmt.Print("  New compression level: ")
		v, _ := reader.ReadString('\n')
		v = strings.TrimSpace(v)
		if _, err := protocol.ParseCompressionLevel(v); err != nil {
			fmt.Printf("  %v\n", err)
			return
		}
		cfg.CompressionLevel = v
	case "c":
		cfg.RejectIPv6 = !cfg.RejectIPv6
		fmt.Printf("  reject_ipv6 is now %v\n", cfg.RejectIPv6)
	case "d":
		fmt.Print("  Start hour (0-23, empty to clear quiet hours): ")
		sv, _ := reader.ReadString('\n')
		sv = strings.TrimSpace(sv)
		if sv == "" {
			cfg.QuietHours = nil
			break
		}
		start, err := strconv.Atoi(sv)
		if err != nil {
			fmt.Printf("  invalid hour: %v\n", err)
			return
		}
		fmt.Print("  End hour (0-23): ")
		ev, _ := reader.ReadString('\n')
		end, err := strconv.Atoi(strings.TrimSpace(ev))
		if err != nil {
			fmt.Printf("  invalid hour: %v\n", err)
			return
		}
		fmt.Print("  Timezone (IANA name, empty for UTC): ")
		tz, _ := reader.ReadString('\n')
		cfg.QuietHours = &protocol.QuietHoursConfig{StartHour: start, EndHour: end, Timezone: strings.TrimSpace(tz)}
	case "e":
		fmt.Print("  New listen address: ")
		v, _ := reader.ReadString('\n')
		cfg.Listen = strings.TrimSpace(v)
	default:
		return
	}

	if err := saveConfigForCLI(cfg); err != nil {
		fmt.Printf("  error saving config: %v\n", err)
		return
	}
	fmt.Println("  Saved. Apply it with: vtel restart")
}
