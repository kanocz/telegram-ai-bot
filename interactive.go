package main

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ai-webfetch/tools"

	"github.com/chzyer/readline"
)

const compactPrompt = `Compress the following dialogue into a concise summary (no more than 2000 words), preserving:
- Key facts, numbers, names
- Conclusions and analysis
- Context of discussed topics
- User requests and what data was retrieved

This will be used as context for continuing the conversation. Be concise, no preamble.`

// interactiveConfig holds settings for the interactive REPL.
type interactiveConfig struct {
	Cfg            modelConfig
	ModelID        string
	ShowThinking   bool
	VerboseTools   bool
	Logf           func(string, ...any)
	Prompts        *Prompts
	NewsConfigPath string
	McpMgr         *MCPManager
	McpNames       []string
	Think          thinkMode
	McpOverrides   map[string]bool
	// Mode controls the prompt and banner.
	// "dot" = cwd mode, "news" = news-focused, "" = general-purpose.
	Mode string
	// SkillNames lists active skills (for userinfo prompt block).
	SkillNames []string
}

// expandedInput holds the result of expanding @file references.
type expandedInput struct {
	Query  string
	Images []ImageURL
}

// runInteractive starts an interactive REPL session.
// If initialQuery is non-empty, it's processed first; then the loop waits for more input.
func runInteractive(ic interactiveConfig, initialQuery string) error {
	// Set up CLI prompter
	tools.SetPrompter(&CLIPrompter{})
	defer tools.ClearPrompter()

	printBanner(ic)

	prompt := "> "
	if ic.Mode == "news" {
		prompt = "news> "
	}

	// Set up readline with @file auto-completion
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          colorBold + prompt + colorReset,
		AutoComplete:    newInteractiveCompleter(),
		InterruptPrompt: "^C",
		EOFPrompt:       "/exit",
		Stderr:          os.Stderr,
		Stdout:          os.Stderr, // prompts and completions go to stderr
	})
	if err != nil {
		return fmt.Errorf("readline init: %w", err)
	}
	defer rl.Close()

	var history []Message
	query := initialQuery

	for {
		if query == "" {
			line, err := rl.Readline()
			if err != nil {
				// EOF or interrupt
				break
			}
			query = strings.TrimSpace(line)
			if query == "" {
				continue
			}
		}

		// Exit commands
		if query == "/exit" || query == "/quit" || query == "/q" {
			fmt.Fprintf(os.Stderr, "%sBye.%s\n", colorDim, colorReset)
			break
		}

		// Compact command
		if query == "/compact" {
			compacted, err := compactHistory(ic.Cfg, ic.ModelID, history, ic.Logf, ic.Think)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%sCompact error: %v%s\n", colorCyan, err, colorReset)
			} else {
				history = compacted
				printContextUsage(ic.Cfg, ic.Prompts, history)
			}
			query = ""
			continue
		}

		// Help command
		if query == "/help" || query == "/?" {
			printHelp(ic)
			query = ""
			continue
		}

		// Expand @file references (text + images)
		expanded := expandFileRefs(query)

		// Dispatch: /news command or general query
		switch {
		case expanded.Query == "/news" || strings.HasPrefix(expanded.Query, "/news "):
			result, err := processNewsCommand(expanded.Query, ic.Cfg, ic.ModelID, ic.ShowThinking, ic.Logf,
				ic.Prompts, ic.NewsConfigPath, ic.McpMgr, ic.McpNames, ic.Think, ic.McpOverrides)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n%sError: %v%s\n", colorCyan, err, colorReset)
			} else {
				history = append(history, Message{Role: "user", Content: expanded.Query})
				history = append(history, Message{Role: "assistant", Content: result})
			}

		default:
			// General query — use LLM with full conversation history
			history = append(history, Message{Role: "user", Content: expanded.Query, Images: expanded.Images})
			activeModules := append(append([]string{}, ic.SkillNames...), ic.McpNames...)
			result, err := runQuery(ic.Cfg, ic.ModelID, expanded.Query, ic.ShowThinking, ic.VerboseTools,
				os.Stdout, ic.Logf, ic.Prompts, ic.McpMgr, ic.McpNames, ic.Think,
				expanded.Images, nil, history[:len(history)-1], ic.McpOverrides, activeModules)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n%sError: %v%s\n", colorCyan, err, colorReset)
				history = history[:len(history)-1] // remove failed user message
			} else {
				history = append(history, Message{Role: "assistant", Content: result})
			}
		}

		// Show context usage
		printContextUsage(ic.Cfg, ic.Prompts, history)

		// Auto-compact if >80% context used
		if ic.Cfg.Limit.Context > 0 {
			allMsgs := append([]Message{{Role: "system", Content: ic.Prompts.SystemPrompt}}, history...)
			tokens := estimateTokens(allMsgs)
			pct := tokens * 100 / ic.Cfg.Limit.Context
			if pct > 80 {
				fmt.Fprintf(os.Stderr, "\n%s⚠ Context > 80%%, auto-compacting...%s\n",
					colorCyan, colorReset)
				compacted, err := compactHistory(ic.Cfg, ic.ModelID, history, ic.Logf, ic.Think)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%sAuto-compact error: %v%s\n", colorCyan, err, colorReset)
				} else {
					history = compacted
					printContextUsage(ic.Cfg, ic.Prompts, history)
				}
			}
		}

		query = "" // loop for next input
	}

	return nil
}

// --- Readline auto-completion ---

// interactiveCompleter provides tab completion for the REPL.
type interactiveCompleter struct{}

func newInteractiveCompleter() *interactiveCompleter {
	return &interactiveCompleter{}
}

func (c *interactiveCompleter) Do(line []rune, pos int) ([][]rune, int) {
	lineStr := string(line[:pos])

	// Find the last token being typed
	lastSpace := strings.LastIndexByte(lineStr, ' ')
	var prefix string
	if lastSpace >= 0 {
		prefix = lineStr[lastSpace+1:]
	} else {
		prefix = lineStr
	}

	// @ file completion
	if strings.HasPrefix(prefix, "@") {
		return completeFilePath(prefix[1:], len(prefix)-1)
	}

	// / command completion (only at start of line)
	if strings.HasPrefix(prefix, "/") && lastSpace < 0 {
		return completeCommand(prefix)
	}

	return nil, 0
}

func completeFilePath(partial string, prefixLen int) ([][]rune, int) {
	// Handle quoted paths: @"partial
	quoted := false
	if strings.HasPrefix(partial, "\"") {
		quoted = true
		partial = partial[1:]
		prefixLen++ // account for the quote
	}

	// Determine directory and file prefix
	dir := filepath.Dir(partial)
	base := filepath.Base(partial)
	if partial == "" || strings.HasSuffix(partial, "/") {
		dir = partial
		if dir == "" {
			dir = "."
		}
		base = ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}

	var candidates [][]rune
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue // skip hidden files unless user typed a dot
		}
		if base != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}

		completion := name[len(base):]
		if e.IsDir() {
			completion += "/"
		} else if quoted {
			completion += "\""
		}

		// Add space after non-dir completions (unless quoted, then after closing quote)
		if !e.IsDir() && !quoted {
			completion += " "
		}

		candidates = append(candidates, []rune(completion))
	}

	return candidates, 0
}

func completeCommand(prefix string) ([][]rune, int) {
	commands := []string{"/news", "/compact", "/help", "/exit", "/quit"}
	var candidates [][]rune
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, prefix) {
			candidates = append(candidates, []rune(cmd[len(prefix):]+" "))
		}
	}
	return candidates, 0
}

// --- File expansion with binary support ---

// isImageMIME returns true if the MIME type is a supported image format.
func isImageMIME(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/tiff":
		return true
	}
	return false
}

// isTextFile heuristically determines if a file is text.
// Checks MIME type first, then scans for null bytes.
func isTextFile(path string, data []byte) bool {
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType != "" {
		if strings.HasPrefix(mimeType, "text/") {
			return true
		}
		if isImageMIME(mimeType) || strings.HasPrefix(mimeType, "audio/") || strings.HasPrefix(mimeType, "video/") {
			return false
		}
	}

	// Known text extensions without registered MIME types
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".rs", ".rb", ".php", ".sh", ".bash", ".zsh",
		".ts", ".tsx", ".jsx", ".vue", ".svelte",
		".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf",
		".md", ".rst", ".txt", ".log",
		".json", ".xml", ".csv", ".tsv",
		".sql", ".graphql", ".proto",
		".dockerfile", ".gitignore", ".env",
		".c", ".h", ".cpp", ".hpp", ".java", ".kt", ".swift",
		".r", ".m", ".mm", ".pl", ".lua", ".dart", ".ex", ".exs",
		".tf", ".hcl", ".nix", ".zig", ".nim", ".v":
		return true
	case "": // no extension — check content
	default:
		// For known binary types detected by content
		detected := http.DetectContentType(data)
		if strings.HasPrefix(detected, "text/") {
			return true
		}
		if detected == "application/octet-stream" {
			// Fall through to null-byte check
		} else {
			return false
		}
	}

	// Check for null bytes in first 8KB
	check := data
	if len(check) > 8192 {
		check = check[:8192]
	}
	for _, b := range check {
		if b == 0 {
			return false
		}
	}
	return true
}

// expandFileRefs finds @path tokens in the input and expands them.
// Text files are embedded inline; images become ImageURL attachments;
// other binary files are base64-encoded inline.
func expandFileRefs(input string) expandedInput {
	var result strings.Builder
	var images []ImageURL
	i := 0

	for i < len(input) {
		if input[i] != '@' || (i > 0 && input[i-1] != ' ') {
			result.WriteByte(input[i])
			i++
			continue
		}

		i++ // skip @
		if i >= len(input) {
			result.WriteByte('@')
			break
		}

		var path string
		if input[i] == '"' {
			i++ // skip opening "
			end := strings.IndexByte(input[i:], '"')
			if end < 0 {
				path = input[i:]
				i = len(input)
			} else {
				path = input[i : i+end]
				i += end + 1
			}
		} else {
			end := strings.IndexByte(input[i:], ' ')
			if end < 0 {
				path = input[i:]
				i = len(input)
			} else {
				path = input[i : i+end]
				i += end
			}
		}

		if path == "" {
			result.WriteByte('@')
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			result.WriteString("@" + path)
			fmt.Fprintf(os.Stderr, "%s⚠ File not found: %s%s\n", colorDim, path, colorReset)
			continue
		}

		// Determine file type
		mimeType := mime.TypeByExtension(filepath.Ext(path))
		if mimeType == "" {
			mimeType = http.DetectContentType(data)
		}

		if isImageMIME(mimeType) {
			// Image → attach as vision input
			b64 := base64.StdEncoding.EncodeToString(data)
			dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)
			images = append(images, ImageURL{URL: dataURI})
			fmt.Fprintf(os.Stderr, "%s🖼 %s (%s, %d bytes) — attached as image%s\n",
				colorDim, path, mimeType, len(data), colorReset)
			result.WriteString(fmt.Sprintf("[Image: %s]", filepath.Base(path)))
		} else if isTextFile(path, data) {
			// Text → embed inline
			fmt.Fprintf(os.Stderr, "%s📎 %s (%d bytes)%s\n", colorDim, path, len(data), colorReset)
			result.WriteString(fmt.Sprintf("\n--- File: %s ---\n%s\n--- End of file ---\n", path, string(data)))
		} else {
			// Other binary → base64 inline with MIME info
			b64 := base64.StdEncoding.EncodeToString(data)
			fmt.Fprintf(os.Stderr, "%s📦 %s (%s, %d bytes) — attached as base64%s\n",
				colorDim, path, mimeType, len(data), colorReset)
			// Truncate very large base64 to avoid blowing up context
			preview := b64
			if len(preview) > 10000 {
				preview = preview[:10000] + "...[truncated]"
			}
			result.WriteString(fmt.Sprintf("\n--- File (binary): %s (%s, %d bytes) ---\nbase64: %s\n--- End of file ---\n",
				path, mimeType, len(data), preview))
		}
	}

	return expandedInput{
		Query:  result.String(),
		Images: images,
	}
}

// --- Banners and help ---

func printBanner(ic interactiveConfig) {
	if ic.Mode == "dot" {
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stderr, "%s╔══════════════════════════════════════════╗%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║  Interactive mode (working directory)    ║%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╠══════════════════════════════════════════╣%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s 📁 %s%s\n", colorCyan, colorReset, cwd, "")
		fmt.Fprintf(os.Stderr, "%s║%s Filesystem: read + write                 %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s Git: log, diff, show                     %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╠══════════════════════════════════════════╣%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s <query>           — ask AI                 %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s @file[Tab]        — attach file            %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /news [topic]     — news                   %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /compact          — compact context        %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /help             — help                   %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /exit             — quit                   %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╚══════════════════════════════════════════╝%s\n", colorCyan, colorReset)
	} else if ic.Mode == "news" {
		fmt.Fprintf(os.Stderr, "%s╔══════════════════════════════════════════╗%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║  Interactive news mode                   ║%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╠══════════════════════════════════════════╣%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /news             — full digest           %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /news europe      — browse category       %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /news <topic>     — search by topic       %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s <query>           — follow-up question    %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s @file[Tab]        — attach file           %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /compact          — compact context       %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /exit             — quit                  %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╚══════════════════════════════════════════╝%s\n", colorCyan, colorReset)
	} else {
		fmt.Fprintf(os.Stderr, "%s╔══════════════════════════════════════════╗%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║  Interactive mode                        ║%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╠══════════════════════════════════════════╣%s\n", colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s <query>           — ask AI                %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /news [topic]     — news                  %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s @file[Tab]        — attach file           %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /compact           — compact context      %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /help              — help                 %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s║%s /exit              — quit                 %s║%s\n", colorCyan, colorReset, colorCyan, colorReset)
		fmt.Fprintf(os.Stderr, "%s╚══════════════════════════════════════════╝%s\n", colorCyan, colorReset)
	}

	// Show active integrations
	var active []string
	if ic.McpMgr != nil && (len(ic.McpNames) > 0 || len(ic.McpOverrides) > 0) {
		active = append(active, "MCP")
	}
	if tools.AskAvailable() {
		active = append(active, "ask_user")
	}
	if len(active) > 0 {
		fmt.Fprintf(os.Stderr, "%sActive: %s%s\n", colorDim, strings.Join(active, ", "), colorReset)
	}
	if ic.Cfg.Limit.Context > 0 {
		fmt.Fprintf(os.Stderr, "%sContext limit: %dk tokens (auto-compact at >80%%)%s\n",
			colorDim, ic.Cfg.Limit.Context/1000, colorReset)
	}
}

func printHelp(ic interactiveConfig) {
	fmt.Fprintf(os.Stderr, "\n%sAvailable commands:%s\n", colorBold, colorReset)
	fmt.Fprintf(os.Stderr, "  %s<text>%s            Send query to AI (with conversation history)\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/news%s             Full news digest\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/news <category>%s  Browse category (europe, war, economics...)\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/news <topic>%s     Search by topic\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s@file%s             Attach file to query (Tab for auto-completion)\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s@\"path with spaces\"%s  Attach file with spaces in path\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/compact%s          Compact context (summarize history to save tokens)\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/help%s             This help\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "  %s/exit%s             Quit (or /quit, /q, Ctrl+D)\n", colorCyan, colorReset)
	fmt.Fprintf(os.Stderr, "\n%sFile formats:%s\n", colorBold, colorReset)
	fmt.Fprintf(os.Stderr, "  %sText%s (.go, .py, .md, .json, ...)     — embedded in query\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "  %sImages%s (.png, .jpg, .webp, ...)      — attached as vision\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "  %sOther%s (binary)                       — base64-encoded\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "\n%sTools and MCP are configured via launch flags:%s\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "  %s-enable-mcp github%s   — enable MCP server\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "  %s-skills eat,reminder%s — load skills\n", colorDim, colorReset)
	fmt.Fprintf(os.Stderr, "  %s-memory ./mem%s        — enable persistent memory\n", colorDim, colorReset)
	fmt.Fprintln(os.Stderr)
}

// --- News command dispatcher ---

func processNewsCommand(text string, cfg modelConfig, modelID string,
	showThinking bool, logf func(string, ...any), prompts *Prompts,
	newsConfigPath string, mcpMgr *MCPManager, mcpNames []string,
	think thinkMode, mcpOverrides map[string]bool) (string, error) {

	arg := strings.TrimSpace(strings.TrimPrefix(text, "/news"))
	contentOut := os.Stdout

	if arg == "" {
		return runNewsSummary(cfg, modelID, showThinking, contentOut, logf,
			newsConfigPath, prompts, mcpMgr, mcpNames, think, mcpOverrides)
	}

	categories, err := readNewsConfig(newsConfigPath)
	if err != nil {
		return "", err
	}

	if cat := matchCategory(arg, categories); cat != nil {
		prompter := tools.GetPrompter()
		if prompter == nil {
			return runNewsSummary(cfg, modelID, showThinking, contentOut, logf,
				newsConfigPath, prompts, mcpMgr, mcpNames, think, mcpOverrides)
		}
		return runNewsBrowse(cfg, modelID, cat, showThinking, contentOut, logf,
			prompts, mcpMgr, mcpNames, think, mcpOverrides, prompter)
	}

	return runNewsSearch(cfg, modelID, arg, showThinking, contentOut, logf,
		newsConfigPath, prompts, mcpMgr, mcpNames, think, mcpOverrides)
}

// --- Context tracking ---

func printContextUsage(cfg modelConfig, prompts *Prompts, history []Message) {
	allMsgs := make([]Message, 0, len(history)+1)
	allMsgs = append(allMsgs, Message{Role: "system", Content: prompts.SystemPrompt})
	allMsgs = append(allMsgs, history...)
	tokens := estimateTokens(allMsgs)

	if cfg.Limit.Context > 0 {
		pct := tokens * 100 / cfg.Limit.Context
		bar := contextBar(pct)
		fmt.Fprintf(os.Stderr, "%s[context: ~%dk/%dk tokens %s %d%%]%s\n",
			colorDim, tokens/1000, cfg.Limit.Context/1000, bar, pct, colorReset)
	} else {
		fmt.Fprintf(os.Stderr, "%s[context: ~%dk tokens]%s\n",
			colorDim, tokens/1000, colorReset)
	}
}

func contextBar(pct int) string {
	const width = 20
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// --- Context compaction ---

func compactHistory(cfg modelConfig, modelID string, history []Message,
	logf func(string, ...any), think thinkMode) ([]Message, error) {

	if len(history) == 0 {
		return history, nil
	}

	var sb strings.Builder
	for _, m := range history {
		switch m.Role {
		case "user":
			sb.WriteString("User: ")
		case "assistant":
			sb.WriteString("Assistant: ")
		default:
			sb.WriteString(m.Role + ": ")
		}
		content := m.Content
		if len(content) > 10000 {
			content = content[:10000] + "\n[...truncated]"
		}
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}

	messages := []Message{
		{Role: "system", Content: compactPrompt},
		{Role: "user", Content: sb.String()},
	}

	logf("%sCompacting context (%d messages)...%s\n", colorDim, len(history), colorReset)

	summary, err := doChat(cfg.BaseURL, modelID, messages, cfg.Limit.Output, cfg.Limit.Context, think)
	if err != nil {
		return history, fmt.Errorf("compact LLM call: %w", err)
	}

	compacted := []Message{
		{Role: "assistant", Content: "[Summary of previous conversation]\n\n" + summary},
	}

	beforeTokens := estimateTokens(history)
	afterTokens := estimateTokens(compacted)
	logf("%sCompacted: %dk → %dk tokens (-%d%%)%s\n",
		colorDim, beforeTokens/1000, afterTokens/1000,
		(beforeTokens-afterTokens)*100/max(beforeTokens, 1), colorReset)

	return compacted, nil
}
