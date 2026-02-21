package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const telegramMaxLen = 4096

type chatRouting struct {
	News  []int64 `json:"news"`
	Mail  []int64 `json:"mail"`
	Other []int64 `json:"other"`
}

type botConfig struct {
	WebhookURL   string  `json:"webhook_url"`
	Listen       string  `json:"listen"`
	AllowedUsers []int64 `json:"allowed_users"`
}

type telegramConfig struct {
	Token string      `json:"token"`
	Chats chatRouting `json:"chat_id"`
	Bot   *botConfig  `json:"bot,omitempty"`
}

func loadTelegramConfig(path string) (*telegramConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg telegramConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram config: token is empty")
	}

	// Validate: chat_id required unless bot-only config
	if cfg.Bot == nil && len(cfg.Chats.News) == 0 && len(cfg.Chats.Mail) == 0 && len(cfg.Chats.Other) == 0 {
		return nil, fmt.Errorf("telegram config: chat_id is empty")
	}

	return &cfg, nil
}

// sendTelegramChunk sends a single message chunk to one chat.
func sendTelegramChunk(token string, chatID int64, text, parseMode string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	vals := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	}
	if parseMode != "" {
		vals.Set("parse_mode", parseMode)
	}

	resp, err := http.PostForm(apiURL, vals)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode error: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("API error: %s", result.Description)
	}
	return nil
}

// sendToChat sends text to a single chat with markdown→HTML conversion + splitting.
// Falls back to plain text if HTML parsing fails.
func sendToChat(token string, chatID int64, text string) error {
	html := markdownToTelegramHTML(text)
	chunks := splitTelegramMessage(html)
	for _, chunk := range chunks {
		if err := sendTelegramChunk(token, chatID, chunk, "HTML"); err != nil {
			// Fallback: send as plain text
			plain := splitTelegramMessage(text)
			for j, p := range plain {
				if err2 := sendTelegramChunk(token, chatID, p, ""); err2 != nil {
					return fmt.Errorf("chunk %d/%d (plain fallback): %w", j+1, len(plain), err2)
				}
			}
			return nil
		}
	}
	return nil
}

// sendToChats sends text to multiple chats.
func sendToChats(token string, chatIDs []int64, text string) error {
	for _, id := range chatIDs {
		if err := sendToChat(token, id, text); err != nil {
			return fmt.Errorf("chat %d: %w", id, err)
		}
	}
	return nil
}

// sendTypingAction sends a "typing" indicator to a chat.
func sendTypingAction(token string, chatID int64) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", token)
	vals := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"action":  {"typing"},
	}
	resp, err := http.PostForm(apiURL, vals)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// markdownToTelegramHTML converts common markdown to Telegram-supported HTML.
// Telegram supports: <b>, <i>, <code>, <pre>, <a>, <s>, <u>, <blockquote>
func markdownToTelegramHTML(text string) string {
	// Escape HTML entities first
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")

	var lines []string
	for _, line := range strings.Split(text, "\n") {
		// Headers: ## text → <b>text</b>
		if trimmed := strings.TrimLeft(line, "#"); len(trimmed) < len(line) {
			trimmed = strings.TrimSpace(trimmed)
			if trimmed != "" {
				lines = append(lines, "<b>"+trimmed+"</b>")
				continue
			}
		}
		lines = append(lines, line)
	}
	text = strings.Join(lines, "\n")

	// Inline code: `text` → <code>text</code>
	text = reInlineCode.ReplaceAllString(text, "<code>$1</code>")

	// Bold: **text** → <b>text</b>
	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	// Italic: *text* → <i>text</i> (but not ** which is bold)
	text = reItalic.ReplaceAllString(text, "${1}<i>$2</i>")

	return text
}

var (
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`(^|[^*])\*([^*]+?)\*`)
)

func splitTelegramMessage(text string) []string {
	if len(text) <= telegramMaxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= telegramMaxLen {
			chunks = append(chunks, text)
			break
		}

		// Find last newline before the limit
		cut := telegramMaxLen
		for i := cut - 1; i > 0; i-- {
			if text[i] == '\n' {
				cut = i + 1 // include the newline in current chunk
				break
			}
		}

		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}
