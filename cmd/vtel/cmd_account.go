package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gotd/td/session"
	gotdtg "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// cmdAccount handles `vtel account ...` - currently just `login`, the
// one-time interactive step that logs a real phone number into MTProto and
// writes a session file `vtel links add` (kind: account) then points at.
// This can't be done non-interactively: Telegram sends the login code by
// SMS/app notification to the phone being logged in, so a human has to be
// there to read and type it (and the 2FA password, if the account has one).
func cmdAccount(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: vtel account login -phone <+15551234567> [-api-id N -api-hash H] [-session path]")
	}
	switch args[0] {
	case "login":
		return cmdAccountLogin(args[1:])
	default:
		return fmt.Errorf("unknown account subcommand %q (want: login)", args[0])
	}
}

func cmdAccountLogin(args []string) error {
	fs := flag.NewFlagSet("account login", flag.ExitOnError)
	phone := fs.String("phone", "", "Phone number to log in, with country code (e.g. +15551234567)")
	apiID := fs.Int("api-id", 0, "Telegram api_id from https://my.telegram.org (defaults to the config's telegram_api_id)")
	apiHash := fs.String("api-hash", "", "Telegram api_hash from https://my.telegram.org (defaults to the config's telegram_api_hash)")
	sessionPath := fs.String("session", "", "Where to write the session file (default: <config dir>/accounts/<phone>.session)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *phone == "" {
		return fmt.Errorf("-phone is required, e.g. -phone +15551234567")
	}

	cfg, err := loadConfigForCLI()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load config: %w", err)
	}
	if *apiID != 0 {
		cfg.TelegramAPIID = *apiID
	}
	if *apiHash != "" {
		cfg.TelegramAPIHash = *apiHash
	}
	if cfg.TelegramAPIID == 0 || cfg.TelegramAPIHash == "" {
		return fmt.Errorf("telegram_api_id/telegram_api_hash not set - pass -api-id/-api-hash, or add them to the config first (get a pair from https://my.telegram.org, one pair covers every account link)")
	}

	path := *sessionPath
	if path == "" {
		path = defaultSessionPath(*phone)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	fmt.Printf("  Logging in %s (session -> %s)...\n", *phone, path)

	client := gotdtg.NewClient(cfg.TelegramAPIID, cfg.TelegramAPIHash, gotdtg.Options{
		SessionStorage: &session.FileStorage{Path: path},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	flow := auth.NewFlow(terminalAuth{phone: *phone, reader: bufio.NewReader(os.Stdin)}, auth.SendCodeOptions{})

	var selfID int64
	runErr := client.Run(ctx, func(rctx context.Context) error {
		if err := client.Auth().IfNecessary(rctx, flow); err != nil {
			return fmt.Errorf("login flow: %w", err)
		}
		me, err := client.Self(rctx)
		if err != nil {
			return fmt.Errorf("get self: %w", err)
		}
		selfID = me.ID
		return nil
	})
	if runErr != nil {
		return runErr
	}

	// Persist api_id/api_hash into the config if this call is what supplied
	// them (via -api-id/-api-hash) - so the tunnel process and any later
	// `vtel account login` don't need them repeated. Preserves every other
	// field already in the file; this command never touches links.
	if err := saveConfigForCLI(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not save telegram_api_id/telegram_api_hash to config: %v\n", err)
	}

	fmt.Printf("\n  Logged in. This account's user ID (give it to the peer side as peer_user_id): %d\n", selfID)
	fmt.Printf("  Session saved to: %s\n", path)
	fmt.Printf("  Now add a link: vtel links add  (kind: account, session: %s, peer_user_id: <the other side's user ID>)\n", path)
	return nil
}

func defaultSessionPath(phone string) string {
	dir := filepath.Dir(cliConfigPath())
	return filepath.Join(dir, "accounts", sanitizePhoneForFilename(phone)+".session")
}

var phoneSanitizeRe = regexp.MustCompile(`[^0-9+]`)

func sanitizePhoneForFilename(phone string) string {
	return phoneSanitizeRe.ReplaceAllString(phone, "")
}

// terminalAuth implements gotd's auth.UserAuthenticator by prompting on
// stdin/stdout - the phone number is already known (passed via -phone), so
// only the login code (always) and the 2FA password (only if the account
// has cloud password enabled) are ever actually prompted for.
type terminalAuth struct {
	phone  string
	reader *bufio.Reader
}

func (t terminalAuth) Phone(context.Context) (string, error) {
	return t.phone, nil
}

func (t terminalAuth) Password(context.Context) (string, error) {
	fmt.Print("  2FA password (this account has cloud password enabled): ")
	pw, _ := t.reader.ReadString('\n')
	pw = strings.TrimSpace(pw)
	if pw == "" {
		return "", auth.ErrPasswordNotProvided
	}
	return pw, nil
}

func (t terminalAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Printf("  Enter the code Telegram just sent to %s: ", t.phone)
	code, _ := t.reader.ReadString('\n')
	return strings.TrimSpace(code), nil
}

func (t terminalAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	fmt.Println("  Accepting Telegram Terms of Service...")
	return nil
}

func (t terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("%s is not a registered Telegram account - vtel account login only logs into an existing real number, it doesn't create a new one", t.phone)
}
