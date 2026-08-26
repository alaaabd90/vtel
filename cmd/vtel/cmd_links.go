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
	fmt.Printf("  %-4s %-8s %-30s %-16s %-18s\n", "#", "kind", "token (redacted) / session path", "peer_id", "channel_id")
	fmt.Println("  " + strings.Repeat("-", 80))
	for i, l := range cfg.Links {
		if l.IsAccount() {
			fmt.Printf("  %-4d %-8s %-30s %-16d %-18d\n", i, "account", l.Session, l.PeerUserID, l.ChannelID)
			continue
		}
		fmt.Printf("  %-4d %-8s %-30s %-16d %-18d\n", i, "bot", redactToken(l.Token), l.PeerBotID, l.ChannelID)
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

	fmt.Print("  Link kind - bot or account [bot]: ")
	kindStr, _ := reader.ReadString('\n')
	kindStr = strings.TrimSpace(kindStr)
	if kindStr == "" {
		kindStr = "bot"
	}
	if kindStr != "bot" && kindStr != "account" {
		return fmt.Errorf("kind must be \"bot\" or \"account\", got %q", kindStr)
	}

	var lc LinkConfig
	if kindStr == "account" {
		lc, err = linksAddAccountFields(reader)
	} else {
		lc, err = linksAddBotFields(reader)
	}
	if err != nil {
		return err
	}

	fmt.Print("  Channel ID: ")
	chStr, _ := reader.ReadString('\n')
	ch, err := strconv.ParseInt(strings.TrimSpace(chStr), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid channel_id: %w", err)
	}
	if ch > 0 {
		return fmt.Errorf("channel_id must be negative (Telegram channel/supergroup IDs always start with -100...) - did you drop the leading minus sign?")
	}
	if ch == 0 {
		return fmt.Errorf("channel_id is required")
	}
	lc.ChannelID = ch

	cfg.Links = append(cfg.Links, lc)
	if err := saveConfigForCLI(cfg); err != nil {
		return err
	}
	fmt.Printf("  Added link #%d. Apply it with: vtel install  (or: vtel restart)\n", len(cfg.Links)-1)
	return nil
}

func linksAddBotFields(reader *bufio.Reader) (LinkConfig, error) {
	fmt.Print("  Bot token: ")
	token, _ := reader.ReadString('\n')
	token = strings.TrimSpace(token)

	fmt.Print("  Peer bot user ID: ")
	peerStr, _ := reader.ReadString('\n')
	peer, err := strconv.ParseInt(strings.TrimSpace(peerStr), 10, 64)
	if err != nil {
		return LinkConfig{}, fmt.Errorf("invalid peer_bot_id: %w", err)
	}
	if token == "" || peer == 0 {
		return LinkConfig{}, fmt.Errorf("token and peer_bot_id are both required")
	}
	return LinkConfig{Kind: "bot", Token: token, PeerBotID: peer}, nil
}

// linksAddAccountFields prompts for an account-kind link's fields. It does
// not itself perform the MTProto login - that's `vtel account login`,
// which is expected to have already been run (once per phone number) to
// produce the session file path this prompts for.
func linksAddAccountFields(reader *bufio.Reader) (LinkConfig, error) {
	fmt.Print("  Session file path (from `vtel account login`): ")
	session, _ := reader.ReadString('\n')
	session = strings.TrimSpace(session)

	fmt.Print("  Peer account user ID (printed by the peer's `vtel account login`): ")
	peerStr, _ := reader.ReadString('\n')
	peer, err := strconv.ParseInt(strings.TrimSpace(peerStr), 10, 64)
	if err != nil {
		return LinkConfig{}, fmt.Errorf("invalid peer_user_id: %w", err)
	}
	if session == "" || peer == 0 {
		return LinkConfig{}, fmt.Errorf("session and peer_user_id are both required")
	}
	return LinkConfig{Kind: "account", Session: session, PeerUserID: peer}, nil
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
