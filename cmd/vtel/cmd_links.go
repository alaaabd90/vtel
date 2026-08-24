package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func cmdLinks(args []string) error {
	if len(args) == 0 {
		return linksList()
	}
	switch args[0] {
	case "list":
		return linksList()
	case "add":
		return linksAddInteractive(bufio.NewReader(os.Stdin))
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: vtel links remove <index>")
		}
		idx, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid index %q", args[1])
		}
		return linksRemove(idx)
	default:
		return fmt.Errorf("unknown links subcommand %q (want list|add|remove)", args[0])
	}
}

func linksList() error {
	cfg, err := loadConfigForCLI()
	if err != nil {
		return err
	}
	if len(cfg.Links) == 0 {
		fmt.Println("  No links configured.")
		return nil
	}
	fmt.Printf("  %-4s %-22s %-16s %-18s\n", "#", "token (redacted)", "peer_bot_id", "channel_id")
	fmt.Println("  " + strings.Repeat("-", 62))
	for i, l := range cfg.Links {
		fmt.Printf("  %-4d %-22s %-16d %-18d\n", i, redactToken(l.Token), l.PeerBotID, l.ChannelID)
	}
	return nil
}

func redactToken(tok string) string {
	if len(tok) <= 10 {
		return "****"
	}
	return tok[:6] + "..." + tok[len(tok)-4:]
}

func linksAddInteractive(reader *bufio.Reader) error {
	cfg, err := loadConfigForCLI()
	if err != nil {
		return err
	}

	fmt.Print("  Bot token: ")
	token, _ := reader.ReadString('\n')
	token = strings.TrimSpace(token)

	fmt.Print("  Peer bot user ID: ")
	peerStr, _ := reader.ReadString('\n')
	peer, err := strconv.ParseInt(strings.TrimSpace(peerStr), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid peer_bot_id: %w", err)
	}

	fmt.Print("  Channel ID: ")
	chStr, _ := reader.ReadString('\n')
	ch, err := strconv.ParseInt(strings.TrimSpace(chStr), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid channel_id: %w", err)
	}

	if token == "" || peer == 0 || ch == 0 {
		return fmt.Errorf("token, peer_bot_id, and channel_id are all required")
	}

	cfg.Links = append(cfg.Links, LinkConfig{Token: token, PeerBotID: peer, ChannelID: ch})
	if err := saveConfigForCLI(cfg); err != nil {
		return err
	}
	fmt.Printf("  Added link #%d. Apply it with: vtel install  (or: vtel restart)\n", len(cfg.Links)-1)
	return nil
}

func linksRemove(idx int) error {
	cfg, err := loadConfigForCLI()
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(cfg.Links) {
		return fmt.Errorf("index %d out of range (0-%d)", idx, len(cfg.Links)-1)
	}
	cfg.Links = append(cfg.Links[:idx], cfg.Links[idx+1:]...)
	if err := saveConfigForCLI(cfg); err != nil {
		return err
	}
	fmt.Printf("  Removed link #%d. Apply it with: vtel restart\n", idx)
	return nil
}
