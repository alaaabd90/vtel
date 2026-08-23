package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultHostBase     = "https://api.telegram.org"
	MaxSendDocumentSize = 50 * 1024 * 1024 // 50MB Telegram sendDocument limit
)

// sharedTransport is one *http.Transport shared by every API instance in
// the process. Every API instance already implicitly shared connection
// pooling via http.DefaultTransport (no Client.Transport was ever set), but
// DefaultTransport's MaxIdleConnsPerHost of 2 is far too low once a pool of
// N bots (Stage 1) all hit the same api.telegram.org host concurrently -
// that would force most requests to open a fresh TCP+TLS connection instead
// of reusing an idle one. Raising it per-host, explicitly, is the actual
// fix; MaxIdleConns/IdleConnTimeout are set generously for the same reason.
var sharedTransport = &http.Transport{
	MaxIdleConns:        256,
	MaxIdleConnsPerHost: 128,
	IdleConnTimeout:     300 * time.Second,
	TLSHandshakeTimeout: 30 * time.Second,
}

type API struct {
	token    string
	hostBase string // scheme+host only, e.g. "https://api.telegram.org"; overridable for tests
	client   *http.Client
}

func NewAPI(token string) *API {
	return NewAPIWithHost(token, defaultHostBase)
}

// NewAPIWithHost is NewAPI with an overridable host, for pointing at an
// in-memory fake Telegram server in tests/smoketests - never used against a
// real bot token in production.
func NewAPIWithHost(token, hostBase string) *API {
	return &API{
		token:    token,
		hostBase: hostBase,
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: sharedTransport,
		},
	}
}

// WarmConnection performs a cheap no-op API call purely to keep this bot's
// connection in sharedTransport's idle pool warm - and, as a side effect,
// verify reachability. Called periodically by pool.Pool.RunWarmup.
func (a *API) WarmConnection() error {
	_, err := a.GetMe()
	return err
}

func (a *API) botURL(method string) string {
	return a.hostBase + "/bot" + a.token + "/" + method
}

type Update struct {
	UpdateID    int          `json:"update_id"`
	ChannelPost *ChannelPost `json:"channel_post"`
}

type ChannelPost struct {
	MessageID  int       `json:"message_id"`
	Chat       Chat      `json:"chat"`
	From       *User     `json:"from"`
	Text       string    `json:"text"`
	Document   *Document `json:"document"`
	SenderChat *Chat     `json:"sender_chat"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int    `json:"file_size"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type fileResponse struct {
	FilePath string `json:"file_path"`
}

type apiError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.Code, e.Description)
}

func (e *apiError) Permanent() bool {
	return e.Code >= 400 && e.Code < 500 && e.Code != 429
}

func newAPIError(result apiResponse) *apiError {
	err := &apiError{
		Code:        result.ErrorCode,
		Description: result.Description,
	}
	if result.Parameters != nil {
		err.RetryAfter = result.Parameters.RetryAfter
	}
	return err
}

// SendDocument uploads a document to a channel. Returns retry_after on 429.
func (a *API) SendDocument(channelID int64, filename string, data []byte) (retryAfter int, err error) {
	if len(data) > MaxSendDocumentSize {
		return 0, fmt.Errorf("document size %d exceeds Telegram 50MB limit", len(data))
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	_ = w.WriteField("chat_id", strconv.FormatInt(channelID, 10))

	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		return 0, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return 0, fmt.Errorf("write document: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("close multipart: %w", err)
	}

	url := a.botURL("sendDocument")
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result apiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("decode response: %w (body: %s)", err, string(respBody))
	}

	if !result.OK {
		apiErr := newAPIError(result)
		return apiErr.RetryAfter, apiErr
	}
	return 0, nil
}

// SendMessage sends a text message to a channel. Returns retry_after on 429.
func (a *API) SendMessage(channelID int64, text string) (retryAfter int, err error) {
	payload := struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{ChatID: channelID, Text: text}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	url := a.botURL("sendMessage")
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result apiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		apiErr := newAPIError(result)
		return apiErr.RetryAfter, apiErr
	}
	return 0, nil
}

// GetUpdates long-polls for updates.
func (a *API) GetUpdates(offset int, timeout int) ([]Update, error) {
	url := fmt.Sprintf("%s?offset=%d&timeout=%d&allowed_updates=[\"channel_post\"]",
		a.botURL("getUpdates"), offset, timeout)

	resp, err := a.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("get updates: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if !result.OK {
		return nil, newAPIError(result)
	}

	var updates []Update
	if err := json.Unmarshal(result.Result, &updates); err != nil {
		return nil, fmt.Errorf("decode updates: %w", err)
	}
	return updates, nil
}

// DownloadFile downloads a file by file_id.
func (a *API) DownloadFile(fileID string) ([]byte, error) {
	// Step 1: getFile to get file_path
	url := fmt.Sprintf("%s?file_id=%s", a.botURL("getFile"), fileID)
	resp, err := a.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, newAPIError(result)
	}

	var fr fileResponse
	if err := json.Unmarshal(result.Result, &fr); err != nil {
		return nil, err
	}

	// Step 2: download from file path
	dlURL := fmt.Sprintf("%s/file/bot%s/%s", a.hostBase, a.token, fr.FilePath)
	dlResp, err := a.client.Get(dlURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer dlResp.Body.Close()

	return io.ReadAll(dlResp.Body)
}

// GetMe returns this bot's user info.
func (a *API) GetMe() (*User, error) {
	url := a.botURL("getMe")
	resp, err := a.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, newAPIError(result)
	}

	var user User
	if err := json.Unmarshal(result.Result, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
