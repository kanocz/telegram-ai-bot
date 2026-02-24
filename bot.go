package main

import (
	"context"
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

type TGMessage struct {
	MessageID int64   `json:"message_id"`
	From      *TGUser `json:"from,omitempty"`
	Chat      TGChat  `json:"chat"`
	Date      int64   `json:"date"`
	Text      string  `json:"text,omitempty"`
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

func runBot(tgCfg *telegramConfig, cfg modelConfig, modelID string,
	showThinking bool, logf func(string, ...any), prompts *Prompts,
	verboseTools bool, newsURLsPath string, mcpMgr *MCPManager) error {

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

		var update Update
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Always respond 200 quickly to avoid Telegram retries
		w.WriteHeader(http.StatusOK)

		msg := update.Message
		if msg == nil || msg.Text == "" {
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
		log.Printf("Message from %s (chat %d): %s", userLabel, msg.Chat.ID, truncate(msg.Text, 100))

		// Process asynchronously
		go handleBotMessage(tgCfg.Token, cfg, modelID, showThinking, logf, prompts, verboseTools, newsURLsPath, mcpMgr, msg)
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
	verboseTools bool, newsURLsPath string, mcpMgr *MCPManager, msg *TGMessage) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic handling message %d: %v", msg.MessageID, r)
			_ = sendToChat(token, msg.Chat.ID, fmt.Sprintf("Internal error: %v", r))
		}
	}()

	chatID := msg.Chat.ID
	cancel := startTyping(token, chatID)
	defer cancel()

	text := strings.TrimSpace(msg.Text)

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

	switch {
	case text == "/news" || strings.HasPrefix(text, "/news "):
		result, err = runNewsSummary(cfg, modelID, showThinking, io.Discard, logf, newsURLsPath, prompts, mcpMgr, mcpNames)

	case text == "/mail" || strings.HasPrefix(text, "/mail "):
		sinceHours := 24.0
		parts := strings.Fields(text)
		if len(parts) >= 2 {
			if h, parseErr := strconv.ParseFloat(parts[1], 64); parseErr == nil && h > 0 {
				sinceHours = h
			}
		}
		result, err = runMailSummary(cfg, modelID, showThinking, io.Discard, logf, prompts, sinceHours, mcpMgr, mcpNames)

	default:
		query := text
		if query == "/start" || query == "/help" {
			query = "Привет! Чем могу помочь? Доступные команды: /news — дайджест новостей, /mail [часы] — дайджест почты, /mcp сервер запрос — с MCP-инструментами, или отправь любой вопрос."
			_ = sendToChat(token, chatID, query)
			return
		}
		result, err = runQuery(cfg, modelID, query, showThinking, verboseTools, io.Discard, logf, prompts, mcpMgr, mcpNames)
	}

	if err != nil {
		log.Printf("Error processing message %d: %v", msg.MessageID, err)
		_ = sendToChat(token, chatID, fmt.Sprintf("Ошибка: %v", err))
		return
	}

	if err := sendToChat(token, chatID, stripThinkTags(result)); err != nil {
		log.Printf("Error sending response to chat %d: %v", chatID, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
