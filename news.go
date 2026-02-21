package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"

	"ai-webfetch/tools"
)

type newsSource struct {
	URL     string
	Name    string
	Content string
	Err     error
}

// readNewsURLs reads URLs from a file, skipping comments (#) and blank lines.
func readNewsURLs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var urls []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no URLs found in %s", path)
	}
	return urls, nil
}

// fetchAllNews fetches all URLs concurrently using tools.FetchURL.
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
			// Truncate to 30k chars — leaves room for drilled articles in 32k context
			if len(content) > 30000 {
				content = content[:30000] + "\n[...truncated]"
			}
			sources[idx].Content = content
		}(i, u)
	}

	wg.Wait()
	return sources
}

// sourceName extracts a readable name from a URL (e.g. "novinky.cz", "chinadaily.com.cn/world/europe").
func sourceName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	// Strip www. prefix
	host = strings.TrimPrefix(host, "www.")

	path := strings.Trim(u.Path, "/")
	if path != "" {
		return host + "/" + path
	}
	return host
}

func runNewsSummary(cfg modelConfig, modelID string, showThinking bool, contentOut io.Writer, logf func(string, ...any), urlsPath string, prompts *Prompts) (string, error) {
	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	// Read URLs
	urls, err := readNewsURLs(urlsPath)
	if err != nil {
		return "", fmt.Errorf("reading news URLs: %w", err)
	}
	progress(fmt.Sprintf("Загрузка %d новостных источников...", len(urls)))

	// Concurrent fetch
	sources := fetchAllNews(urls, progress)

	// Count successful fetches
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

	// Give sub-agents web_fetch_summarize (context-efficient) instead of raw web_fetch
	wfsTool, _ := tools.Get("web_fetch_summarize")
	webFetchDefs := []tools.Definition{wfsTool.Def}

	// Per-source sub-agent analysis (sequential — single GPU)
	progress(fmt.Sprintf("Анализ %d источников через суб-агентов...", ok))
	for i := range sources {
		s := &sources[i]
		if s.Err != nil {
			continue
		}
		progress(fmt.Sprintf("  [%d/%d] Анализ %s...", i+1, len(sources), s.Name))

		messages := []Message{
			{Role: "system", Content: prompts.NewsSourceSubAgent},
			{Role: "user", Content: fmt.Sprintf("Источник: %s\nURL: %s\n\nСодержимое страницы:\n%s", s.Name, s.URL, s.Content)},
		}

		digest, err := doSubAgentWithTools(cfg.BaseURL, modelID, messages, webFetchDefs, cfg.Limit.Output, cfg.Limit.Context, 5, 15000, logf)
		if err != nil {
			progress(fmt.Sprintf("    ошибка: %v", err))
			s.Content = fmt.Sprintf("(ошибка анализа: %v)", err)
			continue
		}
		s.Content = digest
	}

	// Build final synthesis input
	var sb strings.Builder
	for i, s := range sources {
		if s.Err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("=== Источник %d: %s (%s) ===\n", i+1, s.Name, s.URL))
		sb.WriteString(s.Content)
		sb.WriteString("\n\n")
	}

	finalInput := sb.String()
	if len(finalInput) > 60000 {
		finalInput = finalInput[:60000] + "\n[...truncated]"
	}

	progress("Финальный кросс-анализ...")

	messages := []Message{
		{Role: "system", Content: prompts.NewsFinalSynthesis},
		{Role: "user", Content: finalInput},
	}

	result, err := doStream(cfg.BaseURL, modelID, messages, nil, cfg.Limit.Output, showThinking, contentOut)
	if err != nil {
		return "", fmt.Errorf("final synthesis: %w", err)
	}
	fmt.Fprintln(contentOut)
	return result.Content, nil
}
