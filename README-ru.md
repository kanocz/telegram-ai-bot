# ai-webfetch

Telegram бот и CLI-утилита: AI-ассистент с доступом к вебу, почте (IMAP) и Home Assistant.

## Конфигурация

### config.json — AI-модель и язык

Новый формат (с языком):

```json
{
  "model": {
    "Qwen/Qwen3-14B-AWQ": {
      "name": "qwen",
      "baseURL": "http://192.168.1.7:8020/v1",
      "limit": {
        "context": 40960,
        "output": 4096
      }
    }
  },
  "language": "русский"
}
```


### imap.json — доступ к почте

```json
{
  "server": "imap.example.com:993",
  "username": "user@example.com",
  "password": "your-password"
}
```

### homeassistant.json — Home Assistant

```json
{
  "url": "http://homeassistant.local:8123",
  "token": "your-long-lived-access-token"
}
```

Токен создаётся в Home Assistant: Профиль → Безопасность → Долгосрочные токены доступа → Создать токен.

### mcp.json — MCP-серверы (опционально)

```json
{
  "filesystem": {
    "url": "http://localhost:3001/mcp",
    "enabled": true,
    "headers": {}
  },
  "github": {
    "url": "https://api.github.com/mcp",
    "enabled": false,
    "headers": {
      "Authorization": "Bearer ghp_..."
    }
  }
}
```

- `enabled: true` — инструменты всегда доступны, сервер инициализируется при старте
- `enabled: false` — активируется только через `-enable-mcp name` или префикс `/mcp name`

Шаблон: `mcp.json.example`.

### telegram.json — Telegram Bot API

```json
{
  "token": "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
  "chat_id": {
    "news": [-1234585163, 987654321],
    "mail": [-1234585163],
    "other": [-1234585163]
  },
  "bot": {
    "webhook_url": "https://example.com/hook/SECRET",
    "listen": ":8443",
    "allowed_users": [123456789]
  }
}
```

Секция `bot` опциональна (нужна только для `-telegram-bot`).

## Использование

```
./ai-webfetch [-no-think] [-quiet] [-show-subagents] [-verbose-tools] [-telegram] [-language lang] [-config path] [-enable-mcp name1,name2] [-mcp-config path] <запрос>
./ai-webfetch -mail-summary [-no-think] [-quiet] [-show-subagents] [-telegram] [-language lang] [-config path]
./ai-webfetch -news-summary [-news-urls path] [-no-think] [-quiet] [-telegram] [-language lang] [-config path]
./ai-webfetch -telegram-bot [-telegram-config path] [-config path] [-mcp-config path]
./ai-webfetch -export-default-prompts <dir>
```

### Флаги

- `-no-think` — скрыть thinking-вывод модели (по умолчанию показывается серым)
- `-show-subagents` — показать работу суб-агентов: вход, thinking, ответ (с отступом ` | `)
- `-verbose-tools` — показать аргументы вызова и результат каждого tool (результат обрезается до 500 символов)
- `-mail-summary` — автономный дайджест почты: получить непрочитанные, сгруппировать по отправителям, категоризировать (без tool-loop)
- `-news-summary` — кросс-референсный дайджест новостей: загрузить URL из файла, суб-агенты анализируют каждый источник (с доступом к `web_fetch`), финальная сводка с группировкой по событиям и фокусом на Европу
- `-news-urls path` — путь к файлу с URL новостных сайтов (по умолчанию `news.urls`)
- `-quiet` — подавить весь не-ошибочный вывод (для cron); подразумевает `-no-think`
- `-telegram` — отправить результат в Telegram вместо вывода в stdout (требуется `telegram.json`)
- `-telegram-config path` — путь к конфигу Telegram (по умолчанию `telegram.json`)
- `-telegram-chatid id` — переопределить chat ID для одного запуска (все категории → один чат)
- `-telegram-bot` — запустить webhook-бот сервис (требуется секция `bot` в `telegram.json`)
- `-config path` — путь к config.json (по умолчанию `config.json`)
- `-language lang` — язык ответов (перекрывает значение из config.json; по умолчанию `русский`)
- `-enable-mcp name1,name2` — активировать MCP-серверы для этого запроса (через запятую)
- `-mcp-config path` — путь к конфигу MCP (по умолчанию `mcp.json`)
- `-export-default-prompts dir` — экспортировать дефолтные промпты в директорию и выйти
- `-prompts-dir dir` — загрузить промпты из директории (отсутствующие файлы → встроенные значения)

## Инструменты (tools)

| Tool | Описание |
|------|----------|
| `web_fetch` | Загрузить содержимое URL |
| `imap_list_mailboxes` | Список папок почтового ящика |
| `imap_list_messages` | Список писем (по количеству или за период) |
| `imap_read_message` | Полное содержимое письма по UID |
| `imap_summarize_message` | AI-суммаризация письма через суб-агент (экономит контекст) |
| `imap_digest_message` | Полный анализ: суммари + категория + история переписки (всё в суб-агенте) |
| `ha_list` | Обнаружение зон Home Assistant (с алиасами) и устройств в зоне |
| `ha_state` | Детальное состояние устройства с атрибутами по домену |
| `ha_call` | Вызов сервиса Home Assistant (включить/выключить, задать температуру и т.д.) |

## Кастомизация промптов

Экспорт дефолтных промптов для редактирования:

```bash
./ai-webfetch -export-default-prompts ./my-prompts
```

Создаёт 7 файлов: `system-prompt.txt`, `mail-digest-subagent.txt`, `mail-digest-final.txt`, `news-source-subagent.txt`, `news-final-synthesis.txt`, `imap-summarize.txt`, `imap-digest.txt`.

Использование отредактированных промптов:

```bash
./ai-webfetch -prompts-dir ./my-prompts "запрос"
./ai-webfetch -prompts-dir ./my-prompts -mail-summary
```

Смена языка без редактирования промптов:

```bash
./ai-webfetch -language English -news-summary
./ai-webfetch -language čeština -mail-summary
```

Приоритет языка: флаг `-language` > поле `language` в config.json > `"русский"`.

## Примеры

### Веб

```bash
./ai-webfetch "Кратко перескажи что на странице https://example.com"
```

### Почта — последние N писем

```bash
./ai-webfetch "Покажи последние 5 писем"
```

### Почта — за период

```bash
./ai-webfetch "Какие письма пришли за последние 3 часа?"
```

### Почта — эффективная суммаризация (суб-агент)

Каждое письмо обрабатывается в отдельном контексте, в основной попадает только краткая сводка:

```bash
./ai-webfetch "Получи список писем за последние 12 часов через imap_list_messages. \
Затем для каждого письма вызови imap_summarize_message с его UID. \
В конце выведи все сводки вместе."
```

### Почта — быстрый дайджест (standalone)

Автономный режим без tool-loop — Go напрямую получает письма, группирует по отправителям,
запускает суб-агент на каждую группу, затем финальная категоризация:

```bash
./ai-webfetch -mail-summary
./ai-webfetch -mail-summary -show-subagents
```

### Новости — кросс-референсный дайджест

Загружает URL из файла `news.urls`, суб-агенты анализируют каждый источник (с возможностью открывать полные статьи через `web_fetch`), затем финальная сводка с группировкой по событиям:

```bash
./ai-webfetch -news-summary
./ai-webfetch -news-summary -news-urls my-urls.txt
./ai-webfetch -news-summary -quiet -telegram    # cron-режим
```

Формат файла `news.urls`:

```
# Комментарии начинаются с #
https://www.novinky.cz/
https://www.chinadaily.com.cn/world/europe
https://www.bbc.com/news
```

### Почта — полный дайджест с историей переписки

Для каждого письма суб-агент получает содержимое, ищет историю переписки в INBOX и Sent, выдаёт суммари, категорию и контекст:

```bash
./ai-webfetch "Получи список непрочитанных писем за последние 24 часа через imap_list_messages. \
Затем для каждого письма вызови imap_digest_message с его UID. \
В конце сгруппируй результаты по категориям."
```

### Telegram — отправка результата

Вместо вывода в терминал результат отправляется в Telegram-чат:

```bash
./ai-webfetch -telegram "Покажи последние 3 письма"
./ai-webfetch -mail-summary -telegram
./ai-webfetch -telegram -telegram-config /path/to/tg.json "Кратко о https://example.com"
```

### Multi-chat routing

С новым форматом конфига результат отправляется в разные чаты по категориям:

```bash
./ai-webfetch -news-summary -telegram              # → chats.news[]
./ai-webfetch -mail-summary -telegram              # → chats.mail[]
./ai-webfetch -telegram "запрос"                   # → chats.other[]
```

Переопределение chat ID для одного запуска:

```bash
./ai-webfetch -telegram -telegram-chatid 123456789 "запрос"
```

### Telegram бот

Запуск webhook-бота:

```bash
./ai-webfetch -telegram-bot -telegram-config telegram.json
```

Команды бота:
- `/news` — дайджест новостей
- `/mail [часы]` — дайджест почты (по умолчанию 24 часа)
- `/mcp сервер1,сервер2 <запрос>` — запрос с MCP-инструментами
- `/mcp сервер /news` — дайджест новостей с MCP-инструментами
- `/mcp сервер /mail [часы]` — дайджест почты с MCP-инструментами
- любой текст — свободный запрос с tool-loop

### MCP-инструменты

Использование внешних инструментов через MCP-серверы (требуется `mcp.json`):

```bash
# Активировать отключённый MCP-сервер для этого запроса
./ai-webfetch -enable-mcp github "Покажи открытые issues в myrepo"

# Через префикс /mcp в запросе
./ai-webfetch "/mcp github Покажи открытые issues в myrepo"

# Несколько серверов
./ai-webfetch -enable-mcp github,filesystem "Найди README в myrepo"

# Дайджест новостей с MCP-инструментами для суб-агентов
./ai-webfetch -news-summary -enable-mcp search
```

Серверы с `"enabled": true` в `mcp.json` доступны всегда без `-enable-mcp`.

Имена инструментов содержат префикс сервера: `github__list_issues`, `filesystem__read_file` и т.д.

### Умный дом

Управление устройствами Home Assistant на естественном языке (требуется `homeassistant.json`):

```bash
./ai-webfetch "Выключи свет на втором этаже"
./ai-webfetch "Сколько градусов в спальне?"
./ai-webfetch "Открой шторы в гостинной"
```

Ассистент автоматически обнаруживает зоны и устройства через `ha_list`, читает состояния через `ha_state` и управляет устройствами через `ha_call`. Используются алиасы зон и устройств, настроенные в Home Assistant (включая алиасы для голосовых ассистентов).

### Отладка суб-агентов

С флагом `-show-subagents` видна работа каждого суб-агента с отступом ` | `:

```bash
./ai-webfetch -show-subagents "Суммаризируй почту за последние 12 часов"
```

Вывод будет выглядеть примерно так:

```
[tool: imap_list_messages({"since_hours":12})]
[tool: imap_summarize_message({"uid":5247})]
 | --- sub-agent ---
 | System: Summarize the following email concisely...
 | Input: From: news@example.com...
 |
 | [thinking в dim-цвете...]
 |
 | Письмо от Forbes Espresso о решении Верховного суда...
 |
 | --- end sub-agent ---
[tool: imap_summarize_message({"uid":5246})]
 | --- sub-agent ---
 | ...
 | --- end sub-agent ---
```

При вложенных суб-агентах (агент вызывает агента) отступ увеличивается:
` | ` → ` |  | ` → ` |  |  | ` и т.д.

### Отладка вызовов tools

С флагом `-verbose-tools` видны аргументы и результат каждого вызова:

```bash
./ai-webfetch -verbose-tools "Покажи последние 3 письма"
```

```
[tool: imap_list_messages]
  args: {"mailbox":"INBOX","limit":3}
  result: UID: 5247
Date: 2026-02-21T10:30:00+01:00
From: news@example.com
Subject: Daily digest
...
```

## Добавление новых tools

Создайте файл в `tools/`, зарегистрируйте через `init()`:

```go
package tools

func init() {
    Register(&Tool{
        Def: Definition{
            Type: "function",
            Function: Function{
                Name:        "my_tool",
                Description: "...",
                Parameters:  Parameters{...},
            },
        },
        Execute: func(args json.RawMessage) (string, error) {
            // ...
        },
    })
}
```

Для tools, которым нужен AI (суб-агент), используйте `SubAgentFn`:

```go
summary, err := SubAgentFn(systemPrompt, userMessage)
```
