package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// serviceStateString shells out to systemctl rather than talking to a
// running vtel process directly (no status/IPC endpoint exists, and none is
// added here) - the same non-invasive approach gdrive's own cmdStatus uses
// (systemctl is-active + reading the account's config off disk, not an RPC
// to the live process).
func serviceStateString(name string) string {
	out, err := exec.Command("systemctl", "is-active", name).Output()
	if err != nil && len(out) == 0 {
		return "not-found"
	}
	return strings.TrimSpace(string(out))
}

func isServiceActive(name string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
}

func cmdStatus() {
	fmt.Printf("  %-20s %s\n", "SERVICE", "STATUS")
	fmt.Println("  " + strings.Repeat("-", 40))
	state := serviceStateString(serviceName)
	color := "\033[31m"
	if state == "active" {
		color = "\033[32m"
	}
	fmt.Printf("  %-20s %s%-12s\033[0m\n", serviceName, color, state)
	fmt.Println()

	cfg, err := loadConfigForCLI()
	if err != nil {
		fmt.Printf("  Config: %s (not readable: %v)\n", cliConfigPath(), err)
		return
	}
	fmt.Printf("  Config:      %s\n", cliConfigPath())
	fmt.Printf("  Mode:        %s\n", nonEmpty(cfg.Mode, "(not set)"))
	if cfg.Mode == "client" {
		fmt.Printf("  Listen:      %s\n", nonEmpty(cfg.Listen, "127.0.0.1:1080"))
	}
	fmt.Printf("  Links:       %d\n", len(cfg.Links))
	fmt.Printf("  Compression: %s\n", nonEmpty(cfg.CompressionLevel, "fastest"))
	fmt.Printf("  Reject IPv6: %v\n", cfg.RejectIPv6)
	if cfg.QuietHours != nil {
		fmt.Printf("  Quiet hours: %02d:00-%02d:00 (%s)\n",
			cfg.QuietHours.StartHour, cfg.QuietHours.EndHour, nonEmpty(cfg.QuietHours.Timezone, "UTC"))
	} else {
		fmt.Println("  Quiet hours: disabled")
	}
}

func cmdRestart() {
	fmt.Printf("  Restarting %s... ", serviceName)
	if err := exec.Command("systemctl", "restart", serviceName).Run(); err != nil {
		fmt.Printf("\033[31mFAILED: %v\033[0m\n", err)
		return
	}
	fmt.Printf("\033[32mOK\033[0m\n")
}

func cmdLogs() {
	cmd := exec.Command("journalctl", "-u", serviceName, "-f", "-n", "100")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

// cmdInstall (re)creates and starts the systemd unit for whatever config is
// currently at cliConfigPath(). Used by install.sh (once a config with at
// least one link exists) and standalone as `vtel install` after adding
// links by hand.
func cmdInstall() error {
	path := cliConfigPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no config at %s yet - create one first (see 'vtel links add' or config.example.json)", path)
	}

	unit := fmt.Sprintf(`[Unit]
Description=vtel SOCKS5-over-Telegram tunnel
After=network.target

[Service]
Type=simple
ExecStart=%s -config %s
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, binaryPath, path)

	unitPath := "/etc/systemd/system/" + serviceName + ".service"
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath, err)
	}
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	_ = exec.Command("systemctl", "enable", serviceName).Run()
	if err := exec.Command("systemctl", "restart", serviceName).Run(); err != nil {
		return fmt.Errorf("%s failed to start: %w (check: journalctl -u %s -n 50)", serviceName, err, serviceName)
	}
	fmt.Printf("  Installed and started %s.service\n", serviceName)
	return nil
}

// errUninstallCancelled distinguishes "user didn't confirm" from a real
// failure, so the interactive menu can tell the two apart instead of
// treating a cancel as either an error or a completed uninstall.
var errUninstallCancelled = errors.New("uninstall cancelled")

func cmdUninstall(args []string) error {
	return cmdUninstallWithReader(args, bufio.NewReader(os.Stdin))
}

// cmdUninstallWithReader takes an explicit reader (mirroring gdrive's own
// uninstall) rather than opening a second bufio.Reader over os.Stdin, since
// the interactive menu already owns one for the whole session - two
// independent readers over the same stdin can each buffer bytes the other
// needed. Requires typing the exact phrase "DELETE EVERYTHING" unless
// --force is given, since there is no undo once this runs.
func cmdUninstallWithReader(args []string, reader *bufio.Reader) error {
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}

	if !force {
		fmt.Println("This will PERMANENTLY:")
		fmt.Println("  - stop and remove the vtel systemd service")
		fmt.Printf("  - delete %s (config, secret, bot links)\n", vtelRoot)
		fmt.Printf("  - delete the installed vtel binary (%s)\n", binaryPath)
		fmt.Println()
		fmt.Println("This cannot be undone.")
		fmt.Print("Type DELETE EVERYTHING to confirm: ")
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) != "DELETE EVERYTHING" {
			fmt.Println("Cancelled.")
			return errUninstallCancelled
		}
	}
	fmt.Println()

	fmt.Println("Stopping and removing the systemd service...")
	_ = exec.Command("systemctl", "stop", serviceName).Run()
	_ = exec.Command("systemctl", "disable", serviceName).Run()
	_ = os.Remove("/etc/systemd/system/" + serviceName + ".service")
	_ = exec.Command("systemctl", "daemon-reload").Run()
	fmt.Println("  Service stopped, disabled, and unit file removed.")

	fmt.Println("Removing vtel files...")
	if err := os.RemoveAll(vtelRoot); err != nil {
		fmt.Printf("  warning: could not remove %s: %v\n", vtelRoot, err)
	} else {
		fmt.Printf("  Removed %s\n", vtelRoot)
	}

	fmt.Println("Removing installed binary...")
	// binaryPath is the executable currently running this command - on
	// Linux, unlinking a running binary is safe (the process keeps running
	// from the already-open inode until it exits), so this is done last.
	if err := os.Remove(binaryPath); err == nil {
		fmt.Printf("  Removed %s\n", binaryPath)
	}

	fmt.Println()
	fmt.Println("vtel has been fully uninstalled.")
	return nil
}
