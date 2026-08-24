package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/alaaabd90/vtel/protocol"
	"github.com/alaaabd90/vtel/vtelconfig"
)

func redactToken(tok string) string {
	if len(tok) <= 10 {
		return "****"
	}
	return tok[:6] + "..." + tok[len(tok)-4:]
}

// --- Status view ---

func buildStatusView(s *appState, win fyne.Window) fyne.CanvasObject {
	stateLabel := widget.NewLabel("○ Disconnected")
	stateLabel.TextStyle = fyne.TextStyle{Bold: true}
	summaryLabel := widget.NewLabel("")
	errorLabel := widget.NewLabel("")

	connectBtn := widget.NewButton("Connect", nil)
	disconnectBtn := widget.NewButton("Disconnect", nil)
	disconnectBtn.Disable()

	linkList := widget.NewList(
		func() int { return len(s.linkStatuses()) },
		func() fyne.CanvasObject {
			return container.NewHBox(widget.NewLabel("○"), widget.NewLabel(""), layout.NewSpacer(), widget.NewLabel(""))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			statuses := s.linkStatuses()
			if id >= len(statuses) {
				return
			}
			st := statuses[id]
			row := obj.(*fyne.Container)
			dot := row.Objects[0].(*widget.Label)
			name := row.Objects[1].(*widget.Label)
			load := row.Objects[3].(*widget.Label)
			if st.Healthy {
				dot.SetText("●")
			} else {
				dot.SetText("○")
			}
			name.SetText(fmt.Sprintf("Link %d", st.ID))
			load.SetText(fmt.Sprintf("%d active · %.1f KB/s", st.ActiveStreams, float64(st.BytesPerSec)/1024))
		},
	)

	// refresh mutates widgets and is called both from Fyne's own UI-thread
	// callbacks (OnTapped) and from background goroutines (the ticker
	// below, and connectBtn's "go func(){ s.connect(); refresh() }") -
	// wrapping the whole body in fyne.Do makes it safe to call from either,
	// per Fyne's threading model (calling widget setters off the UI
	// goroutine is unsafe and logs warnings as of Fyne 2.8).
	refresh := func() {
		fyne.Do(func() {
			cfg := s.config()
			if s.isRunning() {
				stateLabel.SetText("● Connected")
				healthy := 0
				statuses := s.linkStatuses()
				for _, st := range statuses {
					if st.Healthy {
						healthy++
					}
				}
				summaryLabel.SetText(fmt.Sprintf("Listening on %s  ·  %d/%d link(s) healthy", cfg.Listen, healthy, len(statuses)))
				connectBtn.Disable()
				disconnectBtn.Enable()
			} else {
				stateLabel.SetText("○ Disconnected")
				summaryLabel.SetText(fmt.Sprintf("%d link(s) configured", len(cfg.Links)))
				connectBtn.Enable()
				disconnectBtn.Disable()
			}
			if msg := s.lastError(); msg != "" {
				errorLabel.SetText("Error: " + msg)
			} else {
				errorLabel.SetText("")
			}
			linkList.Refresh()
		})
	}

	connectBtn.OnTapped = func() {
		connectBtn.Disable()
		go func() {
			_ = s.connect()
			refresh()
		}()
	}
	disconnectBtn.OnTapped = func() {
		s.disconnect()
		refresh()
	}

	copyBtn := widget.NewButton("Copy SOCKS5 address", func() {
		win.Clipboard().SetContent("socks5h://" + s.config().Listen)
	})

	statusCard := widget.NewCard("Connection", "",
		container.NewVBox(stateLabel, summaryLabel, errorLabel, container.NewHBox(connectBtn, disconnectBtn)),
	)
	linksCard := widget.NewCard("Link health", "", container.NewVBox(linkList, copyBtn))
	linksCard.Resize(fyne.NewSize(0, 200))

	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for range tick.C {
			refresh()
		}
	}()

	refresh()
	return container.NewVBox(statusCard, linksCard)
}

// --- Links view ---

func buildLinksView(s *appState, refreshStatus func()) fyne.CanvasObject {
	var list *widget.List
	list = widget.NewList(
		func() int { return len(s.config().Links) },
		func() fyne.CanvasObject {
			return container.NewHBox(widget.NewLabel(""), widget.NewLabel(""), layout.NewSpacer(), widget.NewButton("Remove", nil))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			links := s.config().Links
			if id >= len(links) {
				return
			}
			row := obj.(*fyne.Container)
			idxLabel := row.Objects[0].(*widget.Label)
			info := row.Objects[1].(*widget.Label)
			removeBtn := row.Objects[3].(*widget.Button)
			idxLabel.SetText(fmt.Sprintf("#%d", id))
			l := links[id]
			info.SetText(fmt.Sprintf("%s   peer=%d   channel=%d", redactToken(l.Token), l.PeerBotID, l.ChannelID))
			removeBtn.OnTapped = func() {
				cfg := s.config()
				cfg.Links = append(cfg.Links[:id], cfg.Links[id+1:]...)
				_ = s.setConfig(cfg)
				list.Refresh()
			}
		},
	)

	tokenEntry := widget.NewEntry()
	tokenEntry.SetPlaceHolder("Bot token")
	peerEntry := widget.NewEntry()
	peerEntry.SetPlaceHolder("Peer bot user ID")
	channelEntry := widget.NewEntry()
	channelEntry.SetPlaceHolder("Channel ID")
	statusLabel := widget.NewLabel("")

	addBtn := widget.NewButton("Add link", func() {
		peer, err := strconv.ParseInt(strings.TrimSpace(peerEntry.Text), 10, 64)
		if err != nil {
			statusLabel.SetText("Invalid peer_bot_id: " + err.Error())
			return
		}
		ch, err := strconv.ParseInt(strings.TrimSpace(channelEntry.Text), 10, 64)
		if err != nil {
			statusLabel.SetText("Invalid channel_id: " + err.Error())
			return
		}
		token := strings.TrimSpace(tokenEntry.Text)
		if token == "" {
			statusLabel.SetText("Bot token is required")
			return
		}
		cfg := s.config()
		cfg.Links = append(cfg.Links, vtelconfig.LinkConfig{Token: token, PeerBotID: peer, ChannelID: ch})
		if err := s.setConfig(cfg); err != nil {
			statusLabel.SetText("Error saving: " + err.Error())
			return
		}
		tokenEntry.SetText("")
		peerEntry.SetText("")
		channelEntry.SetText("")
		statusLabel.SetText(fmt.Sprintf("Added link #%d. Reconnect to apply.", len(cfg.Links)-1))
		list.Refresh()
		refreshStatus()
	})

	addCard := widget.NewCard("Add a link", "",
		container.NewVBox(tokenEntry, peerEntry, channelEntry, addBtn, statusLabel),
	)

	return container.NewBorder(nil, container.NewPadded(addCard), nil, nil, list)
}

// --- Settings view ---

func buildSettingsView(s *appState) fyne.CanvasObject {
	cfg := s.config()

	secretEntry := widget.NewPasswordEntry()
	secretEntry.SetText(cfg.Secret)

	compressionSelect := widget.NewSelect([]string{"fastest", "default", "better", "best"}, nil)
	if cfg.CompressionLevel == "" {
		compressionSelect.SetSelected("fastest")
	} else {
		compressionSelect.SetSelected(cfg.CompressionLevel)
	}

	rejectIPv6Check := widget.NewCheck("Reject IPv6 literal targets immediately", nil)
	rejectIPv6Check.SetChecked(cfg.RejectIPv6)

	debugCheck := widget.NewCheck("Verbose debug logging (shown in the Logs tab)", nil)
	debugCheck.SetChecked(cfg.Debug)

	listenEntry := widget.NewEntry()
	listenEntry.SetText(cfg.Listen)

	quietHoursCheck := widget.NewCheck("Enable quiet hours", nil)
	startEntry := widget.NewEntry()
	endEntry := widget.NewEntry()
	tzEntry := widget.NewEntry()
	tzEntry.SetPlaceHolder("IANA timezone, e.g. UTC")
	if cfg.QuietHours != nil {
		quietHoursCheck.SetChecked(true)
		startEntry.SetText(strconv.Itoa(cfg.QuietHours.StartHour))
		endEntry.SetText(strconv.Itoa(cfg.QuietHours.EndHour))
		tzEntry.SetText(cfg.QuietHours.Timezone)
	}

	statusLabel := widget.NewLabel("")

	saveBtn := widget.NewButton("Save settings", func() {
		cfg := s.config()
		cfg.Secret = secretEntry.Text
		cfg.CompressionLevel = compressionSelect.Selected
		cfg.RejectIPv6 = rejectIPv6Check.Checked
		cfg.Debug = debugCheck.Checked
		cfg.Listen = strings.TrimSpace(listenEntry.Text)
		if quietHoursCheck.Checked {
			start, err1 := strconv.Atoi(strings.TrimSpace(startEntry.Text))
			end, err2 := strconv.Atoi(strings.TrimSpace(endEntry.Text))
			if err1 != nil || err2 != nil {
				statusLabel.SetText("Quiet hours: start/end must be numbers 0-23")
				return
			}
			cfg.QuietHours = &protocol.QuietHoursConfig{StartHour: start, EndHour: end, Timezone: strings.TrimSpace(tzEntry.Text)}
		} else {
			cfg.QuietHours = nil
		}
		if err := vtelconfig.Validate(&cfg); err != nil {
			statusLabel.SetText("Error: " + err.Error())
			return
		}
		if err := s.setConfig(cfg); err != nil {
			statusLabel.SetText("Error saving: " + err.Error())
			return
		}
		statusLabel.SetText("Saved. Reconnect to apply.")
	})

	return container.NewPadded(container.NewVBox(
		widget.NewCard("Secret", "Must match the peer side exactly.", secretEntry),
		widget.NewCard("Listen address", "SOCKS5 address this app listens on.", listenEntry),
		widget.NewCard("Compression level", "", compressionSelect),
		widget.NewCard("IPv6", "", rejectIPv6Check),
		widget.NewCard("Debug logging", "Traces mux frames, pool link picks, and batch flushes. Off by default; turn off once you've found what you need.", debugCheck),
		widget.NewCard("Quiet hours", "Widen the flush cadence during a daily window instead of pausing.",
			container.NewVBox(quietHoursCheck,
				container.NewGridWithColumns(3,
					widget.NewLabel("Start hour"), widget.NewLabel("End hour"), widget.NewLabel("Timezone"),
				),
				container.NewGridWithColumns(3, startEntry, endEntry, tzEntry),
			),
		),
		saveBtn, statusLabel,
	))
}

// --- Import view ---

func buildImportView(s *appState, refreshAll func()) fyne.CanvasObject {
	configEntry := widget.NewMultiLineEntry()
	configEntry.SetPlaceHolder("Paste a full vtel config.json here…")
	configEntry.SetMinRowsVisible(12)
	statusLabel := widget.NewLabel("")

	importBtn := widget.NewButton("Import config", func() {
		text := strings.TrimSpace(configEntry.Text)
		if text == "" {
			statusLabel.SetText("Error: paste a config first")
			return
		}
		var cfg vtelconfig.Config
		if err := json.Unmarshal([]byte(text), &cfg); err != nil {
			statusLabel.SetText("Error: " + err.Error())
			return
		}
		if err := vtelconfig.Validate(&cfg); err != nil {
			statusLabel.SetText("Error: " + err.Error())
			return
		}
		if err := s.setConfig(cfg); err != nil {
			statusLabel.SetText("Error saving: " + err.Error())
			return
		}
		statusLabel.SetText(fmt.Sprintf("Imported %d link(s). Reconnect to apply.", len(cfg.Links)))
		configEntry.SetText("")
		refreshAll()
	})

	return container.NewPadded(
		widget.NewCard("Import config", "Paste a complete vtel config.json (e.g. copied from the server side).",
			container.NewVBox(configEntry, importBtn, statusLabel),
		),
	)
}

// --- Logs view ---

func buildLogsView(s *appState) fyne.CanvasObject {
	var logList *widget.List
	logList = widget.NewList(
		func() int { return len(s.logBuf.snapshot()) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Wrapping = fyne.TextTruncate
			return l
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			lines := s.logBuf.snapshot()
			if id < len(lines) {
				obj.(*widget.Label).SetText(lines[id])
			}
		},
	)

	// onChange fires from logBuffer.Write, which can be called from any
	// goroutine (the stdout-capture reader in main.go, or vtel's own
	// internal goroutines logging through the redirected stdout) - wrap in
	// fyne.Do for the same reason as buildStatusView's refresh.
	s.logBuf.onChange = func() {
		fyne.Do(func() {
			logList.Refresh()
			lines := s.logBuf.snapshot()
			if n := len(lines); n > 0 {
				logList.ScrollTo(widget.ListItemID(n - 1))
			}
		})
	}

	clearBtn := widget.NewButton("Clear logs", func() { s.logBuf.clear() })
	return container.NewBorder(nil, clearBtn, nil, nil, logList)
}

// --- top-level window ---

func buildUI(a fyne.App, s *appState) fyne.Window {
	win := a.NewWindow("vtel")
	win.Resize(fyne.NewSize(900, 620))

	statusView := buildStatusView(s, win)
	logsView := buildLogsView(s)

	content := container.New(layout.NewMaxLayout(), statusView)
	setView := func(v fyne.CanvasObject) {
		content.Objects[0] = v
		content.Refresh()
	}

	var linksView, importView fyne.CanvasObject
	refreshAll := func() {
		linksView = buildLinksView(s, func() {})
		importView = buildImportView(s, func() {})
	}
	refreshAll()

	titleLabel := widget.NewLabelWithStyle("vtel", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	navBtns := []*widget.Button{
		widget.NewButton("Status", func() { setView(statusView) }),
		widget.NewButton("Links", func() { refreshAll(); setView(linksView) }),
		widget.NewButton("Settings", func() { setView(buildSettingsView(s)) }),
		widget.NewButton("Import", func() { refreshAll(); setView(importView) }),
		widget.NewButton("Logs", func() { setView(logsView) }),
	}

	sidebar := container.NewVBox(container.NewPadded(titleLabel), widget.NewSeparator())
	for _, btn := range navBtns {
		btn.Alignment = widget.ButtonAlignLeading
		sidebar.Add(btn)
	}

	win.SetContent(container.NewBorder(nil, nil, sidebar, nil, content))
	win.SetCloseIntercept(func() {
		s.disconnect()
		win.Close()
	})
	return win
}
