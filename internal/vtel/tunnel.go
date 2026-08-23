package vtel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"
)

// tunnel.go wires mux.go + socksserver.go + dial.go together, mirroring
// gdrive's NewTunnel/ServeClient/ServeExit shape with []*bot standing in
// for gdrive's BlobStore.

// shutdownGrace is how long ServeClient keeps its mux's flush/poll loops
// alive after the outer ctx cancels, so a final FIN for any still-open
// stream has a chance to actually go out. A short fixed delay - not
// gdrive's full graceful-drain machinery, just enough for v1.
const shutdownGrace = 2 * time.Second

type Tunnel struct {
	Bots   []*bot
	ChatID int64
	key    []byte
	Logger *log.Logger
}

// BotsFromConfig constructs one *bot per configured token and validates
// each with getMe, failing fast on a bad token rather than discovering it
// later mid-tunnel.
func BotsFromConfig(ctx context.Context, cfg *Config, logger *log.Logger) ([]*bot, error) {
	bots := make([]*bot, 0, len(cfg.Bots))
	for i, token := range cfg.Bots {
		b := newBot(token)
		if err := b.getMe(ctx); err != nil {
			return nil, fmt.Errorf("vtel: bots[%d]: getMe: %w", i, err)
		}
		if logger != nil {
			logger.Printf("vtel: bot @%s (id %d) ready", b.username, b.id)
		}
		bots = append(bots, b)
	}
	return bots, nil
}

func NewTunnel(bots []*bot, cfg *Config) (*Tunnel, error) {
	if len(bots) == 0 {
		return nil, errors.New("vtel: NewTunnel: at least one bot is required")
	}
	key, err := DeriveKey(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("vtel: NewTunnel: %w", err)
	}
	return &Tunnel{Bots: bots, ChatID: cfg.ChatID, key: key}, nil
}

// ServeClient runs the client role: a SOCKS5 listener whose Handler is a
// one-line closure into mux.openClientStream, backed by a mux started on
// an independent background context so its flush/poll loops survive
// shutdownGrace past ctx canceling (long enough to flush a final FIN for
// any stream still open when Serve returns).
func (t *Tunnel) ServeClient(ctx context.Context, listen string) error {
	mux := newVtelMux("client", t.Bots, t.ChatID, t.key, dirClientToExit, dirExitToClient, nil, t.Logger)

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	mux.start(workerCtx)
	defer func() {
		// Give the mux's flush loop shutdownGrace to send a final FIN for
		// any stream still open (runFlushLoop does one last flushNow on
		// workerCtx.Done() - see mux.go) before actually tearing it down.
		time.Sleep(shutdownGrace)
		cancelWorkers()
	}()

	server := &SOCKSServer{
		Listen: listen,
		Logger: t.Logger,
		Handler: func(handlerCtx context.Context, target string, conn net.Conn) {
			if err := mux.openClientStream(handlerCtx, target, conn); err != nil && t.Logger != nil {
				t.Logger.Printf("vtel: openClientStream(%s): %v", target, err)
			}
		},
	}

	return server.Serve(ctx)
}

// ServeExit runs the exit role: no SOCKS server (its "accept" is inbound
// frameOpen frames arriving via the mux's poll loops, handled by
// handleOpenFrame calling dialExitTarget), just start the mux and block
// until ctx cancels.
func (t *Tunnel) ServeExit(ctx context.Context) error {
	mux := newVtelMux("exit", t.Bots, t.ChatID, t.key, dirExitToClient, dirClientToExit, dialExitTarget, t.Logger)
	mux.start(ctx)
	<-ctx.Done()
	mux.closeAll()
	return nil // ctx canceling (SIGINT/SIGTERM, or the caller's own shutdown) is a clean exit, not a failure
}
