package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
)

type webFetchArgs struct {
	URL string `json:"url"`
}

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "web_fetch",
				Description: "Fetch content from a URL and return it as Markdown. HTML pages are converted to Markdown preserving structure (headings, links, lists). Non-HTML content is returned as-is.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"url": {
							Type:        "string",
							Description: "The URL to fetch content from",
						},
					},
					Required: []string{"url"},
				},
			},
		},
		Execute: executeWebFetch,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "web_fetch_summarize",
				Description: "Fetch a URL and summarize its content via a sub-agent. Returns only the concise summary, NOT the full page text. Much more context-efficient than web_fetch — use this when you need key information from an article.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"url": {
							Type:        "string",
							Description: "The URL to fetch and summarize",
						},
						"prompt": {
							Type:        "string",
							Description: "What to extract or how to summarize, e.g. 'Extract key facts, quotes, and numbers from this news article'",
						},
					},
					Required: []string{"url"},
				},
			},
		},
		Execute: executeWebFetchSummarize,
	})
}

// FetchURL fetches the given URL and returns its content as markdown.
// HTML pages are converted to markdown; other content is returned as-is.
func FetchURL(rawURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("fetch error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return "", fmt.Errorf("read error: %w", err)
	}

	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		text = htmlToMarkdown(text, rawURL)
	}

	return fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, text), nil
}

func executeWebFetch(rawArgs json.RawMessage) (string, error) {
	var args webFetchArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	return FetchURL(args.URL)
}

func executeWebFetchSummarize(rawArgs json.RawMessage) (string, error) {
	var args struct {
		URL    string `json:"url"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	if SubAgentFn == nil {
		return "", fmt.Errorf("sub-agent not available")
	}

	content, err := FetchURL(args.URL)
	if err != nil {
		return "", err
	}

	if len(content) > 60000 {
		content = content[:60000] + "\n[...truncated]"
	}

	prompt := args.Prompt
	if prompt == "" {
		prompt = "Summarize the key information from this web page concisely."
	}

	summary, err := SubAgentFn(prompt, content)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}

	return summary, nil
}

func htmlToMarkdown(html string, sourceURL string) string {
	var opts []converter.ConvertOptionFunc

	if u, err := url.Parse(sourceURL); err == nil && u.Scheme != "" {
		domain := u.Scheme + "://" + u.Host
		opts = append(opts, converter.WithDomain(domain))
	}

	md, err := htmltomarkdown.ConvertString(html, opts...)
	if err != nil {
		return html
	}
	return strings.TrimSpace(md)
}
