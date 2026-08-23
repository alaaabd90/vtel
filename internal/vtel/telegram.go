package vtel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"
)

// bot wraps one Telegram Bot API token. Each lane in the pool is one bot;
// see mux.go for how lanes are picked on send and fanned out on receive.
type bot struct {
	token  string
	client *http.Client
	// id is filled in by getMe on startup; used only for logging, since
	// direction is carried in the frame payload, not inferred from which
	// bot's token sent it (both client and exit hold every bot's token).
	id       int64
	username string
}

func newBot(token string) *bot {
	return &bot{token: token, client: &http.Client{Timeout: 60 * time.Second}}
}

func (b *bot) apiURL(method string) string {
	return "https://api.telegram.org/bot" + b.token + "/" + method
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type tgAPIError struct {
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

func (e *tgAPIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.ErrorCode, e.Description)
}

type tgResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

func (b *bot) getMe(ctx context.Context) error {
	var out tgResponse[tgUser]
	if err := b.callJSON(ctx, "getMe", nil, &out); err != nil {
		return err
	}
	b.id = out.Result.ID
	b.username = out.Result.Username
	return nil
}

// callJSON performs a JSON POST for methods that don't need multipart
// (everything except sendDocument/getFile's download step).
func (b *bot) callJSON(ctx context.Context, method string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL(method), reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	return checkOK(out)
}

// checkOK extracts ok/description/error_code from a tgResponse[T] via a
// tiny local interface, so callJSON stays generic without reflection.
type okChecker interface {
	isOK() (bool, string, int)
}

func (r tgResponse[T]) isOK() (bool, string, int) { return r.OK, r.Description, r.ErrorCode }

func checkOK(out any) error {
	oc, ok := out.(okChecker)
	if !ok {
		return nil
	}
	if okVal, desc, code := oc.isOK(); !okVal {
		return &tgAPIError{Description: desc, ErrorCode: code}
	}
	return nil
}

// sendDocument uploads data as a document to chatID. filename is cosmetic;
// receivers identify vtel traffic by the batch magic header inside the
// (gzip-compressed) bytes, not by filename.
func (b *bot) sendDocument(ctx context.Context, chatID int64, filename string, data []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if err := w.WriteField("disable_notification", "true"); err != nil {
		return err
	}
	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL("sendDocument"), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var out tgResponse[json.RawMessage]
	if err := json.Unmarshal(respData, &out); err != nil {
		return fmt.Errorf("telegram sendDocument: decode response: %w", err)
	}
	return checkOK(out)
}

type tgUpdate struct {
	UpdateID    int64        `json:"update_id"`
	ChannelPost *tgMessage   `json:"channel_post,omitempty"`
	Message     *tgMessage   `json:"message,omitempty"`
}

type tgMessage struct {
	MessageID int64       `json:"message_id"`
	Chat      tgChat      `json:"chat"`
	From      *tgUser     `json:"from,omitempty"`
	Document  *tgDocument `json:"document,omitempty"`
	Date      int64       `json:"date"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgDocument struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	FileName     string `json:"file_name"`
}

// getUpdates long-polls with a 30s server-side timeout. offset must be the
// last seen update_id + 1; callers own persisting it across calls.
func (b *bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	body := map[string]any{
		"offset":          offset,
		"timeout":         30,
		"allowed_updates": []string{"channel_post", "message"},
	}
	var out tgResponse[[]tgUpdate]
	pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if err := b.callJSON(pollCtx, "getUpdates", body, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

type tgFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
}

func (b *bot) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	var out tgResponse[tgFile]
	if err := b.callJSON(ctx, "getFile", map[string]string{"file_id": fileID}, &out); err != nil {
		return nil, err
	}
	url := "https://api.telegram.org/file/bot" + b.token + "/" + out.Result.FilePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
