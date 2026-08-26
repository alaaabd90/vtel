package telegram

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	gotdtg "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// connectTimeout bounds how long NewAccountAPI waits for the initial MTProto
// connection + auth check before giving up. The background connection
// itself (client.Run) keeps running for the AccountAPI's whole lifetime,
// independent of this - it's only the constructor's synchronous "are we
// connected and logged in" wait that's bounded.
const connectTimeout = 30 * time.Second

// AccountAPI implements the API interface over MTProto using a real,
// logged-in Telegram user account (session file produced by `vtel account
// login`) instead of a bot token. One AccountAPI is scoped to exactly one
// link: one MTProto connection/session watching one channel for posts from
// one expected peer, mirroring BotAPI's one-bot-one-link shape.
//
// Unlike BotAPI's offset-based long-polling (a real Bot API primitive),
// MTProto pushes updates to a registered handler as they arrive. GetUpdates
// fakes the same offset/timeout polling shape on top of that push stream
// (see inbox/notifyCh below) purely so Poller.Run (poller.go) - written
// against the Bot API's polling contract - needs zero changes to work with
// either transport.
type AccountAPI struct {
	client *gotdtg.Client
	api    *tg.Client
	ctx    context.Context
	stop   context.CancelFunc

	channelID  int64 // raw MTProto channel ID (not the Bot-API "-100..." form)
	peerUserID int64 // expected sender's real user ID; 0 = accept from anyone in the channel
	selfID     int64

	mu       sync.Mutex
	inbox    []Update
	notifyCh chan struct{}

	docMu    sync.Mutex
	docCache map[string][]byte // synthetic FileID -> bytes, populated eagerly as documents arrive

	peerOnce sync.Once
	peer     tg.InputPeerClass
	peerErr  error
}

// rawChannelID converts a Bot-API-style channel/supergroup chat ID
// (e.g. -1001234567890) to the raw internal channel ID MTProto types use
// (1234567890). vtel's config keeps one channel_id field/format shared by
// both bot and account link kinds - vtelconfig.Validate already requires
// the Bot-API "-100..." form, so account links reuse it rather than adding
// a second channel ID format to the config.
func rawChannelID(botAPIChannelID int64) int64 {
	return -botAPIChannelID - 1000000000000
}

// NewAccountAPI loads the session at sessionPath (written by `vtel account
// login`) and connects over MTProto. Fails fast if the session isn't
// authorized yet - logging in is a separate, interactive, one-time step,
// not something a tunnel process can do on its own.
func NewAccountAPI(apiID int, apiHash, sessionPath string, channelID, peerUserID int64) (*AccountAPI, error) {
	a := &AccountAPI{
		channelID:  rawChannelID(channelID),
		peerUserID: peerUserID,
		notifyCh:   make(chan struct{}),
		docCache:   make(map[string][]byte),
	}

	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewChannelMessage(a.onNewChannelMessage)

	client := gotdtg.NewClient(apiID, apiHash, gotdtg.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		UpdateHandler:  dispatcher,
	})

	runCtx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	go func() {
		_ = client.Run(runCtx, func(rctx context.Context) error {
			status, err := client.Auth().Status(rctx)
			if err != nil {
				err = fmt.Errorf("check auth status: %w", err)
				ready <- err
				return err
			}
			if !status.Authorized {
				err := fmt.Errorf("account session %q is not logged in - run `vtel account login` first", sessionPath)
				ready <- err
				return err
			}
			a.selfID = status.User.ID
			ready <- nil
			<-rctx.Done()
			return rctx.Err()
		})
	}()

	connectCtx, connectCancel := context.WithTimeout(context.Background(), connectTimeout)
	defer connectCancel()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			return nil, err
		}
	case <-connectCtx.Done():
		cancel()
		return nil, fmt.Errorf("connect to Telegram: timed out after %s", connectTimeout)
	}

	a.client = client
	a.api = client.API()
	a.ctx = runCtx
	a.stop = cancel
	return a, nil
}

// Close tears down the background MTProto connection. Not part of the API
// interface (BotAPI has no persistent connection to close either) - useful
// for tests and any future graceful-shutdown path.
func (a *AccountAPI) Close() {
	a.stop()
}

func (a *AccountAPI) push(u Update) {
	a.mu.Lock()
	a.inbox = append(a.inbox, u)
	old := a.notifyCh
	a.notifyCh = make(chan struct{})
	a.mu.Unlock()
	close(old)
}

// GetUpdates fakes Bot API's offset/timeout long-poll contract over the
// push-based inbox onNewChannelMessage fills in. Stale entries (UpdateID <
// offset) are dropped from the inbox as a side effect of every call, so it
// never grows unbounded across the life of the connection.
func (a *AccountAPI) GetUpdates(offset int, timeout int) ([]Update, error) {
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		a.mu.Lock()
		var ready []Update
		for _, u := range a.inbox {
			if u.UpdateID >= offset {
				ready = append(ready, u)
			}
		}
		a.inbox = ready
		ch := a.notifyCh
		a.mu.Unlock()

		if len(ready) > 0 {
			return ready, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			timer.Stop()
			continue
		case <-timer.C:
			return nil, nil
		case <-a.ctx.Done():
			timer.Stop()
			return nil, a.ctx.Err()
		}
	}
}

// DownloadFile looks up bytes cached by onNewChannelMessage - the document
// was already downloaded synchronously when the update arrived (see the
// doc comment on AccountAPI), so this is a map lookup, not a second network
// round trip like BotAPI.DownloadFile's live getFile+download.
func (a *AccountAPI) DownloadFile(fileID string) ([]byte, error) {
	a.docMu.Lock()
	data, ok := a.docCache[fileID]
	delete(a.docCache, fileID)
	a.docMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("account transport: no cached document for fileID %q (already consumed, or this AccountAPI restarted since it arrived)", fileID)
	}
	return data, nil
}

func (a *AccountAPI) GetMe() (*User, error) {
	return &User{ID: a.selfID, IsBot: false}, nil
}

// WarmConnection mirrors BotAPI.WarmConnection's role for pool.Pool's
// periodic health probe.
func (a *AccountAPI) WarmConnection() error {
	_, err := a.client.Self(a.ctx)
	return err
}

func (a *AccountAPI) SendMessage(channelID int64, text string) (retryAfter int, err error) {
	peer, err := a.resolvePeer()
	if err != nil {
		return 0, err
	}
	randID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return 0, err
	}
	_, err = a.api.MessagesSendMessage(a.ctx, &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  text,
		RandomID: randID,
	})
	return floodRetryAfter(err)
}

func (a *AccountAPI) SendDocument(channelID int64, filename string, data []byte) (retryAfter int, err error) {
	if len(data) > MaxSendDocumentSize {
		return 0, fmt.Errorf("document size %d exceeds Telegram's document size limit", len(data))
	}
	peer, err := a.resolvePeer()
	if err != nil {
		return 0, err
	}

	up := uploader.NewUploader(a.api)
	file, err := up.Upload(a.ctx, uploader.NewUpload(filename, bytes.NewReader(data), int64(len(data))))
	if err != nil {
		return floodRetryAfter(err)
	}

	randID, err := crypto.RandInt64(crypto.DefaultRand())
	if err != nil {
		return 0, err
	}
	_, err = a.api.MessagesSendMedia(a.ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		RandomID: randID,
		Media: &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: "application/octet-stream",
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: filename},
			},
		},
	})
	return floodRetryAfter(err)
}

// resolvePeer finds this link's channel among the account's dialogs to get
// the access hash MTProto requires alongside the channel ID (Telegram
// channel IDs alone aren't enough to address a channel over MTProto, unlike
// the Bot API). Scans only the first page of dialogs (100) - fine for a
// small dedicated test/tunnel account, not a general-purpose solution for
// an account with hundreds of chats.
func (a *AccountAPI) resolvePeer() (tg.InputPeerClass, error) {
	a.peerOnce.Do(func() {
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100}
		resp, err := a.api.MessagesGetDialogs(a.ctx, req)
		if err != nil {
			a.peerErr = fmt.Errorf("get dialogs: %w", err)
			return
		}
		modified, ok := resp.AsModified()
		if !ok {
			a.peerErr = fmt.Errorf("get dialogs: unexpected response type %T", resp)
			return
		}
		for _, c := range modified.GetChats() {
			if ch, ok := c.(*tg.Channel); ok && ch.ID == a.channelID {
				a.peer = &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
				return
			}
		}
		a.peerErr = fmt.Errorf("channel (raw id %d) not found among this account's first %d dialogs - is this account a member of the shared channel?", a.channelID, req.Limit)
	})
	return a.peer, a.peerErr
}

func (a *AccountAPI) onNewChannelMessage(ctx context.Context, _ tg.Entities, u *tg.UpdateNewChannelMessage) error {
	msg, ok := u.Message.(*tg.Message)
	if !ok {
		return nil // service message or similar, not a batch we sent
	}
	peerCh, ok := msg.PeerID.(*tg.PeerChannel)
	if !ok || peerCh.ChannelID != a.channelID {
		return nil
	}
	if a.peerUserID != 0 {
		fromUser, ok := msg.FromID.(*tg.PeerUser)
		if !ok || fromUser.UserID != a.peerUserID {
			return nil
		}
	}

	cp := &ChannelPost{
		MessageID: msg.ID,
		Chat:      Chat{ID: -a.channelID - 1000000000000},
		Text:      msg.Message,
	}
	if a.peerUserID != 0 {
		cp.From = &User{ID: a.peerUserID}
	}

	if mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument); ok {
		if doc, ok := mediaDoc.Document.(*tg.Document); ok {
			data, err := a.downloadDocument(ctx, doc)
			if err != nil {
				fmt.Printf("[account] download document (msg %d): %v\n", msg.ID, err)
				return nil // same "skip, don't crash the poll loop" behavior poller.go already relies on
			}
			fileID := fmt.Sprintf("acct:%d:%d", a.channelID, msg.ID)
			a.docMu.Lock()
			a.docCache[fileID] = data
			a.docMu.Unlock()
			cp.Document = &Document{
				FileID:   fileID,
				FileName: documentFilename(doc),
				FileSize: int(doc.Size),
			}
		}
	}

	a.push(Update{UpdateID: msg.ID, ChannelPost: cp})
	return nil
}

func (a *AccountAPI) downloadDocument(ctx context.Context, doc *tg.Document) ([]byte, error) {
	loc := &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}
	var buf bytes.Buffer
	if _, err := downloader.NewDownloader().Download(a.api, loc).Stream(ctx, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func documentFilename(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if fn, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return fn.FileName
		}
	}
	return ""
}

// floodRetryAfter maps gotd's FLOOD_WAIT_X errors to the same (retryAfter,
// err) shape BotAPI's methods return on Bot API 429s, so Sender.sendRetry
// (sender.go) - written against that shape - needs no changes to retry
// either transport correctly.
func floodRetryAfter(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if d, ok := gotdtg.AsFloodWait(err); ok {
		return int(d / time.Second), err
	}
	return 0, err
}
