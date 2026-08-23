// Package faketelegram is a minimal in-memory stand-in for the Telegram Bot
// API - real HTTP, real multipart parsing, real JSON, just not the real
// api.telegram.org. Every bot token's getUpdates/getFile/download hits the
// same shared post list, mirroring "every bot with access to a channel sees
// every post", which is exactly the scenario vtel's frame-ordering/dedup
// logic needs to be correct against. Used by cmd/smoketest and
// cmd/vtel-bench so neither needs live bot tokens or network access.
package faketelegram

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alaaabd90/vtel/telegram"
)

// Server is a fake Telegram Bot API. All bots share one post list scoped to
// a single ChannelID (see ChannelID).
type Server struct {
	// ChannelID is the fake channel every posted message/document is
	// stamped with.
	ChannelID int64

	mu      sync.Mutex
	nextID  int
	posts   []post
	files   map[string][]byte
	botIDs  map[string]int64
	nextBot int64
}

type post struct {
	updateID int
	fileName string
	fileID   string
	text     string
}

// New creates a Server with the given fake channel ID.
func New(channelID int64) *Server {
	return &Server{
		ChannelID: channelID,
		files:     make(map[string][]byte),
		botIDs:    make(map[string]int64),
		nextBot:   1000,
	}
}

// Start wraps Server's handler in an httptest.Server. Callers must Close it.
func (f *Server) Start() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(f.handler))
}

func (f *Server) botID(token string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.botIDs[token]; ok {
		return id
	}
	f.nextBot++
	f.botIDs[token] = f.nextBot
	return f.nextBot
}

func (f *Server) addDocument(fileName string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fileID := fmt.Sprintf("file%d", f.nextID)
	f.files[fileID] = data
	f.posts = append(f.posts, post{updateID: f.nextID, fileName: fileName, fileID: fileID})
	f.nextID++
}

func (f *Server) addText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, post{updateID: f.nextID, text: text})
	f.nextID++
}

func (f *Server) since(offset int) []post {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []post
	for _, p := range f.posts {
		if p.updateID >= offset {
			out = append(out, p)
		}
	}
	return out
}

func (f *Server) file(fileID string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[fileID]
	return data, ok
}

func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// handler routes fake Telegram Bot API requests, matching the real path
// shapes: /bot<token>/<method> and /file/bot<token>/<fileID>.
func (f *Server) handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/file/bot") {
		rest := strings.TrimPrefix(path, "/file/bot")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		data, ok := f.file(parts[1])
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
		return
	}

	if !strings.HasPrefix(path, "/bot") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, "/bot")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	token, method := parts[0], parts[1]

	switch method {
	case "getMe":
		writeOK(w, telegram.User{ID: f.botID(token), IsBot: true})

	case "sendMessage":
		var payload struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.addText(payload.Text)
		writeOK(w, map[string]any{"message_id": 1})

	case "sendDocument":
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var filename string
		var data []byte
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if part.FormName() == "document" {
				filename = part.FileName()
				data, _ = io.ReadAll(part)
			}
		}
		f.addDocument(filename, data)
		writeOK(w, map[string]any{"message_id": 1})

	case "getUpdates":
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		posts := f.since(offset)
		if len(posts) == 0 {
			// Loosely mirror long-poll latency without actually blocking
			// for the full 30s timeout every real getUpdates call would.
			time.Sleep(20 * time.Millisecond)
		}
		updates := make([]telegram.Update, 0, len(posts))
		for _, p := range posts {
			cp := &telegram.ChannelPost{
				MessageID: p.updateID,
				Chat:      telegram.Chat{ID: f.ChannelID},
			}
			if p.text != "" {
				cp.Text = p.text
			} else {
				cp.Document = &telegram.Document{FileID: p.fileID, FileName: p.fileName}
			}
			updates = append(updates, telegram.Update{UpdateID: p.updateID, ChannelPost: cp})
		}
		writeOK(w, updates)

	case "getFile":
		fileID := r.URL.Query().Get("file_id")
		writeOK(w, map[string]any{"file_path": fileID})

	default:
		http.NotFound(w, r)
	}
}
