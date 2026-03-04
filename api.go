package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"ai-webfetch/tools"
)

// ImageURL holds a data-URI for an inline image.
type ImageURL struct {
	URL string `json:"url"`
}

// VideoURL holds a data-URI for an inline video.
type VideoURL struct {
	URL string `json:"url"`
}

// Message represents a chat message in OpenAI format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Images     []ImageURL `json:"-"` // vision images; handled by MarshalJSON
	Videos     []VideoURL `json:"-"` // vision videos; handled by MarshalJSON
}

// MarshalJSON implements custom JSON marshaling for Message.
// When Images is empty, Content is serialized as a plain string (backward-compatible).
// When Images is present, Content becomes an array of content blocks.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 && len(m.Videos) == 0 {
		// Standard encoding — same as default struct marshal.
		type plain Message // avoid recursion
		return json.Marshal(plain(m))
	}

	type contentBlock struct {
		Type     string    `json:"type"`
		Text     string    `json:"text,omitempty"`
		ImageURL *ImageURL `json:"image_url,omitempty"`
		VideoURL *VideoURL `json:"video_url,omitempty"`
	}

	blocks := []contentBlock{{Type: "text", Text: m.Content}}
	for i := range m.Images {
		blocks = append(blocks, contentBlock{
			Type:     "image_url",
			ImageURL: &m.Images[i],
		})
	}
	for i := range m.Videos {
		blocks = append(blocks, contentBlock{
			Type:     "video_url",
			VideoURL: &m.Videos[i],
		})
	}

	type alias struct {
		Role       string         `json:"role"`
		Content    []contentBlock `json:"content"`
		ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}
	return json.Marshal(alias{
		Role:       m.Role,
		Content:    blocks,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function FuncCall `json:"function"`
}

// FuncCall holds the function name and serialized arguments.
type FuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model             string             `json:"model"`
	Messages          []Message          `json:"messages"`
	Tools             []tools.Definition `json:"tools,omitempty"`
	Stream            bool               `json:"stream"`
	MaxTokens         int                `json:"max_tokens,omitempty"`
	ChatTemplateKwargs map[string]any    `json:"chat_template_kwargs,omitempty"`
}

type streamDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []streamToolCall `json:"tool_calls,omitempty"`
}

type streamToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function streamFuncCall `json:"function,omitempty"`
}

type streamFuncCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

// StreamResult holds the accumulated response from streaming.
type StreamResult struct {
	Content   string
	ToolCalls []ToolCall
}

var requestDebug bool
var reBase64Data = regexp.MustCompile(`(base64,)[A-Za-z0-9+/=]{200,}`)

func dumpRequestDebug(url string, payload []byte) {
	truncated := reBase64Data.ReplaceAllString(string(payload), "${1}[...base64 truncated]")
	fmt.Fprintf(os.Stderr, "%s--- REQUEST DEBUG ---%s\nPOST %s\n%s\n%s--- END REQUEST DEBUG ---%s\n",
		colorCyan, colorReset, url, truncated, colorCyan, colorReset)
}

const (
	colorDim   = "\033[2m"
	colorReset = "\033[0m"
	colorCyan  = "\033[36m"
)

// doStream sends a streaming chat completion request and displays the response.
// If toolDefs is nil, the request is sent without tools (pure generation).
func doStream(baseURL, model string, messages []Message, toolDefs []tools.Definition, maxTokens int, showThinking bool, contentOut io.Writer, disableThinking bool) (*StreamResult, error) {
	reqBody := chatRequest{
		Model:     model,
		Messages:  messages,
		Tools:     toolDefs,
		Stream:    true,
		MaxTokens: maxTokens,
	}
	if disableThinking {
		reqBody.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	apiURL := baseURL + "/chat/completions"
	if requestDebug {
		dumpRequestDebug(apiURL, payload)
	}

	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, b)
	}

	var result StreamResult
	tcMap := map[int]*ToolCall{}
	showThink := showThinking
	filter := &thinkFilter{
		writeThink:   func(s string) { if showThink { fmt.Fprint(os.Stderr, s) } },
		writeContent: func(s string) { fmt.Fprint(contentOut, s) },
		onThinkStart: func() { if showThink { fmt.Fprint(os.Stderr, colorDim) } },
		onThinkEnd:   func() { if showThink { fmt.Fprint(os.Stderr, colorReset+"\n") } },
	}
	hadReasoning := false
	reasoningDim := false
	var reasoningBuf strings.Builder

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, ch := range chunk.Choices {
			// Reasoning content (e.g. Qwen3 thinking via vLLM)
			if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
				hadReasoning = true
				reasoningBuf.WriteString(*ch.Delta.ReasoningContent)
				if showThinking {
					if !reasoningDim {
						fmt.Fprint(os.Stderr, colorDim)
						reasoningDim = true
					}
					fmt.Fprint(os.Stderr, *ch.Delta.ReasoningContent)
				}
			}

			// Regular content
			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				if reasoningDim {
					fmt.Fprint(os.Stderr, colorReset+"\n")
					reasoningDim = false
				}
				result.Content += *ch.Delta.Content
				if hadReasoning {
					// reasoning_content was used, content is clean
					fmt.Fprint(contentOut, *ch.Delta.Content)
				} else {
					// Fallback: parse <think> tags in content
					filter.process(*ch.Delta.Content)
				}
			}

			// Tool calls (accumulated across chunks)
			for _, tc := range ch.Delta.ToolCalls {
				if existing, ok := tcMap[tc.Index]; ok {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Function.Name != "" {
						existing.Function.Name = tc.Function.Name
					}
					existing.Function.Arguments += tc.Function.Arguments
				} else {
					tcMap[tc.Index] = &ToolCall{
						ID:   tc.ID,
						Type: tc.Type,
						Function: FuncCall{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
				}
			}
		}
	}

	filter.flush()
	if reasoningDim {
		fmt.Fprint(os.Stderr, colorReset+"\n")
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	for i := 0; i < len(tcMap); i++ {
		if tc, ok := tcMap[i]; ok {
			result.ToolCalls = append(result.ToolCalls, *tc)
		}
	}

	// If no API tool calls, try parsing from text (reasoning + content)
	if len(result.ToolCalls) == 0 {
		src := result.Content + "\n" + reasoningBuf.String()
		if parsed := parseTextToolCalls(src); len(parsed) > 0 {
			result.ToolCalls = parsed
			result.Content = strings.TrimSpace(reToolCallBlock.ReplaceAllString(result.Content, ""))
		}
	}

	return &result, nil
}

// thinkFilter handles <think>...</think> tags in streamed content.
// Output is delegated to callbacks so the same logic works for
// the main stream (stdout/stderr) and sub-agent streams (prefixed stderr).
type thinkFilter struct {
	writeThink   func(string) // emit thinking text
	writeContent func(string) // emit regular content
	onThinkStart func()       // called when <think> opens
	onThinkEnd   func()       // called when </think> closes
	active       bool         // inside <think> block
	pending      string       // buffer for partial tag matching
}

func (f *thinkFilter) process(chunk string) {
	f.pending += chunk

	for f.pending != "" {
		if !f.active {
			if idx := strings.Index(f.pending, "<think>"); idx >= 0 {
				if idx > 0 {
					f.writeContent(f.pending[:idx])
				}
				f.active = true
				f.pending = f.pending[idx+len("<think>"):]
				f.onThinkStart()
				continue
			}
			if n := partialSuffix(f.pending, "<think>"); n > 0 {
				f.writeContent(f.pending[:len(f.pending)-n])
				f.pending = f.pending[len(f.pending)-n:]
				return
			}
			f.writeContent(f.pending)
			f.pending = ""
		} else {
			if idx := strings.Index(f.pending, "</think>"); idx >= 0 {
				if idx > 0 {
					f.writeThink(f.pending[:idx])
				}
				f.active = false
				f.pending = f.pending[idx+len("</think>"):]
				f.onThinkEnd()
				continue
			}
			if n := partialSuffix(f.pending, "</think>"); n > 0 {
				safe := f.pending[:len(f.pending)-n]
				if safe != "" {
					f.writeThink(safe)
				}
				f.pending = f.pending[len(f.pending)-n:]
				return
			}
			f.writeThink(f.pending)
			f.pending = ""
		}
	}
}

func (f *thinkFilter) flush() {
	if f.pending == "" {
		return
	}
	if f.active {
		f.writeThink(f.pending)
		f.onThinkEnd()
	} else {
		f.writeContent(f.pending)
	}
	f.pending = ""
	f.active = false
}

// partialSuffix returns the length of the longest suffix of s
// that is a prefix of tag, or 0 if none.
func partialSuffix(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

// estimateTokens gives a rough upper-bound token estimate for messages.
// Uses ~3 chars per token (conservative for mixed multilingual content).
func estimateTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content) + len(m.Role) + 4 // role + formatting overhead
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments) + 20
		}
		chars += len(m.Images) * 3000  // ~1000 tokens per image (× 3 chars/token)
		chars += len(m.Videos) * 30000 // ~10000 tokens per video (× 3 chars/token)
	}
	return chars/3 + 50 // +50 for message framing overhead
}

// capMaxTokens adjusts maxTokens so input+output fits within contextLimit.
// Returns at least minOutput (256) tokens, or the original maxTokens if
// contextLimit is 0 (unknown).
func capMaxTokens(contextLimit, maxTokens int, messages []Message) int {
	if contextLimit <= 0 {
		return maxTokens
	}
	estimated := estimateTokens(messages)
	available := contextLimit - estimated
	const minOutput = 256
	if available < minOutput {
		return minOutput
	}
	if available < maxTokens {
		return available
	}
	return maxTokens
}

// doSubAgentWithTools runs a silent tool-calling loop for a sub-agent.
// It executes tool calls automatically for up to maxRounds iterations.
// After maxRounds, one final call is made WITHOUT tools to force a text response.
// contextLimit is the model's total context window (0 = no capping).
// maxToolResultChars limits the size of each tool result to prevent context overflow.
// The logf callback is used for optional progress output (suppressed in -quiet).
// toolExecFunc dispatches a tool call by name. Returns result text or error.
type toolExecFunc func(name string, args json.RawMessage) (string, error)

// defaultToolExec dispatches to built-in tools only.
func defaultToolExec(name string, args json.RawMessage) (string, error) {
	if tool, ok := tools.Get(name); ok {
		return tool.Execute(args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func doSubAgentWithTools(baseURL, model string, messages []Message,
	toolDefs []tools.Definition, maxTokens, contextLimit, maxRounds, maxToolResultChars int,
	logf func(string, ...any), execTool toolExecFunc, disableThinking bool) (string, error) {

	for round := 0; round < maxRounds; round++ {
		effectiveMax := capMaxTokens(contextLimit, maxTokens, messages)
		result, err := doStream(baseURL, model, messages, toolDefs, effectiveMax, false, io.Discard, disableThinking)
		if err != nil {
			return "", fmt.Errorf("round %d: %w", round, err)
		}

		if len(result.ToolCalls) == 0 {
			return stripThinkTags(result.Content), nil
		}

		// Add assistant message with tool calls
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		})

		// Execute each tool call
		exec := execTool
		if exec == nil {
			exec = defaultToolExec
		}
		for _, tc := range result.ToolCalls {
			logf("%s  [sub-agent tool: %s]%s\n", colorDim, tc.Function.Name, colorReset)

			var toolResult string
			res, execErr := exec(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if execErr != nil {
				toolResult = "error: " + execErr.Error()
			} else {
				toolResult = res
			}

			// Truncate tool results to prevent context overflow
			if maxToolResultChars > 0 && len(toolResult) > maxToolResultChars {
				toolResult = toolResult[:maxToolResultChars] + "\n[...truncated]"
			}

			messages = append(messages, Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			})
		}
	}

	// Max rounds exceeded — force text response by calling without tools
	logf("%s  [sub-agent: max rounds reached, forcing text]%s\n", colorDim, colorReset)
	effectiveMax := capMaxTokens(contextLimit, maxTokens, messages)
	result, err := doStream(baseURL, model, messages, nil, effectiveMax, false, io.Discard, disableThinking)
	if err != nil {
		return "", fmt.Errorf("final round: %w", err)
	}
	return stripThinkTags(result.Content), nil
}

// doChat makes a non-streaming chat completion call (used by sub-agents).
// contextLimit is the model's total context window (0 = no capping).
func doChat(baseURL, model string, messages []Message, maxTokens, contextLimit int, disableThinking bool) (string, error) {
	effectiveMax := capMaxTokens(contextLimit, maxTokens, messages)
	reqBody := chatRequest{
		Model:     model,
		Messages:  messages,
		Stream:    false,
		MaxTokens: effectiveMax,
	}
	if disableThinking {
		reqBody.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode error: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response from model")
	}

	return stripThinkTags(result.Choices[0].Message.Content), nil
}

var reThinkTags = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

func stripThinkTags(s string) string {
	return strings.TrimSpace(reThinkTags.ReplaceAllString(s, ""))
}

// reToolCallBlock extracts <tool_call>...</tool_call> blocks from text.
var reToolCallBlock = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)

// reXMLFunction parses <function=name> blocks inside a tool_call.
var reXMLFunction = regexp.MustCompile(`(?s)<function=([^>]+)>\s*(.*?)\s*</function>`)

// reXMLParameter parses <parameter=key>value</parameter> inside a function block.
var reXMLParameter = regexp.MustCompile(`(?s)<parameter=([^>]+)>\s*(.*?)\s*</parameter>`)

// parseTextToolCalls extracts tool calls from text containing <tool_call> blocks.
// Supports two formats: JSON (Qwen standard) and XML (<function=...><parameter=...>).
func parseTextToolCalls(text string) []ToolCall {
	matches := reToolCallBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var calls []ToolCall
	for i, m := range matches {
		body := m[1]

		// Try JSON format first: {"name": "func_name", "arguments": {...}}
		var jsonCall struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(body), &jsonCall); err == nil && jsonCall.Name != "" {
			args, _ := json.Marshal(jsonCall.Arguments)
			calls = append(calls, ToolCall{
				ID:   fmt.Sprintf("text_tc_%d", i),
				Type: "function",
				Function: FuncCall{
					Name:      jsonCall.Name,
					Arguments: string(args),
				},
			})
			continue
		}

		// Try XML format: <function=name><parameter=key>value</parameter></function>
		fnMatch := reXMLFunction.FindStringSubmatch(body)
		if fnMatch == nil {
			continue
		}
		funcName := fnMatch[1]
		paramBlock := fnMatch[2]
		params := map[string]string{}
		for _, pm := range reXMLParameter.FindAllStringSubmatch(paramBlock, -1) {
			params[pm[1]] = pm[2]
		}
		args, _ := json.Marshal(params)
		calls = append(calls, ToolCall{
			ID:   fmt.Sprintf("text_tc_%d", i),
			Type: "function",
			Function: FuncCall{
				Name:      funcName,
				Arguments: string(args),
			},
		})
	}

	if len(calls) == 0 {
		return nil
	}
	return calls
}

// prefixWriter writes to w, prepending prefix at the start of every line.
type prefixWriter struct {
	w      io.Writer
	prefix string
	bol    bool // at beginning of line
}

func (pw *prefixWriter) WriteString(s string) {
	for len(s) > 0 {
		if pw.bol {
			io.WriteString(pw.w, pw.prefix)
			pw.bol = false
		}
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			io.WriteString(pw.w, s)
			return
		}
		io.WriteString(pw.w, s[:idx+1])
		pw.bol = true
		s = s[idx+1:]
	}
}

// doSubAgentStream runs a streaming chat completion for a sub-agent,
// displaying all output (thinking + content) on stderr via prefixWriter.
// Returns the clean content (thinking stripped).
func doSubAgentStream(baseURL, model string, messages []Message, maxTokens int, pw *prefixWriter, disableThinking bool) (string, error) {
	reqBody := chatRequest{
		Model:     model,
		Messages:  messages,
		Stream:    true,
		MaxTokens: maxTokens,
	}
	if disableThinking {
		reqBody.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, b)
	}

	var contentBuf strings.Builder
	hadReasoning := false
	reasoningDim := false

	// For <think> tags — all output goes through pw, just with color toggling
	filter := &thinkFilter{
		writeThink:   func(s string) { pw.WriteString(s) },
		writeContent: func(s string) { pw.WriteString(s) },
		onThinkStart: func() { pw.WriteString(colorDim) },
		onThinkEnd:   func() { pw.WriteString(colorReset + "\n") },
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, ch := range chunk.Choices {
			if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
				hadReasoning = true
				if !reasoningDim {
					pw.WriteString(colorDim)
					reasoningDim = true
				}
				pw.WriteString(*ch.Delta.ReasoningContent)
			}

			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				if reasoningDim {
					pw.WriteString(colorReset + "\n")
					reasoningDim = false
				}
				contentBuf.WriteString(*ch.Delta.Content)
				if hadReasoning {
					pw.WriteString(*ch.Delta.Content)
				} else {
					filter.process(*ch.Delta.Content)
				}
			}
		}
	}

	filter.flush()
	if reasoningDim {
		pw.WriteString(colorReset + "\n")
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("stream error: %w", err)
	}

	return stripThinkTags(contentBuf.String()), nil
}
