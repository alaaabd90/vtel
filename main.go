package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/teltun/teltun/telegram"
	"github.com/teltun/teltun/tunnel"
)

func main() {
	cfg := ParseConfig()

	api := telegram.NewAPI(cfg.Token)

	// Verify bot token
	me, err := api.GetMe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to verify bot token: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[teltun] Bot ID: %d, mode: %s\n", me.ID, cfg.Mode)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	switch cfg.Mode {
	case "client":
		c := tunnel.NewClient(api, me.ID, cfg.PeerBotID, cfg.ChannelID, cfg.ListenAddr)
		go func() {
			<-sigCh
			fmt.Println("\n[teltun] shutting down...")
			c.Stop()
			os.Exit(0)
		}()
		if err := c.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Client error: %v\n", err)
			os.Exit(1)
		}

	case "server":
		s := tunnel.NewServer(api, me.ID, cfg.PeerBotID, cfg.ChannelID)
		go func() {
			<-sigCh
			fmt.Println("\n[teltun] shutting down...")
			s.Stop()
			os.Exit(0)
		}()
		s.Run()
	}
}
