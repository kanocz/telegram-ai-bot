package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Telegram Bot API types

type TGUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type TGChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type TGPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size,omitempty"`
}

type TGVideo struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type TGDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type TGMessage struct {
	MessageID int64         `json:"message_id"`
	From      *TGUser       `json:"from,omitempty"`
	Chat      TGChat        `json:"chat"`
	Date      int64         `json:"date"`
	Text      string        `json:"text,omitempty"`
	Photo     []TGPhotoSize `json:"photo,omitempty"`
	Video     *TGVideo      `json:"video,omitempty"`
	VideoNote *TGVideo      `json:"video_note,omitempty"`
	Animation *TGVideo      `json:"animation,omitempty"`
	Document  *TGDocument   `json:"document,omitempty"`
	Caption   string        `json:"caption,omitempty"`
}

// hasVideo returns true if the message contains a video in any form
// (video, video_note, animation, or document with video MIME).
func (m *TGMessage) hasVideo() bool {
	if m.Video != nil || m.VideoNote != nil || m.Animation != nil {
		return true
	}
	return m.Document != nil && strings.HasPrefix(m.Document.MimeType, "video/")
}

// videoFileID returns the file_id and MIME type for the video attachment.
func (m *TGMessage) videoFileID() (fileID, mime string) {
	switch {
	case m.Video != nil:
		return m.Video.FileID, m.Video.MimeType
	case m.VideoNote != nil:
		return m.VideoNote.FileID, m.VideoNote.MimeType
	case m.Animation != nil:
		return m.Animation.FileID, m.Animation.MimeType
	case m.Document != nil && strings.HasPrefix(m.Document.MimeType, "video/"):
		return m.Document.FileID, m.Document.MimeType
	}
	return "", ""
}

type Update struct {
	UpdateID int64      `json:"update_id"`
	Message  *TGMessage `json:"message,omitempty"`
}

// Webhook management

func setWebhook(token, webhookURL string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/setWebhook", token)
	vals := url.Values{"url": {webhookURL}}
	resp, err := http.PostForm(apiURL, vals)
	if err != nil {
		return fmt.Errorf("setWebhook request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("setWebhook decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("setWebhook: %s", result.Description)
	}
	return nil
}

func deleteWebhook(token string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook", token)
	resp, err := http.PostForm(apiURL, url.Values{})
	if err != nil {
		return fmt.Errorf("deleteWebhook request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("deleteWebhook decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("deleteWebhook: %s", result.Description)
	}
	return nil
}

// startTyping sends a "typing" action every 4 seconds until cancel is called.
func startTyping(token string, chatID int64) (cancel func()) {
	done := make(chan struct{})
	go func() {
		_ = sendTypingAction(token, chatID)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = sendTypingAction(token, chatID)
			}
		}
	}()
	return func() { close(done) }
}

// downloadTelegramFile downloads a file by file_id via the Bot API.
func downloadTelegramFile(token, fileID string) ([]byte, error) {
	// Step 1: getFile to obtain file_path
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, url.QueryEscape(fileID))
	resp, err := http.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("getFile request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("getFile decode: %w", err)
	}
	if !result.OK || result.Result.FilePath == "" {
		return nil, fmt.Errorf("getFile: no file_path returned")
	}

	// Step 2: download the file
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, result.Result.FilePath)
	fileResp, err := http.Get(fileURL)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer fileResp.Body.Close()

	data, err := io.ReadAll(fileResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file body: %w", err)
	}
	return data, nil
}

func runBot(tgCfg *telegramConfig, cfg modelConfig, modelID string,
	showThinking bool, logf func(string, ...any), prompts *Prompts,
	verboseTools bool, newsConfigPath string, mcpMgr *MCPManager, globalThink thinkMode) error {

	if tgCfg.Bot == nil {
		return fmt.Errorf("telegram config: 'bot' section is required for -telegram-bot")
	}
	botCfg := tgCfg.Bot

	// Build allowed user set
	allowed := make(map[int64]bool, len(botCfg.AllowedUsers))
	for _, uid := range botCfg.AllowedUsers {
		allowed[uid] = true
	}

	// Set webhook
	if err := setWebhook(tgCfg.Token, botCfg.WebhookURL); err != nil {
		return fmt.Errorf("set webhook: %w", err)
	}
	log.Printf("Webhook set to %s", botCfg.WebhookURL)

	// Extract path from webhook URL for handler registration
	u, err := url.Parse(botCfg.WebhookURL)
	if err != nil {
		return fmt.Errorf("parse webhook URL: %w", err)
	}
	hookPath := u.Path
	if hookPath == "" {
		hookPath = "/"
	}

	mux := http.NewServeMux()
	mux.HandleFunc(hookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		bodyBytes, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		if requestDebug {
			log.Printf("Telegram update: %s", string(bodyBytes))
		}

		var update Update
		if err := json.Unmarshal(bodyBytes, &update); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Always respond 200 quickly to avoid Telegram retries
		w.WriteHeader(http.StatusOK)

		msg := update.Message
		if msg == nil || (msg.Text == "" && len(msg.Photo) == 0 && !msg.hasVideo()) {
			if requestDebug && msg != nil {
				log.Printf("Message filtered out: text=%q photo=%d video=%v anim=%v doc=%v",
					msg.Text, len(msg.Photo), msg.Video != nil, msg.Animation != nil, msg.Document != nil)
			}
			return
		}

		// Check allowed users (if list is non-empty)
		if len(allowed) > 0 && msg.From != nil && !allowed[msg.From.ID] {
			log.Printf("Rejected message from user %d (%s)", msg.From.ID, msg.From.Username)
			return
		}

		userLabel := "unknown"
		if msg.From != nil {
			userLabel = msg.From.FirstName
			if msg.From.Username != "" {
				userLabel += " @" + msg.From.Username
			}
		}
		logText := msg.Text
		if logText == "" && len(msg.Photo) > 0 {
			logText = "[photo] " + msg.Caption
		}
		if logText == "" && msg.hasVideo() {
			logText = "[video] " + msg.Caption
		}
		log.Printf("Message from %s (chat %d): %s", userLabel, msg.Chat.ID, truncate(logText, 100))

		// Process asynchronously
		go handleBotMessage(tgCfg.Token, cfg, modelID, showThinking, logf, prompts, verboseTools, newsConfigPath, mcpMgr, globalThink, msg)
	})

	server := &http.Server{
		Addr:    botCfg.Listen,
		Handler: mux,
	}

	// Graceful shutdown
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-shutdownCh
		log.Println("Shutting down...")

		if err := deleteWebhook(tgCfg.Token); err != nil {
			log.Printf("deleteWebhook error: %v", err)
		} else {
			log.Println("Webhook deleted")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("Bot listening on %s", botCfg.Listen)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

func handleBotMessage(token string, cfg modelConfig, modelID string,
	showThinking bool, logf func(string, ...any), prompts *Prompts,
	verboseTools bool, newsConfigPath string, mcpMgr *MCPManager, globalThink thinkMode, msg *TGMessage) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic handling message %d: %v", msg.MessageID, r)
			_ = sendToChat(token, msg.Chat.ID, fmt.Sprintf("Internal error: %v", r))
		}
	}()

	chatID := msg.Chat.ID
	cancel := startTyping(token, chatID)
	defer cancel()

	// Use Caption as text when message has photo or video
	text := strings.TrimSpace(msg.Text)
	if text == "" && (len(msg.Photo) > 0 || msg.hasVideo()) {
		text = strings.TrimSpace(msg.Caption)
	}

	// Download photo if present
	var images []ImageURL
	if len(msg.Photo) > 0 {
		// Telegram sends multiple sizes; last element is the largest
		best := msg.Photo[len(msg.Photo)-1]
		data, dlErr := downloadTelegramFile(token, best.FileID)
		if dlErr != nil {
			log.Printf("Error downloading photo for message %d: %v", msg.MessageID, dlErr)
			_ = sendToChat(token, chatID, fmt.Sprintf("Ошибка загрузки фото: %v", dlErr))
			return
		}
		mime := http.DetectContentType(data)
		b64 := base64.StdEncoding.EncodeToString(data)
		images = append(images, ImageURL{URL: fmt.Sprintf("data:%s;base64,%s", mime, b64)})

		// Default prompt if no caption
		if text == "" {
			text = "Опиши это изображение."
		}
	}

	// Download video if present (video, video_note, animation, or document with video MIME)
	var videos []VideoURL
	if msg.hasVideo() {
		fileID, mimeType := msg.videoFileID()
		data, dlErr := downloadTelegramFile(token, fileID)
		if dlErr != nil {
			log.Printf("Error downloading video for message %d: %v", msg.MessageID, dlErr)
			_ = sendToChat(token, chatID, fmt.Sprintf("Ошибка загрузки видео: %v", dlErr))
			return
		}
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		videos = append(videos, VideoURL{URL: fmt.Sprintf("data:%s;base64,%s", mimeType, b64)})

		// Default prompt if no caption
		if text == "" {
			text = "Опиши это видео."
		}
	}

	// Parse /think and /nothink prefixes (before /mcp)
	thinkPrefix, text := parseThinkPrefix(text)
	noThinkPrefix, text := parseNothinkPrefix(text)
	think := globalThink
	if thinkPrefix {
		think = thinkEnable
	} else if noThinkPrefix {
		think = thinkDisable
	}

	// Parse /mcp prefix (works for all commands: /mcp github /news, /mcp github query, etc.)
	mcpNames, text := parseMCPPrefix(text)
	if len(mcpNames) > 0 {
		if mcpMgr == nil {
			_ = sendToChat(token, chatID, "MCP not configured (mcp.json not found)")
			return
		}
		if err := mcpMgr.InitServers(mcpNames); err != nil {
			_ = sendToChat(token, chatID, fmt.Sprintf("MCP error: %v", err))
			return
		}
	}

	var result string
	var err error

	// Content output: stderr for debugging (unless quiet)
	var debugOut io.Writer = io.Discard
	if !quietMode {
		debugOut = os.Stderr
	}

	switch {
	case text == "/news" || strings.HasPrefix(text, "/news "):
		result, err = runNewsSummary(cfg, modelID, showThinking, debugOut, logf, newsConfigPath, prompts, mcpMgr, mcpNames, think)

	case text == "/mail" || strings.HasPrefix(text, "/mail "):
		sinceHours := 24.0
		parts := strings.Fields(text)
		if len(parts) >= 2 {
			if h, parseErr := strconv.ParseFloat(parts[1], 64); parseErr == nil && h > 0 {
				sinceHours = h
			}
		}
		result, err = runMailSummary(cfg, modelID, showThinking, debugOut, logf, prompts, sinceHours, mcpMgr, mcpNames, think)

	default:
		query := text
		if query == "/start" || query == "/help" {
			query = "Привет! Чем могу помочь? Доступные команды: /news — дайджест новостей, /mail [часы] — дайджест почты, /mcp сервер запрос — с MCP-инструментами, /think — включить reasoning, /nothink — отключить reasoning, или отправь любой вопрос."
			_ = sendToChat(token, chatID, query)
			return
		}
		var contentBuf strings.Builder
		contentOut := io.MultiWriter(&contentBuf, debugOut)
		result, err = runQuery(cfg, modelID, query, showThinking, verboseTools, contentOut, logf, prompts, mcpMgr, mcpNames, think, images, videos)
		// runQuery returns only the last round's content; contentBuf has
		// accumulated content from ALL rounds (including intermediate tool-calling
		// rounds). Use it as fallback when the final response is empty.
		if err == nil && strings.TrimSpace(stripThinkTags(result)) == "" {
			if s := strings.TrimSpace(contentBuf.String()); s != "" {
				result = s
			}
		}
	}

	if err != nil {
		log.Printf("Error processing message %d: %v", msg.MessageID, err)
		_ = sendToChat(token, chatID, fmt.Sprintf("Ошибка: %v", err))
		return
	}

	reply := stripThinkTags(result)
	if strings.TrimSpace(reply) == "" {
		reply = "(Модель не вернула текстовый ответ — возможно, tool-вызов остался в reasoning. Попробуйте /nothink.)"
	}
	if err := sendToChat(token, chatID, reply); err != nil {
		log.Printf("Error sending response to chat %d: %v", chatID, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
