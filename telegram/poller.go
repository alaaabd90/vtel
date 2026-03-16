package telegram

import (
	"fmt"
	"sort"
	"strings"
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
		recvCh:    make(chan []byte, 64),
		done:      make(chan struct{}),
	}
}

func (p *Poller) RecvChan() <-chan []byte {
	return p.recvCh
}

func (p *Poller) Run() {
	for {
		select {
		case <-p.done:
			return
		default:
		}

		updates, err := p.api.GetUpdates(p.offset, 30)
		if err != nil {
			fmt.Printf("[poller] getUpdates error: %v\n", err)
			continue
		}

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
			// In channels, messages from bots have sender_chat set to the channel.
			// We identify peer bot messages by: the message has a document with our naming convention
			// AND it wasn't sent by us (we don't process our own messages).
			// Since both bots post to the same channel, we need to filter.
			// Bot API: in channels, "from" field may or may not be present.
			// We use a simple approach: if the message has a document with our filename pattern, process it.
			// The peer bot sends with a specific prefix to identify itself.
			if cp.Document == nil {
				continue
			}
			// Filename format: b_<seq>.bin.gz
			if !strings.HasPrefix(cp.Document.FileName, "b_") || !strings.HasSuffix(cp.Document.FileName, ".bin.gz") {
				continue
			}
			docs = append(docs, docUpdate{
				updateID: u.UpdateID,
				fileID:   cp.Document.FileID,
				fileName: cp.Document.FileName,
			})
		}

		// Sort by filename (which encodes sequence number) to ensure ordering
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
