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


### users.json — настройки пользователей

```json
{
  "alice": {
    "telegram_id": 123456789,
    "language": "čeština",
    "chats": {
      "news": 2342344,
      "mail": 3453454,
      "other": 3453454
    },
    "imap": {
      "server": "imap.example.com:993",
      "username": "alice@example.com",
      "password": "alice-password"
    },
    "homeassistant": {
      "enabled": true
    },
    "mcp": {
      "context7": true,
      "github": false
    }
  }
}
```

- Ключ = имя пользователя (используется флагом CLI `-user alice`)
- `telegram_id` = Telegram user ID (бот автоматически определяет пользователя)
- `language` = язык по умолчанию для автоматических задач (опционально; на интерактивные вопросы модель отвечает на языке вопроса)
- `chats` = Telegram chat ID для маршрутизации (news/mail/other); используется флагом `-telegram`
- `imap` = IMAP-данные (опционально; если отсутствует, IMAP-инструменты скрываются)
- `homeassistant` = доступ к HA (опционально; если отсутствует или `enabled: false`, HA-инструменты скрываются)
- `mcp` = per-user MCP-серверы (опционально; `true` включает, `false` отключает)
- CLI: если в конфиге один пользователь, он выбирается автоматически без `-user`

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
  "bot": {
    "webhook_url": "https://example.com/hook/SECRET",
    "listen": ":8443",
    "allow_unregistered_users": false
  }
}
```

Секция `bot` опциональна (нужна только для `-telegram-bot`). Маршрутизация чатов и доступ пользователей настраиваются в `users.json`.

## Использование

```
./ai-webfetch [flags] <запрос>
./ai-webfetch -mail-summary [flags]
./ai-webfetch -news-summary [-news-config path] [flags]
./ai-webfetch -telegram-bot [-telegram-config path] [-config path] [-mcp-config path]
./ai-webfetch -export-default-prompts <dir>
```

### Флаги

- `-no-think` — скрыть thinking-вывод модели (по умолчанию показывается серым)
- `-enable-thinking` — явно включить thinking/reasoning модели (отправляет `enable_thinking: true` в API)
- `-disable-thinking` — полностью отключить thinking/reasoning модели (отправляет `enable_thinking: false` в API); также подразумевает `-no-think`
- `-request-debug` — дамп JSON API-запроса в stderr (base64-данные обрезаются)
- `-show-subagents` — показать работу суб-агентов: вход, thinking, ответ (с отступом ` | `)
- `-verbose-tools` — показать аргументы вызова и результат каждого tool (результат обрезается до 500 символов)
- `-user name` — выбрать пользователя из `users.json` по имени (автовыбор при одном пользователе); включает IMAP, HA, MCP по конфигу пользователя
- `-mail-summary` — автономный дайджест почты: получить непрочитанные, сгруппировать по отправителям, категоризировать (без tool-loop)
- `-news-summary` — кросс-референсный дайджест новостей: загрузить URL из файла, суб-агенты анализируют каждый источник (с доступом к `web_fetch`), финальная сводка с группировкой по событиям и фокусом на Европу
- `-news-config path` — путь к конфигу новостей (по умолчанию `news.json`)
- `-image path` — прикрепить изображение к запросу (vision); изображение отправляется как base64 data URI
- `-video path` — прикрепить видео к запросу (vision); видео отправляется как base64 data URI
- `-quiet` — подавить весь не-ошибочный вывод (для cron); подразумевает `-no-think`
- `-telegram` — отправить результат в Telegram вместо вывода в stdout (требуется `telegram.json`)
- `-telegram-config path` — путь к конфигу Telegram (по умолчанию `telegram.json`)
- `-telegram-chatid id` — переопределить chat ID для одного запуска (все категории → один чат)
- `-telegram-bot` — запустить webhook-бот сервис (требуется секция `bot` в `telegram.json`)
- `-config path` — путь к config.json (по умолчанию `config.json`)
- `-language lang` — язык ответов (перекрывает значение из config.json; по умолчанию `русский`)
- `-enable-mcp name1,name2` — активировать MCP-серверы для этого запроса (через запятую)
- `-mcp-config path` — путь к конфигу MCP (по умолчанию `mcp.json`)
- `-skills name1,name2` — активировать скиллы по имени (через запятую); также доступно как префикс `/skills name1,name2` в запросе
- `-skills-dir path` — переопределить директорию скиллов (по умолчанию: поиск в нескольких местах, см. ниже)
- `-filesystem path` — включить инструменты файловой системы (`file_read`, `file_list`, `file_tree`) в песочнице указанной директории
- `-filesystem-rw` — также включить инструменты записи (`file_write`, `file_patch`); требует `-filesystem`
- `-git` — включить инструменты git-истории (`git_log`, `git_show`, `git_diff`); репозиторий = директория `-filesystem` или cwd
- `-git-dir path` — включить git-инструменты для конкретного репозитория (подразумевает `-git`)
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
| `ha_camera_snapshot` | Снимок с камеры Home Assistant (изображение возвращается для визуального анализа) |
| `fs_list` | Список содержимого директории (требует `-filesystem`) |
| `fs_read` | Чтение файла (требует `-filesystem`) |
| `fs_info` | Метаданные файла/директории (требует `-filesystem`) |
| `fs_write` | Запись файла (требует `-filesystem -filesystem-rw`) |
| `fs_append` | Дозапись в файл (требует `-filesystem -filesystem-rw`) |
| `fs_patch` | Патч файла поиском/заменой (требует `-filesystem -filesystem-rw`) |
| `fs_mkdir` | Создание директории (требует `-filesystem -filesystem-rw`) |
| `fs_rm` | Удаление файла/директории (требует `-filesystem -filesystem-rw`) |
| `git_log` | История коммитов (требует `-git`) |
| `git_show` | Детали коммита (требует `-git`) |
| `git_diff` | Diff между коммитами/ветками (требует `-git`) |

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
./ai-webfetch -mail-summary -user alice
./ai-webfetch -mail-summary -show-subagents
```

### Новости — кросс-референсный дайджест

Суб-агенты анализируют каждый источник (с возможностью открывать полные статьи через `web_fetch`), затем финальная сводка с группировкой по событиям:

```bash
./ai-webfetch -news-summary
./ai-webfetch -news-summary -news-config my-news.json
./ai-webfetch -news-summary -quiet -telegram    # cron-режим
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

Результат отправляется в чат пользователя по категории (из `users.json`):

```bash
./ai-webfetch -user alice -news-summary -telegram   # → alice's chats.news
./ai-webfetch -user alice -mail-summary -telegram   # → alice's chats.mail
./ai-webfetch -user alice -telegram "запрос"         # → alice's chats.other
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
- `/think <запрос>` — включить thinking модели для этого запроса
- `/nothink <запрос>` — отключить thinking модели для этого запроса
- `/mcp сервер1,сервер2 <запрос>` — запрос с MCP-инструментами
- `/mcp сервер /news` — дайджест новостей с MCP-инструментами
- `/mcp сервер /mail [часы]` — дайджест почты с MCP-инструментами
- `/skills имя1,имя2 <запрос>` — запрос с добавлением скиллов в системный промпт
- любой текст — свободный запрос с tool-loop
- фото с подписью — vision-запрос (подпись = промпт; без подписи = «Опиши это изображение»)
- видео с подписью — vision-запрос (подпись = промпт; без подписи = «Опиши это видео»)
- **reply на любое сообщение** — продолжает диалог с полным контекстом

Префиксы можно комбинировать: `/think /skills code-review /mcp github что нового?`

#### Многоходовые диалоги (threading)

Бот поддерживает многоходовые диалоги через reply в Telegram. Когда вы отвечаете (reply) на сообщение бота или своё собственное, вся цепочка ответов восстанавливается и передаётся AI-модели как история разговора.

Как это работает:
1. Вы пишете «Что такое Python?» — бот отвечает reply'ем
2. Вы делаете reply на ответ бота «А Java?» — бот видит полную цепочку: ваш вопрос, свой ответ и ваш уточняющий вопрос
3. Можно продолжать отвечать reply'ями, выстраивая многоходовый диалог (до 20 сообщений в глубину)

Бот всегда отвечает reply'ем на ваше сообщение, поэтому продолжить любой разговор можно просто ответив на него. Сообщения хранятся в памяти (до 1000 на чат); при перезапуске бота используется текст из `reply_to_message` Telegram как fallback на одно сообщение контекста.

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

### Скиллы (skills)

Скиллы — markdown-файлы с инструкциями, которые добавляются в системный промпт, давая модели дополнительные инструкции или контекст.

Скилл может быть как отдельным файлом (`name.md`), так и директорией с `SKILL.md` внутри (`name/SKILL.md`).

Директории поиска (первое совпадение побеждает):

**Глобальные** (от `$HOME`):
- `~/.claude/skills/`
- `~/.agents/skills/`
- `~/.copilot/skills/`

**Локальные** (от рабочей директории):
- `.github/skills/`
- `.claude/skills/`
- `.agents/skills/`

Флаг `-skills-dir path` переопределяет поиск — будет искать только в указанной директории.

```bash
# Активация скилла через флаг CLI
./ai-webfetch -skills code-review "сделай код ревью"

# Несколько скиллов
./ai-webfetch -skills code-review,haiku "проверь мой код"

# Через префикс /skills в запросе
./ai-webfetch "/skills code-review сделай код ревью"

# В комбинации с другими префиксами
./ai-webfetch "/nothink /skills haiku привет"
```

### Режим thinking

Некоторые модели (например Qwen3, Qwen3.5) поддерживают режим thinking/reasoning. По умолчанию решает модель; можно явно включить или отключить:

```bash
# Включить thinking через флаг CLI (глобально)
./ai-webfetch -enable-thinking "реши эту задачу"

# Включить thinking через префикс /think (per-query)
./ai-webfetch "/think сколько будет 25*37?"

# Отключить thinking через флаг CLI (глобально)
./ai-webfetch -disable-thinking "запрос"

# Отключить thinking через префикс /nothink (per-query)
./ai-webfetch "/nothink сколько будет 2+2?"

# Комбинация с другими префиксами
./ai-webfetch "/think /mcp github что нового?"
./ai-webfetch "/nothink /skills code-review проверь код"
```

Для `-news-summary` thinking автоматически отключается на фазах 1 (извлечение заголовков) и 2 (кластеризация по темам) вне зависимости от флагов; фаза 3 (deep dive) учитывает флаг.

### Изображения (vision)

Прикрепить изображение к запросу для моделей с поддержкой vision:

```bash
./ai-webfetch -image photo.jpg "Что на этом изображении?"
./ai-webfetch -disable-thinking -image screenshot.png "Опиши что видишь"
```

В режиме Telegram-бота отправьте фото с подписью (caption используется как промпт). Если подписи нет, бот использует промпт по умолчанию для описания изображения.

### Видео (vision)

Прикрепить видео к запросу для моделей с поддержкой vision:

```bash
./ai-webfetch -video clip.mp4 "Что происходит на этом видео?"
./ai-webfetch -disable-thinking -video recording.mp4 "Опиши что видишь"
```

В режиме Telegram-бота отправьте видео или видеосообщение с подписью (caption используется как промпт). Если подписи нет, бот использует промпт по умолчанию для описания видео.

### Умный дом

Управление устройствами Home Assistant на естественном языке (требуется `homeassistant.json`):

```bash
./ai-webfetch "Выключи свет на втором этаже"
./ai-webfetch "Сколько градусов в спальне?"
./ai-webfetch "Открой шторы в гостинной"
```

Ассистент автоматически обнаруживает зоны и устройства через `ha_list`, читает состояния через `ha_state`, управляет устройствами через `ha_call` и делает снимки с камер через `ha_camera_snapshot` для визуального анализа. Используются алиасы зон и устройств, настроенные в Home Assistant (включая алиасы для голосовых ассистентов).

Пример диалога в Telegram:

> **Вы:** Что происходит в гостиной?
>
> **Бот:** В гостиной сейчас происходит следующее:
>
> **Люди:**
> - Один человек за столом с ноутбуком, работает
> - Другой человек сидит в кресле с белым пледом
>
> **Условия:**
> - Температура: 22.0°C
> - Влажность: 24%
> - Освещение: выключено
> - Шторы: открыты
>
> **Заметки:**
> - На столах разные вещи (книги, коробки, фрукты, бутылки, растения)
> - Камера ведёт запись
>
> Гостиная сейчас используется как рабочее пространство.

Ассистент использовал `ha_list` для поиска устройств в зоне, `ha_camera_snapshot` для снимка с камеры и `ha_state` для чтения показаний датчиков — всё автоматически из одного вопроса на естественном языке.

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
