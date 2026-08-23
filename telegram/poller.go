package telegram

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	pollerInitialBackoff = 1 * time.Second
	pollerMaxBackoff     = 30 * time.Second
)

// Poller long-polls for channel_post updates from the peer bot.
type Poller struct {
	api       *API
	peerBotID int64
	channelID int64
	offset    int
	recvCh    chan []byte
	done      chan struct{}
}

func NewPoller(api *API, peerBotID int64, channelID int64) *Poller {
	return &Poller{
		api:       api,
		peerBotID: peerBotID,
		channelID: channelID,
		recvCh:    make(chan []byte, 128),
		done:      make(chan struct{}),
	}
}

func (p *Poller) RecvChan() <-chan []byte {
	return p.recvCh
}

// batchItem holds a received batch, either already decoded (text msg) or
// pending download (document).
type batchItem struct {
	sortKey string // "{peerBotID}_{seq:012d}" — used for ordering
	data    []byte // non-nil for text messages (already decoded)
	fileID  string // non-empty for documents (needs download)
}

func (p *Poller) Run() {
	errorBackoff := pollerInitialBackoff
	peerPrefix := fmt.Sprintf("%d_", p.peerBotID)

	for {
		select {
		case <-p.done:
			return
		default:
		}

		updates, err := p.api.GetUpdates(p.offset, 30)
		if err != nil {
			fmt.Printf("[poller] getUpdates error: %v (retry in %v)\n", err, errorBackoff)
			select {
			case <-time.After(errorBackoff):
			case <-p.done:
				return
			}
			errorBackoff *= 2
			if errorBackoff > pollerMaxBackoff {
				errorBackoff = pollerMaxBackoff
			}
			continue
		}
		errorBackoff = pollerInitialBackoff

		var items []batchItem

		for _, u := range updates {
			if u.UpdateID >= p.offset {
				p.offset = u.UpdateID + 1
			}
			cp := u.ChannelPost
			if cp == nil || cp.Chat.ID != p.channelID {
				continue
			}

			// Text message: "{peerBotID}_{seq:012d}\n{base64data}"
			if cp.Text != "" && strings.HasPrefix(cp.Text, peerPrefix) {
				parts := strings.SplitN(cp.Text, "\n", 2)
				if len(parts) != 2 {
					continue
				}
				decoded, err := base64.StdEncoding.DecodeString(parts[1])
				if err != nil {
					fmt.Printf("[poller] base64 decode error: %v\n", err)
					continue
				}
				items = append(items, batchItem{sortKey: parts[0], data: decoded})
				continue
			}

			// Document: "{peerBotID}_{seq:012d}.bin.zst"
			if cp.Document != nil &&
				strings.HasPrefix(cp.Document.FileName, peerPrefix) &&
				strings.HasSuffix(cp.Document.FileName, ".bin.zst") {
				sortKey := strings.TrimSuffix(cp.Document.FileName, ".bin.zst")
				items = append(items, batchItem{sortKey: sortKey, fileID: cp.Document.FileID})
			}
		}

		// Sort by sort key — fixed-width zero-padded seq ensures correct order
		// across both text and document batches.
		sort.Slice(items, func(i, j int) bool {
			return items[i].sortKey < items[j].sortKey
		})

		for _, item := range items {
			var data []byte
			if item.data != nil {
				data = item.data
			} else {
				data, err = p.api.DownloadFile(item.fileID)
				if err != nil {
					fmt.Printf("[poller] download error (%s): %v\n", item.sortKey, err)
					continue
				}
			}
			select {
			case p.recvCh <- data:
			case <-p.done:
				return
			}
		}
	}
}

func (p *Poller) Stop() {
	close(p.done)
}
