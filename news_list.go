package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-webfetch/tools"
)

// List mode: phases 1-2 only, top topics per category with a button each.
// The deep dive runs when a button is pressed. Lists are saved as files so the
// bot daemon can serve buttons from a digest that a cron CLI run sent.

const (
	newsDivePrefix = "nd:" // callback_data: nd:<listID>:<topic number>
	newsListTopics = 10
	newsListTTL    = 48 * time.Hour
)

// newsListDir is set by main to <config-dir>/news-cache.
var newsListDir = "news-cache"

type newsList struct {
	ID     string       `json:"id"`
	Header string       `json:"header"`
	Topics []topicGroup `json:"topics"`
}

// buildNewsLists runs headline extraction and clustering for the given
// categories and saves the top topics of each one.
func buildNewsLists(cfg modelConfig, modelID string, categories []newsCategory, prompts *Prompts, logf func(string, ...any)) ([]newsList, error) {
	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}

	// The same page often feeds several categories: fetch and extract it once.
	seen := map[string]bool{}
	var urls []string
	for _, cat := range categories {
		for _, u := range cat.URLs {
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	progress(fmt.Sprintf("Загрузка %d источников...", len(urls)))
	sources := fetchAllNews(urls, progress)

	progress("Фаза 1: Извлечение заголовков...")
	headlines := map[string][]sourceHeadlines{}
	for i := range sources {
		headlines[sources[i].URL] = extractHeadlines(cfg, modelID, sources[i:i+1], "news", prompts, progress)
	}

	var lists []newsList
	for ci, cat := range categories {
		var hl []sourceHeadlines
		for _, u := range cat.URLs {
			hl = append(hl, headlines[u]...)
		}
		if len(hl) == 0 {
			progress(fmt.Sprintf("Категория %q: нет заголовков, пропуск", cat.Name))
			continue
		}
		progress(fmt.Sprintf("Фаза 2 [%s]: Группировка по темам...", cat.Name))
		cl, err := clusterTopics(cfg, modelID, hl, cat.Filter, prompts, progress)
		if err != nil {
			progress(fmt.Sprintf("  ошибка: %v", err))
			continue
		}
		topics := cl.Topics
		sort.SliceStable(topics, func(i, j int) bool { return len(topics[i].Articles) > len(topics[j].Articles) })
		if len(topics) > newsListTopics {
			topics = topics[:newsListTopics]
		}
		if len(topics) == 0 {
			continue
		}
		l := newsList{
			ID:     strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.Itoa(ci),
			Header: cat.DisplayHeader(),
			Topics: topics,
		}
		if err := saveNewsList(l); err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("no topics extracted from any category")
	}
	return lists, nil
}

func saveNewsList(l newsList) error {
	if err := os.MkdirAll(newsListDir, 0o755); err != nil {
		return fmt.Errorf("news list dir: %w", err)
	}
	// Drop lists past their TTL so the directory does not grow forever.
	if entries, err := os.ReadDir(newsListDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > newsListTTL {
				_ = os.Remove(filepath.Join(newsListDir, e.Name()))
			}
		}
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(newsListDir, l.ID+".json"), data, 0o644)
}

// formatNewsList renders a list as plain text (sent without parse mode).
func formatNewsList(l newsList) string {
	var sb strings.Builder
	sb.WriteString(l.Header + "\n")
	for i, t := range l.Topics {
		sb.WriteString(fmt.Sprintf("\n%d. %s", i+1, t.TopicTitle))
		if len(t.Articles) > 1 {
			sb.WriteString(fmt.Sprintf(" [%d ист.]", len(t.Articles)))
		}
	}
	return sb.String()
}

// newsListKeyboard has one numbered button per topic, five per row.
func newsListKeyboard(l newsList) TGInlineKeyboardMarkup {
	var rows [][]TGInlineKeyboardButton
	for i := range l.Topics {
		if i%5 == 0 {
			rows = append(rows, nil)
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], TGInlineKeyboardButton{
			Text:         strconv.Itoa(i + 1),
			CallbackData: fmt.Sprintf("%s%s:%d", newsDivePrefix, l.ID, i+1),
		})
	}
	return TGInlineKeyboardMarkup{InlineKeyboard: rows}
}

func sendNewsLists(token string, chatID int64, lists []newsList) error {
	for _, l := range lists {
		if _, err := sendMessageWithKeyboard(token, chatID, formatNewsList(l), newsListKeyboard(l)); err != nil {
			return err
		}
	}
	return nil
}

// runNewsDive deep-dives one topic from a saved list. arg is the callback data
// "nd:<listID>:<n>"; it can also arrive as typed text, so the ID is validated
// before it becomes a file name.
func runNewsDive(cfg modelConfig, modelID, arg string, prompts *Prompts, logf func(string, ...any),
	mcpMgr *MCPManager, mcpNames []string, think thinkMode, mcpOverrides map[string]bool) (string, error) {

	id, num, _ := strings.Cut(strings.TrimPrefix(arg, newsDivePrefix), ":")
	n, err := strconv.Atoi(num)
	if id == "" || strings.Trim(id, "0123456789abcdefghijklmnopqrstuvwxyz") != "" || err != nil {
		return "", fmt.Errorf("bad news topic reference %q", arg)
	}
	data, err := os.ReadFile(filepath.Join(newsListDir, id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "Список тем устарел (хранится 2 дня). Запросите /news заново.", nil
	}
	if err != nil {
		return "", err
	}
	var l newsList
	if err := json.Unmarshal(data, &l); err != nil {
		return "", fmt.Errorf("news list %s: %w", id, err)
	}
	if n < 1 || n > len(l.Topics) {
		return "", fmt.Errorf("topic %d not in list (1-%d)", n, len(l.Topics))
	}

	progress := func(msg string) {
		logf("%s%s%s\n", colorDim, msg, colorReset)
	}
	t := l.Topics[n-1]
	// minSources 1: the user asked for this topic, dive even with one source.
	results := deepDiveTopics(cfg, modelID, []topicGroup{t}, l.Header, 1, prompts, progress, logf, mcpMgr, mcpNames, think, mcpOverrides)
	tools.ClearTempMemory()

	r := results[0]
	return fmt.Sprintf("**%s**\n%s\n\n%s", r.TopicTitle, l.Header, r.Analysis), nil
}
