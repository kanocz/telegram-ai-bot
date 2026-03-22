package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

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

// --- Shared helpers for interactive modes ---

// extractHeadlines runs Phase 1 (headline extraction) for the given sources.
func extractHeadlines(cfg modelConfig, modelID string, sources []newsSource, catName string, prompts *Prompts, progress func(string)) []sourceHeadlines {
	var result []sourceHeadlines
	for i := range sources {
		s := &sources[i]
		if s.Err != nil {
			continue
		}
		if len(s.Content) < 500 {
			progress(fmt.Sprintf("  [%s] %s — пропуск (%d символов)", catName, s.Name, len(s.Content)))
			continue
		}
		progress(fmt.Sprintf("  [%s] Заголовки %s (%d символов)...", catName, s.Name, len(s.Content)))

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
			progress(fmt.Sprintf("    ошибка JSON: %v", err))
			parsed = sourceHeadlines{SourceName: s.Name, SourceURL: s.URL}
		}
		if parsed.SourceName == "" {
			parsed.SourceName = s.Name
		}
		if parsed.SourceURL == "" {
			parsed.SourceURL = s.URL
		}

		if len(parsed.Headlines) > 0 {
			result = append(result, parsed)
			progress(fmt.Sprintf("    → %d заголовков", len(parsed.Headlines)))
		}
	}
	return result
}

// clusterTopics runs Phase 2 (topic clustering) on headlines.
func clusterTopics(cfg modelConfig, modelID string, headlines []sourceHeadlines, filter string, prompts *Prompts, progress func(string)) (*topicClustering, error) {
	headlinesJSON, err := json.Marshal(headlines)
	if err != nil {
		return nil, fmt.Errorf("marshal headlines: %w", err)
	}

	maxInputChars := cfg.Limit.Context * 3 / 2
	if maxInputChars <= 0 {
		maxInputChars = 80000
	}
	clusterInput := string(headlinesJSON)
	if len(clusterInput) > maxInputChars {
		for i := range headlines {
			for j := range headlines[i].Headlines {
				d := &headlines[i].Headlines[j].Description
				if len(*d) > 60 {
					*d = (*d)[:60] + "..."
				}
			}
		}
		headlinesJSON, _ = json.Marshal(headlines)
		clusterInput = string(headlinesJSON)
		if len(clusterInput) > maxInputChars {
			clusterInput = clusterInput[:maxInputChars]
		}
	}

	systemPrompt := prompts.NewsTopicCluster
	if filter != "" {
		systemPrompt += "\n\nДОПОЛНИТЕЛЬНЫЙ ФИЛЬТР: " + filter
	}

	clusterMessages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: clusterInput},
	}

	clusterRaw, err := doChat(cfg.BaseURL, modelID, clusterMessages, cfg.Limit.Output, cfg.Limit.Context, thinkDisable)
	if err != nil {
		return nil, fmt.Errorf("clustering: %w", err)
	}

	clustering, err := extractJSON[topicClustering](clusterRaw)
	if err != nil {
		return nil, fmt.Errorf("parse clustering: %w", err)
	}
	return &clustering, nil
}

// --- Keyword-based pre-filtering for search mode ---

// searchKeywords holds the result of keyword generation for search filtering.
type searchKeywords struct {
	KeywordGroups [][]string `json:"keyword_groups"`
	Description   string     `json:"description"`
}

// generateSearchKeywords asks the LLM to produce keyword groups for pre-filtering pages.
func generateSearchKeywords(cfg modelConfig, modelID string, query string, prompts *Prompts) (*searchKeywords, error) {
	messages := []Message{
		{Role: "system", Content: prompts.NewsSearchKeywords},
		{Role: "user", Content: query},
	}

	raw, err := doChat(cfg.BaseURL, modelID, messages, cfg.Limit.Output, cfg.Limit.Context, thinkDisable)
	if err != nil {
		return nil, fmt.Errorf("generate keywords: %w", err)
	}

	kw, err := extractJSON[searchKeywords](raw)
	if err != nil {
		return nil, fmt.Errorf("parse keywords: %w", err)
	}
	// Normalize all keywords to lowercase
	for i := range kw.KeywordGroups {
		for j := range kw.KeywordGroups[i] {
			kw.KeywordGroups[i][j] = strings.ToLower(kw.KeywordGroups[i][j])
		}
	}
	return &kw, nil
}

// contentMatchesKeywords checks if the page content matches at least one keyword group.
// A group matches if ALL keywords in the group are found in the lowercased content.
func contentMatchesKeywords(content string, groups [][]string) bool {
	if len(groups) == 0 {
		return true // no filter = match everything
	}
	lower := strings.ToLower(content)
	for _, group := range groups {
		allFound := true
		for _, kw := range group {
			if !strings.Contains(lower, kw) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}

// searchTopics runs a search-specific clustering: given all headlines and a query,
// returns only topics relevant to the search query.
func searchTopics(cfg modelConfig, modelID string, headlines []sourceHeadlines, query string, prompts *Prompts, progress func(string)) (*topicClustering, error) {
	headlinesJSON, err := json.Marshal(headlines)
	if err != nil {
		return nil, fmt.Errorf("marshal headlines: %w", err)
	}

	maxInputChars := cfg.Limit.Context * 3 / 2
	if maxInputChars <= 0 {
		maxInputChars = 80000
	}
	input := string(headlinesJSON)
	if len(input) > maxInputChars {
		for i := range headlines {
			for j := range headlines[i].Headlines {
				d := &headlines[i].Headlines[j].Description
				if len(*d) > 60 {
					*d = (*d)[:60] + "..."
				}
			}
		}
		headlinesJSON, _ = json.Marshal(headlines)
		input = string(headlinesJSON)
		if len(input) > maxInputChars {
			input = input[:maxInputChars]
		}
	}

	systemPrompt := strings.ReplaceAll(prompts.NewsTopicSearch, "{search_query}", query)

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: input},
	}

	raw, err := doChat(cfg.BaseURL, modelID, messages, cfg.Limit.Output, cfg.Limit.Context, thinkDisable)
	if err != nil {
		return nil, fmt.Errorf("search clustering: %w", err)
	}

	clustering, err := extractJSON[topicClustering](raw)
	if err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return &clustering, nil
}

// deepDiveTopics runs Phase 3 (deep dive) for the given topics and returns results.
func deepDiveTopics(cfg modelConfig, modelID string, topics []topicGroup, category string, prompts *Prompts, progress func(string),
	logf func(string, ...any), mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool) []topicResult {

	// Prepare tools for deep-dive sub-agents
	wfsTool, _ := tools.Get("web_fetch_summarize")
	subAgentDefs := []tools.Definition{wfsTool.Def}
	for _, name := range []string{"memory_temp_put", "memory_temp_get"} {
		if t, ok := tools.Get(name); ok {
			subAgentDefs = append(subAgentDefs, t.Def)
		}
	}
	subAgentExec := makeToolExec(mcpMgr, mcpNames)
	if mcpMgr != nil && (len(mcpNames) > 0 || len(mcpOverrides) > 0) {
		subAgentDefs = append(subAgentDefs, mcpMgr.ActiveToolDefs(mcpNames, mcpOverrides)...)
	}

	var results []topicResult
	multiSourceCount := 0
	for _, t := range topics {
		if len(t.Articles) >= 2 {
			multiSourceCount++
		}
	}

	deepIdx := 0
	for _, t := range topics {
		if len(t.Articles) < 2 {
			brief := buildBriefFromArticles(t.Articles)
			results = append(results, topicResult{
				TopicTitle:  t.TopicTitle,
				Category:    category,
				SourceCount: len(t.Articles),
				Analysis:    brief,
			})
			continue
		}

		deepIdx++
		progress(fmt.Sprintf("  [%d/%d] %s (%d источников)...", deepIdx, multiSourceCount, t.TopicTitle, len(t.Articles)))

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

		results = append(results, topicResult{
			TopicTitle:  t.TopicTitle,
			Category:    category,
			SourceCount: len(t.Articles),
			Analysis:    analysis,
		})
	}
	return results
}

// matchCategory tries to match user input to a news category (case-insensitive, supports aliases).
func matchCategory(input string, categories []newsCategory) *newsCategory {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return nil
	}

	// Aliases: common names in Russian and English
	aliases := map[string]string{
		"czech": "czech", "чехия": "czech", "cz": "czech", "домашние": "czech", "домой": "czech",
		"europe": "europe", "европа": "europe", "eu": "europe", "ес": "europe",
		"war": "war", "война": "war", "украина": "war", "ukraine": "war", "конфликт": "war",
		"economics": "economics", "экономика": "economics", "economy": "economics", "бизнес": "economics", "финансы": "economics",
		"world": "world", "мир": "world", "свет": "world",
	}

	// Try exact alias match
	if canonical, ok := aliases[input]; ok {
		for i := range categories {
			if strings.ToLower(categories[i].Name) == canonical {
				return &categories[i]
			}
		}
	}

	// Try prefix match on category name
	for i := range categories {
		if strings.HasPrefix(strings.ToLower(categories[i].Name), input) {
			return &categories[i]
		}
	}

	// Try prefix match on header (which may contain emoji)
	for i := range categories {
		header := strings.ToLower(categories[i].Header)
		// Strip emoji/non-letter prefix
		stripped := strings.TrimLeftFunc(header, func(r rune) bool {
			return !unicode.IsLetter(r)
		})
		stripped = strings.TrimSpace(stripped)
		if stripped != "" && strings.HasPrefix(stripped, input) {
			return &categories[i]
		}
	}

	return nil
}

// runNewsBrowse handles "/news <category>" — interactive category browse mode.
// Shows clustered topics, lets the user pick one for deep dive.
func runNewsBrowse(cfg modelConfig, modelID string, cat *newsCategory, showThinking bool,
	contentOut io.Writer, logf func(string, ...any), prompts *Prompts,
	mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool,
	prompter tools.UserPrompter) (string, error) {

	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	// Phase 0: Fetch sources for this category
	progress(fmt.Sprintf("Загрузка %d источников для %s...", len(cat.URLs), cat.DisplayHeader()))
	sources := fetchAllNews(cat.URLs, progress)

	var ok int
	for _, s := range sources {
		if s.Err == nil {
			ok++
		}
	}
	if ok == 0 {
		return "", fmt.Errorf("все источники недоступны для %s", cat.Name)
	}

	// Phase 1: Extract headlines
	progress(fmt.Sprintf("Фаза 1 [%s]: Извлечение заголовков...", cat.Name))
	headlines := extractHeadlines(cfg, modelID, sources, cat.Name, prompts, progress)
	if len(headlines) == 0 {
		return "", fmt.Errorf("нет заголовков для %s", cat.Name)
	}

	// Phase 2: Cluster by topic
	progress(fmt.Sprintf("Фаза 2 [%s]: Группировка по темам...", cat.Name))
	clustering, err := clusterTopics(cfg, modelID, headlines, cat.Filter, prompts, progress)
	if err != nil {
		return "", err
	}
	if len(clustering.Topics) == 0 {
		return "", fmt.Errorf("нет тем для %s", cat.Name)
	}

	// Sort topics: multi-source first, then by article count
	sort.Slice(clustering.Topics, func(i, j int) bool {
		return len(clustering.Topics[i].Articles) > len(clustering.Topics[j].Articles)
	})

	// Build topic list for the user
	var options []tools.UserOption
	for _, t := range clustering.Topics {
		srcTag := ""
		if len(t.Articles) > 1 {
			srcTag = fmt.Sprintf(" [%d ист.]", len(t.Articles))
		}
		options = append(options, tools.UserOption{
			Label:       t.TopicTitle,
			Description: srcTag,
		})
	}

	// Add "all topics" option
	options = append(options, tools.UserOption{
		Label:       "📋 Все темы",
		Description: fmt.Sprintf("Полный анализ всех %d тем", len(clustering.Topics)),
	})

	question := fmt.Sprintf("%s — %d тем найдено. Выберите тему для анализа:", cat.DisplayHeader(), len(clustering.Topics))

	answer, err := prompter.Ask(tools.UserQuestion{
		Question: question,
		Options:  options,
	})
	if err != nil {
		return "", fmt.Errorf("ask user: %w", err)
	}

	// Determine which topic(s) to analyze
	var selectedTopics []topicGroup
	allLabel := options[len(options)-1].Label

	if answer == allLabel {
		// All topics
		selectedTopics = clustering.Topics
	} else {
		// Try to find by label match
		for _, t := range clustering.Topics {
			if t.TopicTitle == answer {
				selectedTopics = []topicGroup{t}
				break
			}
		}
		// Try numeric index
		if len(selectedTopics) == 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(answer)); err == nil && n >= 1 && n <= len(clustering.Topics) {
				selectedTopics = []topicGroup{clustering.Topics[n-1]}
			}
		}
		if len(selectedTopics) == 0 {
			// Fallback: treat as search within this category's topics
			for _, t := range clustering.Topics {
				if strings.Contains(strings.ToLower(t.TopicTitle), strings.ToLower(answer)) {
					selectedTopics = append(selectedTopics, t)
				}
			}
		}
		if len(selectedTopics) == 0 {
			return fmt.Sprintf("Тема \"%s\" не найдена среди %d тем.", answer, len(clustering.Topics)), nil
		}
	}

	// Phase 3: Deep dive
	progress(fmt.Sprintf("Фаза 3: Анализ %d тем...", len(selectedTopics)))
	results := deepDiveTopics(cfg, modelID, selectedTopics, cat.Name, prompts, progress, logf, mcpMgr, mcpNames, think, mcpOverrides)
	tools.ClearTempMemory()

	// Format output
	output := formatNewsOutput(results, []newsCategory{*cat})
	fmt.Fprint(contentOut, output)
	fmt.Fprintln(contentOut)
	return output, nil
}

// runNewsSearch handles "/news <search query>" — topic search across all sources.
// Uses keyword pre-filtering: LLM generates multilingual keywords first, then
// only pages containing matching keywords are processed with headline extraction.
func runNewsSearch(cfg modelConfig, modelID string, query string, showThinking bool,
	contentOut io.Writer, logf func(string, ...any), configPath string, prompts *Prompts,
	mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool) (string, error) {

	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	// Read config
	categories, err := readNewsConfig(configPath)
	if err != nil {
		return "", fmt.Errorf("reading news config: %w", err)
	}

	// Phase 0a: Generate search keywords (fast LLM call, runs before fetching pages)
	progress(fmt.Sprintf("Генерация ключевых слов для \"%s\"...", query))
	keywords, kwErr := generateSearchKeywords(cfg, modelID, query, prompts)
	if kwErr != nil {
		progress(fmt.Sprintf("  ⚠ Ошибка генерации ключевых слов: %v — будут обработаны все страницы", kwErr))
	} else {
		var groupStrs []string
		for _, g := range keywords.KeywordGroups {
			groupStrs = append(groupStrs, "("+strings.Join(g, " + ")+")")
		}
		progress(fmt.Sprintf("  Фильтр: %s", strings.Join(groupStrs, " | ")))
		if keywords.Description != "" {
			progress(fmt.Sprintf("  Логика: %s", keywords.Description))
		}
	}

	// Collect unique URLs across all categories (deduplicate)
	seen := map[string]bool{}
	var allURLs []string
	for _, cat := range categories {
		for _, u := range cat.URLs {
			if !seen[u] {
				seen[u] = true
				allURLs = append(allURLs, u)
			}
		}
	}

	// Phase 0b: Fetch all pages
	progress(fmt.Sprintf("Поиск: \"%s\" — загрузка %d источников...", query, len(allURLs)))
	sources := fetchAllNews(allURLs, progress)

	var fetchedOK int
	for _, s := range sources {
		if s.Err == nil {
			fetchedOK++
		}
	}
	if fetchedOK == 0 {
		return "", fmt.Errorf("все источники недоступны")
	}
	progress(fmt.Sprintf("Загружено %d/%d источников", fetchedOK, len(sources)))

	// Phase 0c: Pre-filter pages by keywords
	var relevantSources []newsSource
	var skippedCount int
	if keywords != nil && len(keywords.KeywordGroups) > 0 {
		for _, s := range sources {
			if s.Err != nil {
				continue
			}
			if contentMatchesKeywords(s.Content, keywords.KeywordGroups) {
				relevantSources = append(relevantSources, s)
			} else {
				skippedCount++
			}
		}
		progress(fmt.Sprintf("Фильтрация: %d релевантных, %d пропущено по ключевым словам", len(relevantSources), skippedCount))
	} else {
		// No keywords — process everything
		for _, s := range sources {
			if s.Err == nil {
				relevantSources = append(relevantSources, s)
			}
		}
	}

	if len(relevantSources) == 0 {
		return fmt.Sprintf("По запросу \"%s\" не найдено релевантных источников (проверено %d страниц).", query, fetchedOK), nil
	}

	// Phase 1: Extract headlines only from relevant sources
	progress(fmt.Sprintf("Фаза 1: Извлечение заголовков из %d релевантных источников...", len(relevantSources)))
	headlines := extractHeadlines(cfg, modelID, relevantSources, "search", prompts, progress)
	if len(headlines) == 0 {
		return fmt.Sprintf("По запросу \"%s\" не удалось извлечь заголовки.", query), nil
	}

	// Phase 2: Search-specific clustering
	progress(fmt.Sprintf("Фаза 2: Поиск тем по запросу \"%s\"...", query))
	clustering, err := searchTopics(cfg, modelID, headlines, query, prompts, progress)
	if err != nil {
		return "", err
	}
	if len(clustering.Topics) == 0 {
		return fmt.Sprintf("По запросу \"%s\" ничего не найдено среди текущих новостей.", query), nil
	}

	progress(fmt.Sprintf("Найдено %d тем по запросу \"%s\"", len(clustering.Topics), query))

	// Phase 3: Deep dive on all found topics
	progress("Фаза 3: Анализ найденных тем...")
	results := deepDiveTopics(cfg, modelID, clustering.Topics, "Поиск: "+query, prompts, progress, logf, mcpMgr, mcpNames, think, mcpOverrides)
	tools.ClearTempMemory()

	if len(results) == 0 {
		return fmt.Sprintf("По запросу \"%s\" ничего не найдено.", query), nil
	}

	// Format output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 🔎 Поиск: %s\n\n", query))
	for _, r := range results {
		sourceTag := ""
		if r.SourceCount > 1 {
			sourceTag = fmt.Sprintf(" [%d источников]", r.SourceCount)
		}
		sb.WriteString(fmt.Sprintf("**%s**%s\n", r.TopicTitle, sourceTag))
		sb.WriteString(r.Analysis)
		sb.WriteString("\n\n")
	}

	output := sb.String()
	fmt.Fprint(contentOut, output)
	fmt.Fprintln(contentOut)
	return output, nil
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
