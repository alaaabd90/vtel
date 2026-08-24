package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runMenu() {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\033[2J\033[H") // clear screen
		printMenuHeader()
		fmt.Print("Choice: ")
		line, _ := reader.ReadString('\n')
		choice := strings.TrimSpace(line)
		fmt.Println()
		switch choice {
		case "1":
			cmdStatus()
		case "2":
			cmdRestart()
		case "3":
			cmdLogs()
		case "4":
			linksMenu(reader)
			continue
		case "5":
			settingsMenu(reader)
		case "6":
			if err := cmdConfigShow(nil); err != nil {
				fmt.Printf("  error: %v\n", err)
			}
		case "7":
			fmt.Println("Updating binary...")
			if err := cmdUpdate(); err != nil {
				fmt.Printf("update failed: %v\n", err)
			}
		case "8":
			fmt.Print("Version/tag to roll back to (e.g. v1.0.0, empty to list available): ")
			v, _ := reader.ReadString('\n')
			if err := cmdRollback([]string{strings.TrimSpace(v)}); err != nil {
				fmt.Printf("rollback failed: %v\n", err)
			}
		case "9":
			err := cmdUninstallWithReader(nil, reader)
			if err == nil {
				return // vtel no longer exists on disk, nothing left to loop back to
			}
			if err != errUninstallCancelled {
				fmt.Printf("uninstall failed: %v\n", err)
			}
		case "0", "q", "quit", "exit":
			fmt.Println("Bye.")
			return
		default:
			fmt.Println("Invalid choice.")
		}
		if choice != "3" {
			fmt.Print("\nPress Enter to continue...")
			_, _ = reader.ReadString('\n')
		}
	}
}

func printMenuHeader() {
	cfg, _ := loadConfigForCLI() // best-effort; a missing/unreadable config just shows placeholders
	title := fmt.Sprintf("vtel Manager (%s)", version)
	mode := nonEmpty(cfg.Mode, "(none)")
	fmt.Printf(`
  +----------------------------------------+
  | %-38s |
  |  Mode: %-9s Links: %-13d |
  +----------------------------------------+
  |  1) Show status                        |
  |  2) Restart service                    |
  |  3) View live logs                     |
  |  4) Manage links (bots/channels)       |
  |  5) Change settings                    |
  |  6) Show/export config                 |
  |  7) Update binary                      |
  |  8) Roll back to a previous version    |
  |  9) Uninstall vtel completely          |
  |  0) Exit                               |
  +----------------------------------------+
`, title, mode, len(cfg.Links))
}

func linksMenu(reader *bufio.Reader) {
	for {
		fmt.Println("  a) List links")
		fmt.Println("  b) Add a link")
		fmt.Println("  c) Remove a link")
		fmt.Println("  d) Back to main menu")
		fmt.Print("Choice: ")
		ch, _ := reader.ReadString('\n')
		fmt.Println()
		switch strings.TrimSpace(ch) {
		case "a":
			if err := linksList(); err != nil {
				fmt.Printf("  error: %v\n", err)
			}
		case "b":
			if err := linksAddInteractive(reader); err != nil {
				fmt.Printf("  error: %v\n", err)
			}
		case "c":
			_ = linksList()
			fmt.Print("  Index to remove: ")
			v, _ := reader.ReadString('\n')
			idx, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				fmt.Printf("  invalid index: %v\n", err)
				break
			}
			if err := linksRemove(idx); err != nil {
				fmt.Printf("  error: %v\n", err)
			}
		case "d", "":
			return
		default:
			fmt.Println("  Invalid choice.")
		}
		fmt.Print("\nPress Enter to continue...")
		_, _ = reader.ReadString('\n')
	}
}
