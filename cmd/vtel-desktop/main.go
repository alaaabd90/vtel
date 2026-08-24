// Command vtel-desktop is a Fyne GUI client for vtel, mirroring the shape
// of gdrive's own desktop app (cmd/gkdrive-desktop in the sibling
// project) - a client-role app; the server/VPS side is managed by the
// vtel CLI (cmd/vtel). Unlike gkdrive-desktop, there's no multi-profile
// load balancer here: a vtel Client already balances across every
// configured link internally, so one config == one running client.
package main

import (
	"fmt"
	"os"

	"fyne.io/fyne/v2/app"
)

func main() {
	s := newAppState()

	// vtel's own packages log via plain fmt.Printf to os.Stdout - there's
	// no pluggable logger interface to hook into instead. Redirecting
	// os.Stdout itself (before anything runs) captures all of that into
	// the Logs view for free, without touching any core package.
	r, w, err := os.Pipe()
	if err == nil {
		os.Stdout = w
		go func() {
			buf := make([]byte, 4096)
			for {
				n, rerr := r.Read(buf)
				if n > 0 {
					_, _ = s.logBuf.Write(buf[:n])
				}
				if rerr != nil {
					return
				}
			}
		}()
	} else {
		fmt.Fprintf(os.Stderr, "warning: could not redirect stdout for log capture: %v\n", err)
	}

	if err := s.loadOrInitConfig(); err != nil {
		s.logger.Printf("load config: %v", err)
	} else {
		s.logger.Printf("config loaded from %s (%d link(s))", configPath(), len(s.config().Links))
	}

	a := app.NewWithID("io.vtel.desktop")
	win := buildUI(a, s)
	win.ShowAndRun()
}
