package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const telegramMaxLen = 4096

type botConfig struct {
	WebhookURL        string `json:"webhook_url"`
	Listen            string `json:"listen"`
	AllowUnregistered bool   `json:"allow_unregistered_users"`
}

type telegramConfig struct {
	Token string     `json:"token"`
	Bot   *botConfig `json:"bot,omitempty"`
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

	resp, err := httpClient.PostForm(apiURL, vals)
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

// sendPhotoDataURI sends a photo from a data URI (data:image/jpeg;base64,...) to a Telegram chat.
// Returns the message_id of the sent photo message.
func sendPhotoDataURI(token string, chatID int64, dataURI, caption string, replyToMsgID int64) (int64, error) {
	// Parse data URI: "data:<mime>;base64,<data>"
	parts := strings.SplitN(dataURI, ",", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid data URI")
	}

	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, fmt.Errorf("decode base64: %w", err)
	}

	// Determine extension from MIME
	ext := ".jpg"
	header := parts[0] // "data:image/png;base64"
	if strings.Contains(header, "image/png") {
		ext = ".png"
	} else if strings.Contains(header, "image/gif") {
		ext = ".gif"
	} else if strings.Contains(header, "image/webp") {
		ext = ".webp"
	}

	// Build multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}
	if replyToMsgID != 0 {
		_ = writer.WriteField("reply_to_message_id", strconv.FormatInt(replyToMsgID, 10))
	}
	part, err := writer.CreateFormFile("photo", "snapshot"+ext)
	if err != nil {
		return 0, err
	}
	if _, err := part.Write(data); err != nil {
		return 0, err
	}
	_ = writer.Close()

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	resp, err := httpClient.Post(apiURL, writer.FormDataContentType(), &buf)
	if err != nil {
		return 0, fmt.Errorf("sendPhoto request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("sendPhoto decode: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("sendPhoto: %s", result.Description)
	}
	return result.Result.MessageID, nil
}

// sendTelegramText splits text, converts each chunk from markdown to HTML and
// sends it. Splitting happens before conversion so an opening tag and its
// closing tag always land in the same chunk. A chunk Telegram refuses to parse
// is re-sent on its own as plain text; re-sending the whole message, as this
// used to, delivered every earlier chunk a second time.
// replyToMsgID, when non-zero, makes the first chunk a reply.
// Returns the message_id of the first chunk sent.
func sendTelegramText(token string, chatID int64, text string, replyToMsgID int64) (int64, error) {
	var firstMsgID int64
	chunks := splitTelegramMessage(text)
	for i, chunk := range chunks {
		replyID := int64(0)
		if i == 0 {
			replyID = replyToMsgID
		}

		var msgID int64
		var err error
		if html := markdownToTelegramHTML(chunk); len(html) <= telegramMaxLen {
			msgID, err = sendTelegramChunk(token, chatID, html, "HTML", replyID)
		} else {
			// Conversion outgrew the limit; the HTML attempt would only be
			// rejected, so go straight to plain text.
			err = fmt.Errorf("converted chunk too long")
		}
		if err != nil {
			msgID, err = sendTelegramChunk(token, chatID, chunk, "", replyID)
			if err != nil {
				return firstMsgID, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
			}
		}

		if firstMsgID == 0 {
			firstMsgID = msgID
		}
	}
	return firstMsgID, nil
}

// sendToChat sends text to a single chat with markdown→HTML conversion + splitting.
func sendToChat(token string, chatID int64, text string) error {
	_, err := sendTelegramText(token, chatID, text, 0)
	return err
}

// sendBotReply sends text as a reply to replyToMsgID and returns the first sent message ID.
func sendBotReply(token string, chatID int64, text string, replyToMsgID int64) (int64, error) {
	return sendTelegramText(token, chatID, text, replyToMsgID)
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

// sendMessageWithKeyboard sends a message with an inline keyboard and returns the message_id.
func sendMessageWithKeyboard(token string, chatID int64, text string, keyboard any) (int64, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	replyMarkup, err := json.Marshal(keyboard)
	if err != nil {
		return 0, fmt.Errorf("marshal keyboard: %w", err)
	}

	vals := url.Values{
		"chat_id":      {strconv.FormatInt(chatID, 10)},
		"text":         {text},
		"reply_markup": {string(replyMarkup)},
	}

	resp, err := httpClient.PostForm(apiURL, vals)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

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

// answerCallbackQuery acknowledges a callback query to remove the loading indicator.
func answerCallbackQuery(token, callbackQueryID string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	vals := url.Values{
		"callback_query_id": {callbackQueryID},
	}
	resp, err := httpClient.PostForm(apiURL, vals)
	if err != nil {
		return fmt.Errorf("answerCallbackQuery: %w", err)
	}
	resp.Body.Close()
	return nil
}

// sendTypingAction sends a "typing" indicator to a chat.
func sendTypingAction(token string, chatID int64) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", token)
	vals := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"action":  {"typing"},
	}
	resp, err := httpClient.PostForm(apiURL, vals)
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

// telegramSplitBudget is the byte budget for one markdown chunk. Converting
// markdown to HTML only grows the text (escaping, tags), so the budget sits
// below telegramMaxLen to leave room. Bytes are a safe proxy for Telegram's
// limit: a string is never longer in UTF-16 code units than it is in bytes.
const telegramSplitBudget = 3000

func splitTelegramMessage(text string) []string {
	if len(text) <= telegramSplitBudget {
		return []string{text}
	}

	var chunks []string
	for len(text) > telegramSplitBudget {
		cut := telegramCutPoint(text)
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

// telegramCutPoint picks where to break a chunk: the last newline in the
// window, else the last space, else the nearest rune boundary. Cutting on a
// raw byte splits a multi-byte character, and Telegram rejects invalid UTF-8.
// For Cyrillic text that happens on any long run without a newline.
func telegramCutPoint(s string) int {
	window := s[:telegramSplitBudget]
	if i := strings.LastIndexByte(window, '\n'); i > 0 {
		return i + 1 // include the newline in the current chunk
	}
	if i := strings.LastIndexByte(window, ' '); i > 0 {
		return i + 1
	}
	cut := telegramSplitBudget
	for cut > 1 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return cut
}
