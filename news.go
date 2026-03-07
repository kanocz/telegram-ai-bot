package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"ai-webfetch/tools"
)

// --- Data structures ---

// newsCategory represents one category from news.json.
type newsCategory struct {
	Name   string   `json:"name"`
	Header string   `json:"header"`
	Filter string   `json:"filter"`
	URLs   []string `json:"urls"`
}

func (c newsCategory) DisplayHeader() string {
	if c.Header != "" {
		return c.Header
	}
	return c.Name
}

// Phase 1: headline extraction output
type newsHeadline struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

type sourceHeadlines struct {
	SourceName string         `json:"source_name"`
	SourceURL  string         `json:"source_url"`
	Headlines  []newsHeadline `json:"headlines"`
}

// Phase 2: topic clustering output
type topicArticle struct {
	SourceName string `json:"source_name"`
	Title      string `json:"title"`
	ArticleURL string `json:"article_url"`
	Brief      string `json:"brief"`
}

type topicGroup struct {
	TopicTitle string         `json:"topic_title"`
	Articles   []topicArticle `json:"articles"`
}

type topicClustering struct {
	Topics []topicGroup `json:"topics"`
}

// Phase 3+4: internal result
type topicResult struct {
	TopicTitle  string
	Category    string
	SourceCount int
	Analysis    string
}

// --- Config reader ---

func readNewsConfig(path string) ([]newsCategory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cats []newsCategory
	if err := json.Unmarshal(data, &cats); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cats) == 0 {
		return nil, fmt.Errorf("no categories found in %s", path)
	}
	for _, c := range cats {
		if len(c.URLs) == 0 {
			return nil, fmt.Errorf("category %q has no URLs", c.Name)
		}
	}
	return cats, nil
}

// --- Existing helpers ---

type newsSource struct {
	URL     string
	Name    string
	Content string
	Err     error
}

func fetchAllNews(urls []string, progress func(string)) []newsSource {
	sources := make([]newsSource, len(urls))
	var wg sync.WaitGroup

	for i, u := range urls {
		sources[i].URL = u
		sources[i].Name = sourceName(u)
		wg.Add(1)
		go func(idx int, rawURL string) {
			defer wg.Done()
			progress(fmt.Sprintf("  [%d/%d] %s...", idx+1, len(urls), sourceName(rawURL)))
			content, err := tools.FetchURL(rawURL)
			if err != nil {
				sources[idx].Err = err
				return
			}
			if len(content) > 30000 {
				content = content[:30000] + "\n[...truncated]"
			}
			sources[idx].Content = content
		}(i, u)
	}

	wg.Wait()
	return sources
}

func sourceName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	host = strings.TrimPrefix(host, "www.")

	path := strings.Trim(u.Path, "/")
	if path != "" {
		return host + "/" + path
	}
	return host
}

// --- JSON extraction ---

// extractJSON tries to parse a JSON value of type T from raw LLM output.
// It handles: direct JSON, ```json fenced blocks, and bare {/[ searching.
func extractJSON[T any](raw string) (T, error) {
	var zero T
	raw = strings.TrimSpace(raw)

	// 1. Direct unmarshal
	var result T
	if err := json.Unmarshal([]byte(raw), &result); err == nil {
		return result, nil
	}

	// 2. Extract from ```json ... ``` block
	if idx := strings.Index(raw, "```json"); idx >= 0 {
		start := idx + len("```json")
		end := strings.Index(raw[start:], "```")
		if end >= 0 {
			block := strings.TrimSpace(raw[start : start+end])
			if err := json.Unmarshal([]byte(block), &result); err == nil {
				return result, nil
			}
		}
	}
	// Also try ``` without json suffix
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + len("```")
		// Skip language tag on same line
		if nl := strings.IndexByte(raw[start:], '\n'); nl >= 0 {
			start += nl + 1
		}
		end := strings.Index(raw[start:], "```")
		if end >= 0 {
			block := strings.TrimSpace(raw[start : start+end])
			if err := json.Unmarshal([]byte(block), &result); err == nil {
				return result, nil
			}
		}
	}

	// 3. Find first { or [ and balance brackets
	opener := -1
	var open, close byte
	for i, c := range raw {
		if c == '{' {
			opener = i
			open, close = '{', '}'
			break
		}
		if c == '[' {
			opener = i
			open, close = '[', ']'
			break
		}
	}
	if opener >= 0 {
		depth := 0
		inStr := false
		escape := false
		for i := opener; i < len(raw); i++ {
			c := raw[i]
			if escape {
				escape = false
				continue
			}
			if c == '\\' && inStr {
				escape = true
				continue
			}
			if c == '"' {
				inStr = !inStr
				continue
			}
			if inStr {
				continue
			}
			if c == open {
				depth++
			} else if c == close {
				depth--
				if depth == 0 {
					block := raw[opener : i+1]
					if err := json.Unmarshal([]byte(block), &result); err == nil {
						return result, nil
					}
					break
				}
			}
		}
	}

	return zero, fmt.Errorf("no valid JSON found in response (len=%d)", len(raw))
}

// --- Main pipeline ---

func runNewsSummary(cfg modelConfig, modelID string, showThinking bool, contentOut io.Writer, logf func(string, ...any), configPath string, prompts *Prompts, mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool) (string, error) {
	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	// --- Phase 0: Read config, fetch all source pages ---
	categories, err := readNewsConfig(configPath)
	if err != nil {
		return "", fmt.Errorf("reading news config: %w", err)
	}

	// Collect all URLs across categories, tracking which belong where
	var allURLs []string
	type catRange struct {
		start, end int
	}
	catRanges := make([]catRange, len(categories))
	for i, cat := range categories {
		catRanges[i].start = len(allURLs)
		allURLs = append(allURLs, cat.URLs...)
		catRanges[i].end = len(allURLs)
	}

	progress(fmt.Sprintf("Загрузка %d новостных источников (%d категорий)...", len(allURLs), len(categories)))

	sources := fetchAllNews(allURLs, progress)

	var ok int
	for _, s := range sources {
		if s.Err == nil {
			ok++
		} else {
			progress(fmt.Sprintf("  ✗ %s: %v", s.Name, s.Err))
		}
	}
	if ok == 0 {
		return "", fmt.Errorf("no news sources fetched successfully")
	}
	progress(fmt.Sprintf("Загружено %d/%d источников", ok, len(sources)))

	// Split sources back into per-category slices
	catSources := make([][]newsSource, len(categories))
	for i, cr := range catRanges {
		catSources[i] = sources[cr.start:cr.end]
	}

	// Prepare tools for deep-dive sub-agents (shared across categories)
	wfsTool, _ := tools.Get("web_fetch_summarize")
	subAgentDefs := []tools.Definition{wfsTool.Def}
	// Add memory_temp tools so deep-dive sub-agents can cross-reference findings
	for _, name := range []string{"memory_temp_put", "memory_temp_get"} {
		if t, ok := tools.Get(name); ok {
			subAgentDefs = append(subAgentDefs, t.Def)
		}
	}
	subAgentExec := makeToolExec(mcpMgr, mcpNames)
	if mcpMgr != nil && (len(mcpNames) > 0 || len(mcpOverrides) > 0) {
		subAgentDefs = append(subAgentDefs, mcpMgr.ActiveToolDefs(mcpNames, mcpOverrides)...)
	}

	var allResults []topicResult

	// --- Process each category independently (Phases 1-3) ---
	for ci, cat := range categories {
		cSources := catSources[ci]

		var catOK int
		for _, s := range cSources {
			if s.Err == nil {
				catOK++
			}
		}
		if catOK == 0 {
			progress(fmt.Sprintf("Категория %q: все источники недоступны, пропуск", cat.Name))
			continue
		}

		// --- Phase 1: Extract headlines for this category ---
		progress(fmt.Sprintf("Фаза 1 [%s]: Извлечение заголовков из %d источников...", cat.Name, catOK))
		var catHeadlines []sourceHeadlines

		for i := range cSources {
			s := &cSources[i]
			if s.Err != nil {
				continue
			}
			if len(s.Content) < 500 {
				progress(fmt.Sprintf("  [%s] %s — пропуск (%d символов, слишком мало контента)", cat.Name, s.Name, len(s.Content)))
				continue
			}
			progress(fmt.Sprintf("  [%s] Заголовки %s (%d символов)...", cat.Name, s.Name, len(s.Content)))

			messages := []Message{
				{Role: "system", Content: prompts.NewsHeadlineExtract},
				{Role: "user", Content: fmt.Sprintf("Источник: %s\nURL: %s\n\nСодержимое страницы:\n%s", s.Name, s.URL, s.Content)},
			}

			raw, err := doChat(cfg.BaseURL, modelID, messages, cfg.Limit.Output, cfg.Limit.Context, thinkDisable)
			if err != nil {
				progress(fmt.Sprintf("    ошибка: %v", err))
				continue
			}

			parsed, err := extractJSON[sourceHeadlines](raw)
			if err != nil {
				preview := raw
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				progress(fmt.Sprintf("    ошибка JSON: %v", err))
				progress(fmt.Sprintf("    ответ LLM: %s", preview))
				parsed = sourceHeadlines{SourceName: s.Name, SourceURL: s.URL}
			}
			if parsed.SourceName == "" {
				parsed.SourceName = s.Name
			}
			if parsed.SourceURL == "" {
				parsed.SourceURL = s.URL
			}

			if len(parsed.Headlines) > 0 {
				catHeadlines = append(catHeadlines, parsed)
				progress(fmt.Sprintf("    → %d заголовков", len(parsed.Headlines)))
			} else {
				preview := raw
				if len(preview) > 300 {
					preview = preview[:300] + "..."
				}
				progress(fmt.Sprintf("    ⚠ 0 заголовков, ответ LLM: %s", preview))
			}
		}

		if len(catHeadlines) == 0 {
			progress(fmt.Sprintf("Категория %q: нет заголовков, пропуск", cat.Name))
			continue
		}

		// --- Phase 2: Cluster by topic within this category ---
		progress(fmt.Sprintf("Фаза 2 [%s]: Группировка по темам...", cat.Name))

		headlinesJSON, err := json.Marshal(catHeadlines)
		if err != nil {
			progress(fmt.Sprintf("  ошибка marshal: %v", err))
			continue
		}

		maxInputChars := cfg.Limit.Context * 3 / 2
		if maxInputChars <= 0 {
			maxInputChars = 80000
		}
		clusterInput := string(headlinesJSON)
		if len(clusterInput) > maxInputChars {
			for i := range catHeadlines {
				for j := range catHeadlines[i].Headlines {
					d := &catHeadlines[i].Headlines[j].Description
					if len(*d) > 60 {
						*d = (*d)[:60] + "..."
					}
				}
			}
			headlinesJSON, _ = json.Marshal(catHeadlines)
			clusterInput = string(headlinesJSON)
			if len(clusterInput) > maxInputChars {
				clusterInput = clusterInput[:maxInputChars]
			}
		}

		systemPrompt := prompts.NewsTopicCluster
		if cat.Filter != "" {
			systemPrompt += "\n\nДОПОЛНИТЕЛЬНЫЙ ФИЛЬТР: " + cat.Filter
		}

		clusterMessages := []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: clusterInput},
		}

		clusterRaw, err := doChat(cfg.BaseURL, modelID, clusterMessages, cfg.Limit.Output, cfg.Limit.Context, thinkDisable)
		if err != nil {
			progress(fmt.Sprintf("  ошибка кластеризации: %v", err))
			continue
		}

		clustering, err := extractJSON[topicClustering](clusterRaw)
		if err != nil {
			progress(fmt.Sprintf("  ошибка JSON: %v", err))
			continue
		}

		progress(fmt.Sprintf("  → %d тем", len(clustering.Topics)))

		// --- Phase 3: Deep dive per topic within this category ---
		multiSourceCount := 0
		for _, t := range clustering.Topics {
			if len(t.Articles) >= 2 {
				multiSourceCount++
			}
		}

		if multiSourceCount > 0 {
			progress(fmt.Sprintf("Фаза 3 [%s]: Deep dive для %d тем с несколькими источниками...", cat.Name, multiSourceCount))
		}

		deepIdx := 0
		for _, t := range clustering.Topics {
			if len(t.Articles) < 2 {
				brief := buildBriefFromArticles(t.Articles)
				allResults = append(allResults, topicResult{
					TopicTitle:  t.TopicTitle,
					Category:    cat.Name,
					SourceCount: len(t.Articles),
					Analysis:    brief,
				})
				continue
			}

			deepIdx++
			progress(fmt.Sprintf("  [%s %d/%d] %s (%d источников)...", cat.Name, deepIdx, multiSourceCount, t.TopicTitle, len(t.Articles)))

			sourceList := buildSourceList(t.Articles)

			prompt := prompts.NewsTopicDeepDive
			prompt = strings.ReplaceAll(prompt, "{topic_title}", t.TopicTitle)
			prompt = strings.ReplaceAll(prompt, "{source_list}", sourceList)

			messages := []Message{
				{Role: "system", Content: prompt},
				{Role: "user", Content: fmt.Sprintf("Проанализируй тему \"%s\" используя указанные источники.", t.TopicTitle)},
			}

			analysis, err := doSubAgentWithTools(cfg.BaseURL, modelID, messages, subAgentDefs, cfg.Limit.Output, cfg.Limit.Context, 5, 15000, logf, subAgentExec, think)
			if err != nil {
				progress(fmt.Sprintf("    ошибка: %v, используем briefs", err))
				analysis = buildBriefFromArticles(t.Articles)
			}

			allResults = append(allResults, topicResult{
				TopicTitle:  t.TopicTitle,
				Category:    cat.Name,
				SourceCount: len(t.Articles),
				Analysis:    analysis,
			})
		}
	}

	tools.ClearTempMemory()

	if len(allResults) == 0 {
		return "", fmt.Errorf("no topics extracted from any category")
	}

	// --- Phase 4: Format output ---
	progress("Фаза 4: Форматирование...")

	output := formatNewsOutput(allResults, categories)
	fmt.Fprint(contentOut, output)
	fmt.Fprintln(contentOut)

	return output, nil
}

// buildSourceList creates a numbered list of articles for the deep dive prompt.
func buildSourceList(articles []topicArticle) string {
	var sb strings.Builder
	for i, a := range articles {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n   URL: %s\n   Brief: %s\n", i+1, a.SourceName, a.Title, a.ArticleURL, a.Brief))
	}
	return sb.String()
}

// buildBriefFromArticles creates a short summary from article briefs (for single-source topics).
func buildBriefFromArticles(articles []topicArticle) string {
	if len(articles) == 0 {
		return ""
	}
	if len(articles) == 1 {
		a := articles[0]
		if a.Brief != "" {
			return fmt.Sprintf("%s (%s)", a.Brief, a.SourceName)
		}
		return fmt.Sprintf("%s (%s)", a.Title, a.SourceName)
	}
	var parts []string
	for _, a := range articles {
		if a.Brief != "" {
			parts = append(parts, fmt.Sprintf("- %s (%s)", a.Brief, a.SourceName))
		} else {
			parts = append(parts, fmt.Sprintf("- %s (%s)", a.Title, a.SourceName))
		}
	}
	return strings.Join(parts, "\n")
}

func formatNewsOutput(results []topicResult, categories []newsCategory) string {
	// Group by category
	grouped := map[string][]topicResult{}
	for _, r := range results {
		grouped[r.Category] = append(grouped[r.Category], r)
	}

	// Sort within each category: multi-source first, then by title
	for cat := range grouped {
		sort.Slice(grouped[cat], func(i, j int) bool {
			if grouped[cat][i].SourceCount != grouped[cat][j].SourceCount {
				return grouped[cat][i].SourceCount > grouped[cat][j].SourceCount
			}
			return grouped[cat][i].TopicTitle < grouped[cat][j].TopicTitle
		})
	}

	var sb strings.Builder

	for _, cat := range categories {
		items, ok := grouped[cat.Name]
		if !ok || len(items) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("## %s\n\n", cat.DisplayHeader()))

		for _, r := range items {
			sourceTag := ""
			if r.SourceCount > 1 {
				sourceTag = fmt.Sprintf(" [%d источников]", r.SourceCount)
			}
			sb.WriteString(fmt.Sprintf("**%s**%s\n", r.TopicTitle, sourceTag))
			sb.WriteString(r.Analysis)
			sb.WriteString("\n\n")
		}
	}

	// Cross-analysis stats block
	sb.WriteString("## 🔍 Кросс-анализ\n\n")

	totalTopics := len(results)
	multiSource := 0
	maxSources := 0
	for _, r := range results {
		if r.SourceCount >= 2 {
			multiSource++
		}
		if r.SourceCount > maxSources {
			maxSources = r.SourceCount
		}
	}

	sb.WriteString(fmt.Sprintf("- Всего тем: %d\n", totalTopics))
	sb.WriteString(fmt.Sprintf("- Темы с несколькими источниками: %d\n", multiSource))
	if maxSources > 1 {
		var topTopics []string
		for _, r := range results {
			if r.SourceCount == maxSources {
				topTopics = append(topTopics, r.TopicTitle)
			}
		}
		sb.WriteString(fmt.Sprintf("- Максимальное покрытие (%d источников): %s\n", maxSources, strings.Join(topTopics, ", ")))
	}

	return sb.String()
}
