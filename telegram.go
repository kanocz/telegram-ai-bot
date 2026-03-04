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
// If replyToMsgID is non-zero, the message is sent as a reply.
// Returns the message_id of the sent message.
func sendTelegramChunk(token string, chatID int64, text, parseMode string, replyToMsgID int64) (int64, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	vals := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	}
	if parseMode != "" {
		vals.Set("parse_mode", parseMode)
	}
	if replyToMsgID != 0 {
		vals.Set("reply_to_message_id", strconv.FormatInt(replyToMsgID, 10))
	}

	resp, err := http.PostForm(apiURL, vals)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("decode error: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("API error: %s", result.Description)
	}
	return result.Result.MessageID, nil
}

// sendToChat sends text to a single chat with markdown→HTML conversion + splitting.
// Falls back to plain text if HTML parsing fails.
func sendToChat(token string, chatID int64, text string) error {
	html := markdownToTelegramHTML(text)
	chunks := splitTelegramMessage(html)
	for _, chunk := range chunks {
		if _, err := sendTelegramChunk(token, chatID, chunk, "HTML", 0); err != nil {
			// Fallback: send as plain text
			plain := splitTelegramMessage(text)
			for j, p := range plain {
				if _, err2 := sendTelegramChunk(token, chatID, p, "", 0); err2 != nil {
					return fmt.Errorf("chunk %d/%d (plain fallback): %w", j+1, len(plain), err2)
				}
			}
			return nil
		}
	}
	return nil
}

// sendBotReply sends text as a reply to replyToMsgID and returns the first sent message ID.
func sendBotReply(token string, chatID int64, text string, replyToMsgID int64) (int64, error) {
	html := markdownToTelegramHTML(text)
	chunks := splitTelegramMessage(html)
	var firstMsgID int64
	for i, chunk := range chunks {
		replyID := int64(0)
		if i == 0 {
			replyID = replyToMsgID
		}
		msgID, err := sendTelegramChunk(token, chatID, chunk, "HTML", replyID)
		if err != nil {
			// Fallback: send as plain text
			plain := splitTelegramMessage(text)
			for j, p := range plain {
				rid := int64(0)
				if j == 0 && firstMsgID == 0 {
					rid = replyToMsgID
				}
				mid, err2 := sendTelegramChunk(token, chatID, p, "", rid)
				if err2 != nil {
					return 0, fmt.Errorf("chunk %d/%d (plain fallback): %w", j+1, len(plain), err2)
				}
				if firstMsgID == 0 {
					firstMsgID = mid
				}
			}
			return firstMsgID, nil
		}
		if firstMsgID == 0 {
			firstMsgID = msgID
		}
	}
	return firstMsgID, nil
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

// escapeHTML escapes &, < and > for Telegram HTML parse mode.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// markdownToTelegramHTML converts common markdown to Telegram-supported HTML.
// Handles fenced code blocks (```), inline code, bold, italic, and headers.
func markdownToTelegramHTML(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	var codeLines []string
	inCodeBlock := false
	codeBlockLang := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Fenced code block delimiter
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				codeLines = nil
			} else {
				code := escapeHTML(strings.Join(codeLines, "\n"))
				if codeBlockLang != "" {
					out = append(out, fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", escapeHTML(codeBlockLang), code))
				} else {
					out = append(out, "<pre>"+code+"</pre>")
				}
				inCodeBlock = false
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Normal line: escape HTML, then apply markdown formatting
		line = escapeHTML(line)

		// Headers: ## text → <b>text</b>
		if hdr := strings.TrimLeft(line, "#"); len(hdr) < len(line) {
			hdr = strings.TrimSpace(hdr)
			if hdr != "" {
				out = append(out, "<b>"+hdr+"</b>")
				continue
			}
		}

		// Inline code: `text` → <code>text</code>
		line = reInlineCode.ReplaceAllString(line, "<code>$1</code>")
		// Bold: **text** → <b>text</b>
		line = reBold.ReplaceAllString(line, "<b>$1</b>")
		// Italic: *text* → <i>text</i> (but not ** which is bold)
		line = reItalic.ReplaceAllString(line, "${1}<i>$2</i>")

		out = append(out, line)
	}

	// Unclosed code block — emit what we have
	if inCodeBlock {
		code := escapeHTML(strings.Join(codeLines, "\n"))
		out = append(out, "<pre>"+code+"</pre>")
	}

	return strings.Join(out, "\n")
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
