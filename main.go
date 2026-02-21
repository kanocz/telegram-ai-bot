package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

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
	showSubAgents := flag.Bool("show-subagents", false, "show sub-agent input, thinking, and output")
	verboseTools := flag.Bool("verbose-tools", false, "show tool call arguments and results")
	mailSummary := flag.Bool("mail-summary", false, "standalone mail digest: fetch unread, group by sender, categorize")
	newsSummary := flag.Bool("news-summary", false, "cross-referenced news digest from configured URLs")
	newsURLs := flag.String("news-urls", "news.urls", "path to file with news URLs (one per line)")
	telegram := flag.Bool("telegram", false, "send output to Telegram instead of stdout")
	telegramCfgPath := flag.String("telegram-config", "telegram.json", "path to telegram config file")
	telegramChatID := flag.Int64("telegram-chatid", 0, "override Telegram chat ID for this invocation")
	telegramBot := flag.Bool("telegram-bot", false, "run as Telegram webhook bot service")
	quiet := flag.Bool("quiet", false, "suppress all non-error output (for cron)")
	configPath := flag.String("config", "config.json", "path to config file")
	languageFlag := flag.String("language", "", "response language (overrides config)")
	exportDefaultPrompts := flag.String("export-default-prompts", "", "export default prompts to directory and exit")
	promptsDir := flag.String("prompts-dir", "", "load prompts from directory (missing files use defaults)")
	flag.Parse()

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
		fmt.Fprintf(os.Stderr, "Usage: %s [-no-think] [-quiet] [-mail-summary] [-news-summary] [-telegram] [-telegram-bot] [-language lang] [-config path] <query>\n", os.Args[0])
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
		// Override chat routing if -telegram-chatid specified
		if *telegramChatID != 0 {
			ids := []int64{*telegramChatID}
			tgCfg.Chats = chatRouting{News: ids, Mail: ids, Other: ids}
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

	// Resolve language: CLI flag > config > default
	language := "русский"
	if configLanguage != "" {
		language = configLanguage
	}
	if *languageFlag != "" {
		language = *languageFlag
	}

	// Load and apply prompts
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
	applyLanguage(&prompts, language)
	installToolPrompts(&prompts)

	showThinking := !*noThink && !*quiet

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

			result, err := doSubAgentStream(cfg.BaseURL, modelID, msgs, cfg.Limit.Output, pw)
			if err != nil {
				return "", err
			}

			pw.WriteString("\n" + colorCyan + "--- end sub-agent ---" + colorReset + "\n")
			return result, nil
		}

		return doChat(cfg.BaseURL, modelID, msgs, cfg.Limit.Output)
	}

	if *mailSummary {
		content, err := runMailSummary(cfg, modelID, showThinking, contentOut, logf, &prompts, 24)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mail summary error: %v\n", err)
			os.Exit(1)
		}
		if *telegram {
			logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
			if err := sendToChats(tgCfg.Token, tgCfg.Chats.Mail, stripThinkTags(content)); err != nil {
				fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
				os.Exit(1)
			}
			logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(content), colorReset)
		}
		return
	}

	if *newsSummary {
		content, err := runNewsSummary(cfg, modelID, showThinking, contentOut, logf, *newsURLs, &prompts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "news summary error: %v\n", err)
			os.Exit(1)
		}
		if *telegram {
			logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
			if err := sendToChats(tgCfg.Token, tgCfg.Chats.News, stripThinkTags(content)); err != nil {
				fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
				os.Exit(1)
			}
			logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(content), colorReset)
		}
		return
	}

	if *telegramBot {
		if err := runBot(tgCfg, cfg, modelID, showThinking, logf, &prompts, *verboseTools, *newsURLs); err != nil {
			fmt.Fprintf(os.Stderr, "bot error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	finalContent, err := runQuery(cfg, modelID, query, showThinking, *verboseTools, contentOut, logf, &prompts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}

	if *telegram {
		logf("%sОтправка в Telegram...%s\n", colorDim, colorReset)
		if err := sendToChats(tgCfg.Token, tgCfg.Chats.Other, stripThinkTags(finalContent)); err != nil {
			fmt.Fprintf(os.Stderr, "telegram error: %v\n", err)
			os.Exit(1)
		}
		logf("%sОтправлено в Telegram (%d символов)%s\n", colorDim, len(finalContent), colorReset)
	}
}

func runQuery(cfg modelConfig, modelID string, query string,
	showThinking, verboseTools bool, contentOut io.Writer,
	logf func(string, ...any), prompts *Prompts) (string, error) {

	messages := []Message{
		{Role: "system", Content: prompts.SystemPrompt},
		{Role: "user", Content: query},
	}

	for {
		result, err := doStream(cfg.BaseURL, modelID, messages, tools.All(), cfg.Limit.Output, showThinking, contentOut)
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

			var toolResult string
			if tool, ok := tools.Get(tc.Function.Name); ok {
				res, execErr := tool.Execute(json.RawMessage(tc.Function.Arguments))
				if execErr != nil {
					toolResult = "error: " + execErr.Error()
				} else {
					toolResult = res
				}
			} else {
				toolResult = fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
			}

			if verboseTools {
				preview := toolResult
				if len(preview) > 500 {
					preview = preview[:500] + "..."
				}
				logf("%s  result: %s%s\n", colorDim, preview, colorReset)
			}

			messages = append(messages, Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			})
		}
	}
}

func runMailSummary(cfg modelConfig, modelID string, showThinking bool, contentOut io.Writer, logf func(string, ...any), prompts *Prompts, sinceHours float64) (string, error) {
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

	result, err := doStream(cfg.BaseURL, modelID, messages, nil, cfg.Limit.Output, showThinking, contentOut)
	if err != nil {
		return "", fmt.Errorf("final synthesis: %w", err)
	}
	fmt.Fprintln(contentOut)
	return result.Content, nil
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
