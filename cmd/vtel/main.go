package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/telegram"
	"github.com/alaaabd90/vtel/tunnel"
)

func main() {
	cfg := ParseConfig()

	key, err := protocol.DeriveKey(cfg.Secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid secret: %v\n", err)
		os.Exit(1)
	}
	level, err := protocol.ParseCompressionLevel(cfg.CompressionLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid compression_level: %v\n", err)
		os.Exit(1)
	}

	specs := make([]tunnel.LinkSpec, 0, len(cfg.Links))
	for i, lc := range cfg.Links {
		api := telegram.NewAPI(lc.Token)
		me, err := api.GetMe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to verify bot token for link %d: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Printf("[vtel] link %d: bot ID %d\n", i, me.ID)
		specs = append(specs, tunnel.LinkSpec{
			ID:               i,
			API:              api,
			BotID:            me.ID,
			PeerBotID:        lc.PeerBotID,
			ChannelID:        lc.ChannelID,
			Key:              key,
			CompressionLevel: level,
		})
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
