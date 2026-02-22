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
	NewsSourceSubAgent string
	NewsFinalSynthesis string
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
	{"news-source-subagent.txt", func(p *Prompts) *string { return &p.NewsSourceSubAgent }},
	{"news-final-synthesis.txt", func(p *Prompts) *string { return &p.NewsFinalSynthesis }},
	{"imap-summarize.txt", func(p *Prompts) *string { return &p.ImapSummarize }},
	{"imap-digest.txt", func(p *Prompts) *string { return &p.ImapDigest }},
}

func defaultPrompts() Prompts {
	return Prompts{
		SystemPrompt:       defaultSystemPrompt,
		MailDigestSubAgent: defaultMailDigestSubAgent,
		MailDigestFinal:    defaultMailDigestFinal,
		NewsSourceSubAgent: defaultNewsSourceSubAgent,
		NewsFinalSynthesis: defaultNewsFinalSynthesis,
		ImapSummarize:      defaultImapSummarize,
		ImapDigest:         defaultImapDigest,
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
Response language: {language}.

Rules:
- When summarizing multiple emails, prefer imap_summarize_message (processes each email in a separate context) over imap_read_message to avoid exceeding the context window.
- NEVER make assumptions about data you haven't retrieved. If the user asks about correspondence history, message counts, or any email data — you MUST call the appropriate tool to get the actual data. Do not guess or assume "no messages found" without making the tool call.
- When asked to check correspondence with a sender, use imap_list_messages with the "participant" filter and appropriate "since_hours" to search both INBOX and Sent. You must do this for EACH sender the user asks about.
- Execute ALL steps the user requested, even if there are many tool calls needed. Do not skip steps to save time.
- For smart home requests: always start with ha_list(target="areas") to discover available areas, then ha_list(target="<area_id>") to find entities before controlling them. Never guess entity IDs.`

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

const defaultNewsSourceSubAgent = `Ты — аналитик новостей. Тебе дан текст главной страницы новостного сайта.

Твоя задача:
1. Извлеки 5-10 самых важных/заметных новостей с этой страницы.
2. Для 2-3 самых важных статей — используй инструмент web_fetch_summarize чтобы получить ключевые детали. В параметре prompt укажи что именно извлечь (например: "Извлеки ключевые факты, цифры, цитаты и детали из этой новостной статьи").
3. Для каждой новости укажи тег темы: [Европа], [Политика], [Экономика], [Война/Конфликты], [Технологии], [Общество] или другой подходящий.

Формат вывода для каждой новости:
[Тег] **Заголовок** — краткое описание (1-2 предложения). Если загружал статью через web_fetch_summarize — добавь ключевые детали.

Язык ответа: {language}. Будь конкретен, избегай общих фраз.`

const defaultNewsFinalSynthesis = `Ты — аналитик-редактор новостного дайджеста. Тебе даны дайджесты новостей от нескольких источников.

Твоя задача — создать кросс-референсную сводку, группируя новости по СОБЫТИЯМ (не по источникам).

ВАЖНЫЕ ПРАВИЛА:
- Группируй одинаковые события из разных источников вместе
- Отмечай различия в подаче: что подчёркивает каждый источник, какие детали опускает
- Обращай особое внимание на пропагандистские приёмы и однобокую подачу
- Фокус на Европу — европейские новости выделяй в первую очередь
- Для каждого события указывай источники в скобках

Структура отчёта:

## 🇪🇺 Европа
(события, касающиеся Европы — политика, экономика, общество)

## 🌍 Международные события
(мировые события вне Европы)

## 💰 Экономика
(экономические новости, рынки, бизнес)

## ⚡ Прочее
(технологии, наука, спорт, курьёзы)

## 🔍 Кросс-анализ
- Какие события освещены несколькими источниками? Как различается подача?
- Пропагандистские приёмы, если обнаружены
- Умолчания: отмечай ТОЛЬКО если крупное международное событие намеренно проигнорировано источником, для которого оно релевантно. НЕ отмечай как умолчание то, что региональный сайт не пишет о событиях другого региона — это нормально (чешские СМИ не обязаны писать о внутренних делах Китая, и наоборот)

Если в категории нет новостей — не выводи её (кроме Кросс-анализа — он обязателен).
Язык ответа: {language}.`

const defaultImapSummarize = `Summarize the following email concisely in 2-3 sentences. Focus on the main topic, key information, and any action items. Response language: {language}.`

const defaultImapDigest = `Analyze the email and its conversation history. Provide a structured response:

1. SUMMARY: 2-3 sentence summary of the email
2. CATEGORY: exactly one of: important | needs-reply | invoice/accounting | regular | newsletter/promo
3. CONVERSATION: if history exists, briefly describe the ongoing conversation topic and context. If no history, write "No prior conversation."

Response language: {language}.`
