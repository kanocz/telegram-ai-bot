package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-webfetch/tools"
)

type limitConfig struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type modelConfig struct {
	Name    string      `json:"name"`
	BaseURL string      `json:"baseURL"`
	Limit   limitConfig `json:"limit"`
}

type appConfig struct {
	Model    map[string]modelConfig `json:"model"`
	Language string                 `json:"language"`
}

func loadConfig(path string) (modelID string, cfg modelConfig, language string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", modelConfig{}, "", err
	}

	// Try new format: {"model": {...}, "language": "..."}
	var ac appConfig
	if err := json.Unmarshal(data, &ac); err != nil {
		return "", modelConfig{}, "", err
	}

	if len(ac.Model) > 0 {
		for id, c := range ac.Model {
			return id, c, ac.Language, nil
		}
	}

	// Fallback: old flat format {"modelId": {...}}
	var flat map[string]modelConfig
	if err := json.Unmarshal(data, &flat); err != nil {
		return "", modelConfig{}, "", err
	}
	for id, c := range flat {
		return id, c, "", nil
	}
	return "", modelConfig{}, "", fmt.Errorf("no models defined in config")
}

func main() {
	// Flags parsed below; signal handler set up after flag.Parse
	// so we can check -telegram-bot.

	noThink := flag.Bool("no-think", false, "hide model thinking output")
	enableThinkingFlag := flag.Bool("enable-thinking", false, "explicitly enable model thinking/reasoning")
	thinkFlag := flag.Bool("disable-thinking", false, "disable model thinking/reasoning")
	showSubAgents := flag.Bool("show-subagents", false, "show sub-agent input, thinking, and output")
	verboseTools := flag.Bool("verbose-tools", false, "show tool call arguments and results")
	requestDebugFlag := flag.Bool("request-debug", false, "dump API request JSON to stderr (base64 data truncated)")
	mailSummary := flag.Bool("mail-summary", false, "standalone mail digest: fetch unread, group by sender, categorize")
	newsSummary := flag.Bool("news-summary", false, "cross-referenced news digest from configured URLs")
	newsConfig := flag.String("news-config", "news.json", "path to news config file (JSON with categories)")
	telegram := flag.Bool("telegram", false, "send output to Telegram instead of stdout")
	telegramCfgPath := flag.String("telegram-config", "telegram.json", "path to telegram config file")
	telegramChatID := flag.Int64("telegram-chatid", 0, "override Telegram chat ID for this invocation")
	telegramBot := flag.Bool("telegram-bot", false, "run as Telegram webhook bot service")
	quiet := flag.Bool("quiet", false, "suppress all non-error output (for cron)")
	configPath := flag.String("config", "config.json", "path to config file")
	languageFlag := flag.String("language", "", "response language (overrides config)")
	exportDefaultPrompts := flag.String("export-default-prompts", "", "export default prompts to directory and exit")
	promptsDir := flag.String("prompts-dir", "", "load prompts from directory (missing files use defaults)")
	enableMCP := flag.String("enable-mcp", "", "activate MCP servers by name (comma-separated)")
	mcpConfigPath := flag.String("mcp-config", "mcp.json", "path to MCP server config file")
	imageFile := flag.String("image", "", "path to image file to attach to query (vision)")
	videoFile := flag.String("video", "", "path to video file to attach to query (vision)")
	filesystemRoot := flag.String("filesystem", "", "enable filesystem tools sandboxed to this directory")
	filesystemRW := flag.Bool("filesystem-rw", false, "enable write filesystem tools (requires -filesystem)")
	gitFlag := flag.Bool("git", false, "enable git history tools (repo = -filesystem dir, or cwd)")
	gitDir := flag.String("git-dir", "", "enable git history tools on this repo (implies -git)")
	userFlag := flag.String("user", "", "user name from users.json (auto-selects if only one user)")
	skillsFlag := flag.String("skills", "", "activate skills by name (comma-separated)")
	skillsDirFlag := flag.String("skills-dir", "", "override skills directory (default: ~/.claude/skills)")
	noAsk := flag.Bool("no-ask", false, "disable interactive ask_user tool (for cron/scripting)")
	memoryFlag := flag.String("memory", "", "enable memory tools at this path (\"off\" to disable even if set in users.json)")
	flag.Parse()

	requestDebug = *requestDebugFlag
	quietMode = *quiet

	// Register filesystem and git tools
	if *filesystemRoot != "" {
		tools.RegisterFilesystem(*filesystemRoot, *filesystemRW)
	}
	if *gitDir != "" || *gitFlag {
		repoPath := *gitDir
		if repoPath == "" {
			if *filesystemRoot != "" {
				repoPath = *filesystemRoot
			} else {
				repoPath, _ = os.Getwd()
			}
		}
		tools.RegisterGit(repoPath)
	}

	// Reset terminal colors on Ctrl+C (interactive mode only)
	if !*telegramBot {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		go func() {
			<-sig
			fmt.Fprint(os.Stderr, colorReset+"\n")
			os.Exit(130)
		}()
	}

	// Export default prompts and exit (before usage check)
	if *exportDefaultPrompts != "" {
		if err := exportPrompts(*exportDefaultPrompts); err != nil {
			fmt.Fprintf(os.Stderr, "export prompts error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Prompts exported to %s\n", *exportDefaultPrompts)
		os.Exit(0)
	}

	if !*mailSummary && !*newsSummary && !*telegramBot && flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <query>\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Load telegram config early (fail fast)
	var tgCfg *telegramConfig
	if *telegram || *telegramBot {
		var err error
		tgCfg, err = loadTelegramConfig(*telegramCfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "telegram config error: %v\n", err)
			os.Exit(1)
		}
	}

	// Content output: stdout normally, discard for telegram/quiet
	var contentOut io.Writer = os.Stdout
	if *telegram || *telegramBot || *quiet {
		contentOut = io.Discard
	}

	// logf prints non-error info to stderr; suppressed in quiet mode
	logf := func(format string, args ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format, args...)
		}
	}

	query := strings.Join(flag.Args(), " ")

	modelID, cfg, configLanguage, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Resolve base language: CLI flag > config > default
	// (user language is applied later, after user resolution)
	language := "русский"
	if configLanguage != "" {
		language = configLanguage
	}
	if *languageFlag != "" {
		language = *languageFlag
	}

	// Load prompts (language applied after user resolution below)
	var prompts Prompts
	if *promptsDir != "" {
		prompts, err = loadPrompts(*promptsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load prompts error: %v\n", err)
			os.Exit(1)
		}
	} else {
		prompts = defaultPrompts()
	}

	// Parse /think and /nothink prefixes from query (before /mcp)
	thinkPrefix, query := parseThinkPrefix(query)
	noThinkPrefix, query := parseNothinkPrefix(query)

	// Determine thinking mode: explicit enable > explicit disable > default
	think := thinkDefault
	switch {
	case *enableThinkingFlag || thinkPrefix:
		think = thinkEnable
	case *thinkFlag || noThinkPrefix:
		think = thinkDisable
	}

	// Parse /skills prefix and merge with flag names
	skillsPrefixNames, query := parseSkillsPrefix(query)
	var flagSkillNames []string
	if *skillsFlag != "" {
		for _, n := range strings.Split(*skillsFlag, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				flagSkillNames = append(flagSkillNames, n)
			}
		}
	}
	skillNames := dedup(flagSkillNames, skillsPrefixNames)

	// Skill shortcut: "/reminder do something" → load skill "reminder"
	var skillDirs []string
	if *skillsDirFlag != "" {
		skillDirs = []string{*skillsDirFlag}
	} else {
		skillDirs = skillSearchDirs()
	}
	if shortcutName, shortcutQuery := parseSkillShortcut(query, skillDirs); shortcutName != "" {
		skillNames = dedup(skillNames, []string{shortcutName})
		query = shortcutQuery
	}

	if len(skillNames) > 0 {
		skillText, err := loadSkills(skillDirs, skillNames)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skills error: %v\n", err)
			os.Exit(1)
		}
		prompts.SystemPrompt += skillText
	}

	showThinking := !*noThink && !*quiet
	if think == thinkDisable {
		showThinking = false
	}

	// Load MCP config (optional — nil if no config file)
	var mcpMgr *MCPManager
	if _, err := os.Stat(*mcpConfigPath); err == nil {
		mcpMgr, err = LoadMCPConfig(*mcpConfigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp config error: %v\n", err)
			os.Exit(1)
		}
		mcpMgr.InitEnabled(logf)
	}

	// Parse -enable-mcp flag names
	var flagMCPNames []string
	if *enableMCP != "" {
		for _, n := range strings.Split(*enableMCP, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				flagMCPNames = append(flagMCPNames, n)
			}
		}
		if mcpMgr == nil {
			fmt.Fprintf(os.Stderr, "error: -enable-mcp used but %s not found\n", *mcpConfigPath)
			os.Exit(1)
		}
		if err := mcpMgr.InitServers(flagMCPNames); err != nil {
			fmt.Fprintf(os.Stderr, "mcp init error: %v\n", err)
			os.Exit(1)
		}
	}

	// Set up sub-agent function for tools that need AI processing
	showSA := *showSubAgents && !*quiet
	tools.SubAgentFn = func(systemPrompt, userMessage string) (string, error) {
		tools.SubAgentDepth.Add(1)
		defer tools.SubAgentDepth.Add(-1)

		msgs := []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		}

		if showSA {
			prefix := strings.Repeat(" | ", int(tools.SubAgentDepth.Load()))
			pw := &prefixWriter{w: os.Stderr, prefix: prefix, bol: true}

			pw.WriteString(colorCyan + "--- sub-agent ---" + colorReset + "\n")
			pw.WriteString(colorDim + "System: " + systemPrompt + colorReset + "\n")
			input := userMessage
			if len(input) > 300 {
				input = input[:300] + "..."
			}
			pw.WriteString(colorDim + "Input: " + input + colorReset + "\n")
			pw.WriteString("\n")

			result, err := doSubAgentStream(cfg.BaseURL, modelID, msgs, cfg.Limit.Output, pw, think)
			if err != nil {
				return "", err
			}

			pw.WriteString("\n" + colorCyan + "--- end sub-agent ---" + colorReset + "\n")
			return result, nil
		}

		return doChat(cfg.BaseURL, modelID, msgs, cfg.Limit.Output, cfg.Limit.Context, think)
	}

	// Resolve user from users.json
	var user *UserConfig
	users := getUsers()
	if *userFlag != "" {
		var err error
		user, err = resolveUserByName(users, *userFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "user error: %v\n", err)
			os.Exit(1)
		}
	} else if len(users) == 1 {
		for _, u := range users {
			user = u
		}
	}

	// Apply per-user overrides for CLI
	if user != nil {
		if imapCfg := userImapConfig(user); imapCfg != nil {
			tools.SetImapOverride(imapCfg)
		}
		haEnabled := user.HA != nil && user.HA.Enabled
		tools.SetHAEnabled(haEnabled)
		if calCfg := userCalendarConfig(user); calCfg != nil {
			tools.SetCalendarOverride(calCfg)
		}
		if contactsCfg := userContactsConfig(user); contactsCfg != nil {
			tools.SetContactsOverride(contactsCfg)
		}
		// User language (overridden by CLI flag)
		if user.Language != "" && *languageFlag == "" {
			language = user.Language
		}
	}

	// Determine memory path: user config, overridden by -memory flag
	memoryPath := ""
	if user != nil && user.Memory != "" {
		memoryPath = user.Memory
	}
	if *memoryFlag != "" {
		if *memoryFlag == "off" {
			memoryPath = ""
		} else {
			memoryPath = *memoryFlag
		}
	}
	if memoryPath != "" {
		tools.SetMemoryOverride(memoryPath)
		defer tools.ClearMemoryOverride()
	}

	// Save prompt template before language application (for bot per-user language)
	promptsTemplate := prompts

	// Now apply language to prompts (after user resolution)
	applyLanguage(&prompts, language)
	installToolPrompts(&prompts)

	// Inject current time into system prompt so the model knows "now"
	now := time.Now()
	zone, _ := now.Zone()
	timeStr := fmt.Sprintf("\n\nCurrent time: %s (%s).",
		now.Format("2006-01-02 15:04"), zone)
	prompts.SystemPrompt += timeStr
	promptsTemplate.SystemPrompt += timeStr

	// Compute MCP overrides from user config
	var mcpOverrides map[string]bool
	if user != nil && len(user.MCP) > 0 {
		mcpOverrides = user.MCP
	}

	// Validate: -telegram without -telegram-chatid needs a user for chat routing
	if *telegram && *telegramChatID == 0 && user == nil {
		fmt.Fprintf(os.Stderr, "error: -telegram requires -user or -telegram-chatid\n")
		os.Exit(1)
	}

	// Parse /mcp prefix from query and merge with flag names
	prefixNames, query := parseMCPPrefix(query)
	mcpNames := dedup(flagMCPNames, prefixNames)
	if len(mcpNames) > 0 {
		if mcpMgr == nil {
			fmt.Fprintf(os.Stderr, "error: /mcp prefix used but %s not found\n", *mcpConfigPath)
			os.Exit(1)
		}
		if err := mcpMgr.InitServers(mcpNames); err != nil {
			fmt.Fprintf(os.Stderr, "mcp init error: %v\n", err)
			os.Exit(1)
		}
	}

	if *mailSummary {
		content, err := runMailSummary(cfg, modelID, showThinking, contentOut, logf, &prompts, 24, mcpMgr, mcpNames, think, mcpOverrides)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mail summary error: %v\n", err)
			os.Exit(1)
		}
		if *telegram {
			chatID := userChatID(user, "mail", *telegramChatID)
			logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
			if err := sendToChat(tgCfg.Token, chatID, stripThinkTags(content)); err != nil {
				fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
				os.Exit(1)
			}
			logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(content), colorReset)
		}
		return
	}

	if *newsSummary {
		content, err := runNewsSummary(cfg, modelID, showThinking, contentOut, logf, *newsConfig, &prompts, mcpMgr, mcpNames, think, mcpOverrides)
		if err != nil {
			fmt.Fprintf(os.Stderr, "news summary error: %v\n", err)
			os.Exit(1)
		}
		if *telegram {
			chatID := userChatID(user, "news", *telegramChatID)
			logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
			if err := sendToChat(tgCfg.Token, chatID, stripThinkTags(content)); err != nil {
				fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
				os.Exit(1)
			}
			logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(content), colorReset)
		}
		return
	}

	if *telegramBot {
		if err := runBot(tgCfg, cfg, modelID, showThinking, logf, &promptsTemplate, language, *verboseTools, *newsConfig, mcpMgr, think); err != nil {
			fmt.Fprintf(os.Stderr, "bot error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	defer tools.HAClose()

	var images []ImageURL
	if *imageFile != "" {
		dataURL, imgErr := loadFileDataURL(*imageFile)
		if imgErr != nil {
			fmt.Fprintf(os.Stderr, "image error: %v\n", imgErr)
			os.Exit(1)
		}
		images = append(images, ImageURL{URL: dataURL})
	}

	var videos []VideoURL
	if *videoFile != "" {
		dataURL, vidErr := loadFileDataURL(*videoFile)
		if vidErr != nil {
			fmt.Fprintf(os.Stderr, "video error: %v\n", vidErr)
			os.Exit(1)
		}
		videos = append(videos, VideoURL{URL: dataURL})
	}

	// Enable ask_user in interactive CLI mode (not telegram, quiet, mail, news, or -no-ask)
	if !*noAsk && !*telegram && !*quiet {
		tools.SetPrompter(&CLIPrompter{})
		defer tools.ClearPrompter()
		prompts.SystemPrompt += AskUserPromptHint
	}

	if memoryPath != "" {
		prompts.SystemPrompt += MemoryPromptHint
	}

	finalContent, err := runQuery(cfg, modelID, query, showThinking, *verboseTools, contentOut, logf, &prompts, mcpMgr, mcpNames, think, images, videos, nil, mcpOverrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}

	if *telegram {
		chatID := userChatID(user, "other", *telegramChatID)
		logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
		if err := sendToChat(tgCfg.Token, chatID, stripThinkTags(finalContent)); err != nil {
			fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
			os.Exit(1)
		}
		logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(finalContent), colorReset)
	}
}

func runQuery(cfg modelConfig, modelID string, query string,
	showThinking, verboseTools bool, contentOut io.Writer,
	logf func(string, ...any), prompts *Prompts,
	mcpMgr *MCPManager, mcpNames []string, think thinkMode,
	images []ImageURL, videos []VideoURL, history []Message,
	mcpOverrides map[string]bool) (string, error) {

	// Merge built-in + MCP tool definitions
	toolDefs := tools.All()
	if mcpMgr != nil {
		toolDefs = append(toolDefs, mcpMgr.ActiveToolDefs(mcpNames, mcpOverrides)...)
	}
	execTool := makeToolExec(mcpMgr, mcpNames)

	userMsg := Message{Role: "user", Content: query, Images: images, Videos: videos}
	messages := []Message{
		{Role: "system", Content: prompts.SystemPrompt},
	}
	messages = append(messages, history...)
	messages = append(messages, userMsg)

	for {
		result, err := doStream(cfg.BaseURL, modelID, messages, toolDefs, cfg.Limit.Output, showThinking, contentOut, think)
		if err != nil {
			return "", err
		}

		if len(result.ToolCalls) == 0 {
			fmt.Fprintln(contentOut)
			return result.Content, nil
		}

		messages = append(messages, Message{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		})

		for _, tc := range result.ToolCalls {
			if verboseTools {
				logf("%s[tool: %s]%s\n", colorCyan, tc.Function.Name, colorReset)
				logf("%s  args: %s%s\n", colorDim, tc.Function.Arguments, colorReset)
			} else {
				logf("%s[tool: %s(%s)]%s\n",
					colorCyan, tc.Function.Name, tc.Function.Arguments, colorReset)
			}

			res, execErr := execTool(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			var toolResult string
			if execErr != nil {
				toolResult = "error: " + execErr.Error()
			} else {
				toolResult = res
			}

			// Check if the tool produced images (e.g. camera snapshot)
			var toolImages []ImageURL
			if imgURIs := tools.TakePendingImages(); len(imgURIs) > 0 {
				for _, uri := range imgURIs {
					toolImages = append(toolImages, ImageURL{URL: uri})
				}
			}

			if verboseTools {
				preview := toolResult
				if len(preview) > 500 {
					preview = preview[:500] + "..."
				}
				logf("%s  result: %s%s\n", colorDim, preview, colorReset)
				if len(toolImages) > 0 {
					logf("%s  images: %d%s\n", colorDim, len(toolImages), colorReset)
				}
			}

			messages = append(messages, Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
				Images:     toolImages,
			})
		}
	}
}

func runMailSummary(cfg modelConfig, modelID string, showThinking bool, contentOut io.Writer, logf func(string, ...any), prompts *Prompts, sinceHours float64, mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool) (string, error) {
	defer tools.ClearTempMemory()

	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	progress("Получение непрочитанных писем...")

	groups, err := tools.FetchUnreadGrouped(tools.MailDigestConfig{
		SinceHours: sinceHours,
		ProgressFn: progress,
	})
	if err != nil {
		return "", fmt.Errorf("fetch unread: %w", err)
	}
	if len(groups) == 0 {
		msg := "Нет непрочитанных писем за последние 24 часа."
		fmt.Fprintln(contentOut, msg)
		return msg, nil
	}

	// Per group: run sub-agent digest
	progress(fmt.Sprintf("Анализ %d групп через суб-агентов...", len(groups)))
	for i := range groups {
		g := &groups[i]
		label := g.SenderName
		if label == "" {
			label = g.SenderAddr
		}
		progress(fmt.Sprintf("  [%d/%d] %s...", i+1, len(groups), label))

		input := buildGroupDigestInput(g)
		// Inject persistent memory context about the sender
		if memCtx := tools.MemoryLookup(g.SenderAddr); memCtx != "" {
			input += "\n\n=== MEMORY ===\n" + memCtx
		}
		digest, err := tools.SubAgentFn(prompts.MailDigestSubAgent, input)
		if err != nil {
			progress(fmt.Sprintf("    ошибка: %v", err))
			g.Digest = fmt.Sprintf("(ошибка анализа: %v)", err)
			continue
		}
		g.Digest = digest
	}

	// Build final prompt with all digests
	var sb strings.Builder
	for i, g := range groups {
		label := g.SenderName
		if label == "" {
			label = g.SenderAddr
		}
		sb.WriteString(fmt.Sprintf("=== Отправитель %d: %s <%s> (%d писем) ===\n",
			i+1, label, g.SenderAddr, len(g.Emails)))
		sb.WriteString(g.Digest)
		sb.WriteString("\n\n")
	}

	finalInput := sb.String()
	if len(finalInput) > 60000 {
		finalInput = finalInput[:60000] + "\n[...truncated]"
	}

	progress("Финальная категоризация...")

	messages := []Message{
		{Role: "system", Content: prompts.MailDigestFinal},
		{Role: "user", Content: finalInput},
	}

	// Merge built-in + MCP tools for final synthesis
	toolDefs := tools.All()
	execTool := makeToolExec(mcpMgr, mcpNames)
	if mcpMgr != nil && (len(mcpNames) > 0 || len(mcpOverrides) > 0) {
		toolDefs = append(toolDefs, mcpMgr.ActiveToolDefs(mcpNames, mcpOverrides)...)
	}

	for {
		result, err := doStream(cfg.BaseURL, modelID, messages, toolDefs, cfg.Limit.Output, showThinking, contentOut, think)
		if err != nil {
			return "", fmt.Errorf("final synthesis: %w", err)
		}

		if len(result.ToolCalls) == 0 {
			fmt.Fprintln(contentOut)
			return result.Content, nil
		}

		messages = append(messages, Message{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		})

		for _, tc := range result.ToolCalls {
			logf("%s[tool: %s]%s\n", colorCyan, tc.Function.Name, colorReset)
			res, execErr := execTool(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			var toolResult string
			if execErr != nil {
				toolResult = "error: " + execErr.Error()
			} else {
				toolResult = res
			}
			messages = append(messages, Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			})
		}
	}
}

func buildGroupDigestInput(g *tools.SenderGroup) string {
	var sb strings.Builder

	for i, e := range g.Emails {
		sb.WriteString(fmt.Sprintf("--- Письмо %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("From: %s\nTo: %s\nDate: %s\nSubject: %s\n\n",
			e.From, e.To, e.Date, e.Subject))
		sb.WriteString(e.Body)
		sb.WriteString("\n\n")
	}

	if len(g.History) > 0 {
		sb.WriteString("=== ИСТОРИЯ ПЕРЕПИСКИ ===\n")
		for _, r := range g.History {
			sb.WriteString(fmt.Sprintf("%s | From: %s | To: %s | Subject: %s\n",
				r.Date, r.From, r.To, r.Subject))
		}
	}

	content := sb.String()
	if len(content) > 60000 {
		content = content[:60000] + "\n[...truncated]"
	}
	return content
}

// loadFileDataURL reads a file and returns a data URI (data:<mime>;base64,...).
// MIME is determined from the file extension first (more reliable for video),
// falling back to http.DetectContentType.
func loadFileDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file %s: %w", path, err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64), nil
}

// CLIPrompter implements tools.UserPrompter for interactive CLI sessions.
// It prints the question and numbered options to stderr, reads from stdin.
type CLIPrompter struct{}

func (p *CLIPrompter) Ask(q tools.UserQuestion) (string, error) {
	fmt.Fprintf(os.Stderr, "\n%s%s%s\n", colorBold, q.Question, colorReset)

	if len(q.Options) > 0 {
		for i, opt := range q.Options {
			if opt.Description != "" {
				fmt.Fprintf(os.Stderr, "  %s%d)%s %s — %s\n", colorCyan, i+1, colorReset, opt.Label, opt.Description)
			} else {
				fmt.Fprintf(os.Stderr, "  %s%d)%s %s\n", colorCyan, i+1, colorReset, opt.Label)
			}
		}
		if q.MultiSelect {
			fmt.Fprintf(os.Stderr, "%sВведите номера через запятую или свой ответ: %s", colorDim, colorReset)
		} else {
			fmt.Fprintf(os.Stderr, "%sВведите номер или свой ответ: %s", colorDim, colorReset)
		}
	} else {
		fmt.Fprintf(os.Stderr, "%sВведите ответ: %s", colorDim, colorReset)
	}

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return "", fmt.Errorf("no input (EOF)")
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" {
		return "", fmt.Errorf("empty input")
	}

	if len(q.Options) == 0 {
		return input, nil
	}

	if q.MultiSelect {
		// Parse comma-separated numbers
		parts := strings.Split(input, ",")
		var labels []string
		allNumeric := true
		for _, part := range parts {
			part = strings.TrimSpace(part)
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > len(q.Options) {
				allNumeric = false
				break
			}
			labels = append(labels, q.Options[n-1].Label)
		}
		if allNumeric && len(labels) > 0 {
			data, _ := json.Marshal(labels)
			return string(data), nil
		}
		// Not numeric — return raw input
		return input, nil
	}

	// Single select: try to parse as number
	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= len(q.Options) {
		return q.Options[n-1].Label, nil
	}
	// Not a number — return as custom answer
	return input, nil
}

// dedup merges two name lists, removing duplicates.
func dedup(a, b []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
