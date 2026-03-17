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
	apiBase             = "https://api.telegram.org/bot"
	MaxSendDocumentSize = 50 * 1024 * 1024 // 50MB Telegram sendDocument limit
)

type API struct {
	token  string
	client *http.Client
}

func NewAPI(token string) *API {
	return &API{
		token: token,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
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
	ID    int64  `json:"id"`
	IsBot bool   `json:"is_bot"`
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

	url := apiBase + a.token + "/sendDocument"
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
		if result.ErrorCode == 429 && result.Parameters != nil {
			return result.Parameters.RetryAfter, fmt.Errorf("rate limited: %s", result.Description)
		}
		return 0, fmt.Errorf("API error %d: %s", result.ErrorCode, result.Description)
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

	url := apiBase + a.token + "/sendMessage"
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
		if result.ErrorCode == 429 && result.Parameters != nil {
			return result.Parameters.RetryAfter, fmt.Errorf("rate limited: %s", result.Description)
		}
		return 0, fmt.Errorf("API error %d: %s", result.ErrorCode, result.Description)
	}
	return 0, nil
}

// GetUpdates long-polls for updates.
func (a *API) GetUpdates(offset int, timeout int) ([]Update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=%d&allowed_updates=[\"channel_post\"]",
		apiBase, a.token, offset, timeout)

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
		return nil, fmt.Errorf("API error %d: %s", result.ErrorCode, result.Description)
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
	url := fmt.Sprintf("%s%s/getFile?file_id=%s", apiBase, a.token, fileID)
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
		return nil, fmt.Errorf("API error %d: %s", result.ErrorCode, result.Description)
	}

	var fr fileResponse
	if err := json.Unmarshal(result.Result, &fr); err != nil {
		return nil, err
	}

	// Step 2: download from file path
	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", a.token, fr.FilePath)
	dlResp, err := a.client.Get(dlURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer dlResp.Body.Close()

	return io.ReadAll(dlResp.Body)
}

// GetMe returns this bot's user info.
func (a *API) GetMe() (*User, error) {
	url := apiBase + a.token + "/getMe"
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
		return nil, fmt.Errorf("API error: %s", result.Description)
	}

	var user User
	if err := json.Unmarshal(result.Result, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
