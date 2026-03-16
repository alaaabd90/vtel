package telegram

import (
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
	recvCh    chan []byte // received file data
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

func (p *Poller) Run() {
	errorBackoff := pollerInitialBackoff

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
		errorBackoff = pollerInitialBackoff // reset on success

		// Sort updates by batch sequence (filename) for ordering
		type docUpdate struct {
			updateID int
			fileID   string
			fileName string
		}
		var docs []docUpdate

		for _, u := range updates {
			if u.UpdateID >= p.offset {
				p.offset = u.UpdateID + 1
			}
			cp := u.ChannelPost
			if cp == nil {
				continue
			}
			if cp.Chat.ID != p.channelID {
				continue
			}
			if cp.Document == nil {
				continue
			}
			// Filename format: {peerBotID}_{seq:012d}.bin.gz
			// Filtering by peer bot ID prefix prevents processing our own messages.
			peerPrefix := fmt.Sprintf("%d_", p.peerBotID)
			if !strings.HasPrefix(cp.Document.FileName, peerPrefix) || !strings.HasSuffix(cp.Document.FileName, ".bin.gz") {
				continue
			}
			docs = append(docs, docUpdate{
				updateID: u.UpdateID,
				fileID:   cp.Document.FileID,
				fileName: cp.Document.FileName,
			})
		}

		// Sort by filename (fixed-width sequence number ensures correct lexicographic order)
		sort.Slice(docs, func(i, j int) bool {
			return docs[i].fileName < docs[j].fileName
		})

		for _, d := range docs {
			data, err := p.api.DownloadFile(d.fileID)
			if err != nil {
				fmt.Printf("[poller] download error (%s): %v\n", d.fileName, err)
				continue
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
