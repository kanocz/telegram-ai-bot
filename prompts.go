package main

import (
	"ai-webfetch/tools"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prompts holds all configurable prompt texts.
type Prompts struct {
	SystemPrompt       string
	MailDigestSubAgent string
	MailDigestFinal    string
	NewsHeadlineExtract string
	NewsTopicCluster    string
	NewsTopicDeepDive   string
	ImapSummarize      string
	ImapDigest         string
}

type promptMeta struct {
	FileName string
	Field    func(p *Prompts) *string
}

var promptFields = []promptMeta{
	{"system-prompt.txt", func(p *Prompts) *string { return &p.SystemPrompt }},
	{"mail-digest-subagent.txt", func(p *Prompts) *string { return &p.MailDigestSubAgent }},
	{"mail-digest-final.txt", func(p *Prompts) *string { return &p.MailDigestFinal }},
	{"news-headline-extract.txt", func(p *Prompts) *string { return &p.NewsHeadlineExtract }},
	{"news-topic-cluster.txt", func(p *Prompts) *string { return &p.NewsTopicCluster }},
	{"news-topic-deepdive.txt", func(p *Prompts) *string { return &p.NewsTopicDeepDive }},
	{"imap-summarize.txt", func(p *Prompts) *string { return &p.ImapSummarize }},
	{"imap-digest.txt", func(p *Prompts) *string { return &p.ImapDigest }},
}

func defaultPrompts() Prompts {
	return Prompts{
		SystemPrompt:        defaultSystemPrompt,
		MailDigestSubAgent:  defaultMailDigestSubAgent,
		MailDigestFinal:     defaultMailDigestFinal,
		NewsHeadlineExtract: defaultNewsHeadlineExtract,
		NewsTopicCluster:    defaultNewsTopicCluster,
		NewsTopicDeepDive:   defaultNewsTopicDeepDive,
		ImapSummarize:       defaultImapSummarize,
		ImapDigest:          defaultImapDigest,
	}
}

func exportPrompts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := defaultPrompts()
	for _, m := range promptFields {
		path := filepath.Join(dir, m.FileName)
		if err := os.WriteFile(path, []byte(*m.Field(&p)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func loadPrompts(dir string) (Prompts, error) {
	p := defaultPrompts()
	for _, m := range promptFields {
		path := filepath.Join(dir, m.FileName)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return p, fmt.Errorf("read %s: %w", path, err)
		}
		*m.Field(&p) = string(data)
	}
	return p, nil
}

func applyLanguage(p *Prompts, language string) {
	for _, m := range promptFields {
		field := m.Field(p)
		*field = strings.ReplaceAll(*field, "{language}", language)
	}
}

func installToolPrompts(p *Prompts) {
	tools.ImapSummarizePrompt = p.ImapSummarize
	tools.ImapDigestPrompt = p.ImapDigest
}

const defaultSystemPrompt = `You are a helpful assistant. You have access to tools for fetching web content, reading email, and controlling smart home devices via Home Assistant.
Respond in the same language the user writes in. Default language (for automated tasks like mail/news digest): {language}.

Rules:
- When summarizing multiple emails, prefer imap_summarize_message (processes each email in a separate context) over imap_read_message to avoid exceeding the context window.
- NEVER make assumptions about data you haven't retrieved. If the user asks about correspondence history, message counts, or any email data — you MUST call the appropriate tool to get the actual data. Do not guess or assume "no messages found" without making the tool call.
- When asked to check correspondence with a sender, use imap_list_messages with the "participant" filter and appropriate "since_hours" to search both INBOX and Sent. You must do this for EACH sender the user asks about.
- Execute ALL steps the user requested, even if there are many tool calls needed. Do not skip steps to save time.
- For smart home requests: always start with ha_list(target="areas") to discover available areas, then ha_list(target="<area_id>") to find entities before controlling them. Never guess entity IDs.
- For calendar requests: use cal_list to discover calendars, then cal_events to query events by date range. Subscription calendars (iCal URLs) are read-only. Use cal_create_event/cal_update_event/cal_delete_event only for CalDAV calendars.
- For contact lookups: use contacts_search with the person's name, email, or phone. Do not guess contact details without searching first.`

const defaultMailDigestSubAgent = `Ты анализируешь группу писем от одного отправителя и историю переписки с ним.
Язык ответа: {language}.

Дай краткий дайджест:
1. Кто отправитель (имя, компания/контекст если понятно)
2. Общая суть всех писем от этого отправителя: если несколько писем образуют один диалог или связаны по теме — опиши суть диалога/ситуации целиком в 2-3 предложениях, НЕ перечисляя каждое письмо отдельно. Если письма на разные темы — кратко по каждой теме.
3. Контекст переписки: если есть история, кратко опиши о чём шла речь ранее
4. Отметь, если в письмах есть: фактура/счёт/invoice (в теле или во вложении), запрос на отзыв (от zbozi.cz, heureka.cz, google, overeno zakazniky и т.п.)

Будь лаконичен. Не повторяй заголовки дословно.`

const defaultMailDigestFinal = `Ты получил дайджесты непрочитанных писем, сгруппированные по отправителям.

Распредели ВСЕ письма по категориям и выведи структурированную сводку.

ВАЖНЫЕ ПРАВИЛА:
- Если от одного отправителя несколько писем на одну тему (диалог) — объединяй в ОДНУ строку с общей сутью, не перечисляй каждое отдельно. Укажи количество писем если > 1.
- Если письмо содержит фактуру/счёт/invoice (в теле или вложении) — оно ВСЕГДА идёт в "Счета / Бухгалтерия", даже если это также благодарность за покупку.
- Запросы на отзыв (от zbozi.cz, heureka.cz, google reviews, overeno zakazniky) — это НЕ "требующие ответа". Собери их в конце отдельной строкой: "Запросы на отзывы: N шт (от таких-то площадок, по таким-то заказам)". Если запрос связан с заказом, упоминаемым в другом письме — отметь связь.
- "Требующие ответа" — только письма, где реальный человек ждёт твоего ответа (вопрос, просьба, обсуждение).

Категории:

## 🔴 Важные
(срочные, от руководства, критичные уведомления, дедлайны)

## 💬 Требующие ответа
(вопросы, запросы, ожидающие реакции от реальных людей)

## 🧾 Счета / Бухгалтерия
(фактуры, акты, оплаты, всё где есть invoice/счёт)

## 📋 Обычные
(информационные, трекеры задач, обычная переписка, уведомления о заказах)

## 📰 Рассылки
(newsletters, промо, автоматические уведомления)

Для каждой записи укажи: отправитель, суть. Если в категории нет писем — не выводи её.
Язык ответа: {language}.`

const defaultNewsHeadlineExtract = `Ты — экстрактор новостных заголовков. Тебе дан текст главной страницы новостного сайта.

Твоя задача — извлечь ВСЕ новостные заголовки со страницы и вернуть их в формате JSON. Не фильтруй — бери всё что видишь (обычно 15-40 заголовков). Чем больше заголовков, тем лучше: это позволит найти общие темы между источниками.

ВАЖНО:
- Возвращай ТОЛЬКО валидный JSON, без пояснений, без markdown-блоков.
- URL статей должны быть абсолютными. Если на странице относительные ссылки — дополни их доменом источника.
- Теги: europe, international, economy, politics, war, technology, society, other.
- description — 1 предложение, конкретика (цифры, имена, факты). Если на странице нет описания — составь из контекста заголовка.

Формат:
{"source_name": "...", "source_url": "...", "headlines": [{"title": "...", "url": "...", "description": "...", "tags": ["europe", "economy"]}]}

Язык заголовков и описаний: {language}.`

const defaultNewsTopicCluster = `Ты — аналитик-группировщик новостей. Тебе дан JSON с заголовками новостей из нескольких источников одной тематической группы.

Твоя задача — сгруппировать заголовки по темам/событиям и вернуть результат в формате JSON.

ПРАВИЛА:
- Одно и то же событие из разных источников — одна тема.
- Уникальные новости (из одного источника) — отдельная тема каждая.
- topic_title — краткое название события/темы на {language}.
- brief — 1 предложение из исходного description.

Возвращай ТОЛЬКО валидный JSON, без пояснений.

Формат:
{"topics": [{"topic_title": "...", "articles": [{"source_name": "...", "title": "...", "article_url": "...", "brief": "..."}]}]}`

const defaultNewsTopicDeepDive = `Ты — аналитик новостей. Тема: "{topic_title}".

Источники по этой теме:
{source_list}

Твоя задача:
1. Загрузи каждую статью через инструмент web_fetch_summarize (prompt: "Извлеки ключевые факты, цифры, цитаты, позиции сторон из этой новостной статьи").
2. Сравни подачу разных источников: что подчёркивает каждый, какие детали добавляет или опускает.
3. Напиши краткий анализ (3-7 предложений): суть события + различия в подаче.

ВАЖНО:
- Будь конкретен: имена, цифры, факты.
- Отмечай пропагандистские приёмы если есть.
- Если статья не загрузилась — используй brief из заголовков.

Язык ответа: {language}.`

const defaultImapSummarize = `Summarize the following email concisely in 2-3 sentences. Focus on the main topic, key information, and any action items. Response language: {language}.`

const defaultImapDigest = `Analyze the email and its conversation history. Provide a structured response:

1. SUMMARY: 2-3 sentence summary of the email
2. CATEGORY: exactly one of: important | needs-reply | invoice/accounting | regular | newsletter/promo
3. CONVERSATION: if history exists, briefly describe the ongoing conversation topic and context. If no history, write "No prior conversation."

Response language: {language}.`
