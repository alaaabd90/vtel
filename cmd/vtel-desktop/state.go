package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alaaabd90/vtel/tunnel"
	"github.com/alaaabd90/vtel/vtelconfig"
)

// configDir resolves a per-user config directory the same way on Windows
// and Linux (os.UserConfigDir: %AppData%\vtel on Windows, ~/.config/vtel on
// Linux) - unlike cmd/vtel's CLI, which targets a fixed VPS path
// (/root/vtel), this is a portable desktop app with no root/systemd
// assumptions.
func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir, _ = os.UserHomeDir()
	}
	return filepath.Join(dir, "vtel")
}

func configPath() string {
	return filepath.Join(configDir(), "config.json")
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// logBuffer captures log lines for the Logs view. Also used as the target
// of a redirected os.Stdout (see main.go) so vtel's own internal fmt.Printf
// logging - which has no pluggable logger interface - shows up here without
// any changes to vtel's core packages.
type logBuffer struct {
	mu       sync.Mutex
	lines    []string
	onChange func()
}

func (b *logBuffer) Write(p []byte) (n int, err error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		b.mu.Lock()
		b.lines = append(b.lines, line)
		if len(b.lines) > 2000 {
			b.lines = b.lines[len(b.lines)-2000:]
		}
		cb := b.onChange
		b.mu.Unlock()
		if cb != nil {
			cb()
		}
	}
	return len(p), nil
}

func (b *logBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

func (b *logBuffer) clear() {
	b.mu.Lock()
	b.lines = b.lines[:0]
	cb := b.onChange
	b.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// appState holds the whole application's runtime state: the loaded config,
// the (possibly nil, when disconnected) running tunnel.Client, and the
// captured log buffer.
type appState struct {
	logBuf *logBuffer
	logger *log.Logger

	mu      sync.Mutex
	cfg     vtelconfig.Config
	client  *tunnel.Client
	running bool
	lastErr string
}

func newAppState() *appState {
	buf := &logBuffer{}
	return &appState{
		logBuf: buf,
		logger: log.New(buf, "[vtel-desktop] ", log.LstdFlags|log.Lmsgprefix),
	}
}

// loadOrInitConfig reads the config file, creating a fresh client-mode
// skeleton (with a random secret and no links) if none exists yet -
// mirroring install.sh's own "write a skeleton, tell the user to add
// links" behavior for the VPS CLI.
func (s *appState) loadOrInitConfig() error {
	path := configPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		secret, genErr := randomSecret()
		if genErr != nil {
			return genErr
		}
		cfg := vtelconfig.Config{
			Mode:             "client",
			Listen:           "127.0.0.1:1080",
			Secret:           secret,
			CompressionLevel: "fastest",
			Links:            []vtelconfig.LinkConfig{},
		}
		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		return s.saveConfig()
	}
	if err != nil {
		return err
	}
	var cfg vtelconfig.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

func (s *appState) config() vtelconfig.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *appState) setConfig(cfg vtelconfig.Config) error {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return s.saveConfig()
}

func (s *appState) saveConfig() error {
	cfg := s.config()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0600)
}

func (s *appState) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *appState) lastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// connect verifies every link's bot token (live GetMe calls, see
// vtelconfig.BuildLinkSpecs), starts the tunnel client, and runs it in the
// background. Always operates in client mode - vtel-desktop, like gdrive's
// own desktop app, is a client-role app; the VPS/server side is managed by
// the vtel CLI (see cmd/vtel).
func (s *appState) connect() error {
	if s.isRunning() {
		return nil
	}
	cfg := s.config()
	cfg.Mode = "client"
	if err := vtelconfig.Validate(&cfg); err != nil {
		s.setLastError(err.Error())
		return err
	}

	specs, err := vtelconfig.BuildLinkSpecs(&cfg, func(i int, botID int64, err error) {
		if err != nil {
			s.logger.Printf("link %d: verify failed: %v", i, err)
			return
		}
		s.logger.Printf("link %d: bot ID %d verified", i, botID)
	})
	if err != nil {
		s.setLastError(err.Error())
		return err
	}

	client := tunnel.NewClient(specs, cfg.Listen, cfg.RejectIPv6)

	s.mu.Lock()
	s.client = client
	s.running = true
	s.lastErr = ""
	s.mu.Unlock()

	go func() {
		if err := client.Run(); err != nil {
			s.logger.Printf("client exited: %v", err)
			s.mu.Lock()
			s.running = false
			s.lastErr = err.Error()
			s.mu.Unlock()
		}
	}()
	return nil
}

func (s *appState) disconnect() {
	s.mu.Lock()
	client := s.client
	s.running = false
	s.client = nil
	s.mu.Unlock()
	if client != nil {
		client.Stop()
	}
}

func (s *appState) setLastError(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

// linkStatuses returns the current per-link health snapshot, or nil when
// disconnected.
func (s *appState) linkStatuses() []tunnel.LinkStatus {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.LinkStatuses()
}
