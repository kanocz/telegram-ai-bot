# ai-webfetch

Telegram bot and CLI tool: AI assistant with web, email (IMAP), Home Assistant, calendar (CalDAV/iCal), contacts (CardDAV), and persistent memory access.

## Configuration

All config files are looked up from a single **config directory**:

1. If `-config path/to/config.json` is given — the directory of that file is used (e.g. `-config /etc/mybot/config.json` → `/etc/mybot/`)
2. Otherwise — `~/.config/tgbot/`

Individual flags (`-telegram-config`, `-news-config`, `-mcp-config`) override specific files. Files without a dedicated flag (`users.json`, `homeassistant.json`) always come from the config directory.

### config.json — AI model and language

New format (with language):

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

### users.json — per-user settings

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
    },
    "memory": "/home/alice/.ai-memory",
    "userinfo": "/home/alice/.ai-userinfo.json"
  }
}
```

- Key = human-readable name (used by CLI flag `-user alice`)
- `telegram_id` = Telegram user ID (bot auto-matches by this)
- `language` = default response language for automated tasks (optional; the model always responds in the language of the question for interactive queries)
- `chats` = Telegram chat IDs for routing (news/mail/other); used by `-telegram` flag
- `imap` = IMAP credentials (optional; if missing, IMAP tools are hidden)
- `homeassistant` = HA access (optional; if missing or `enabled: false`, HA tools are hidden)
- `calendar` = CalDAV/iCal settings (optional; if missing, calendar tools are hidden). Can have `server` (CalDAV), `ical_urls` (subscriptions), or both. `writable: true` enables create/update/delete. A user can have only `ical_urls` without a CalDAV server for read-only calendar access.
- `contacts` = CardDAV settings (optional; if missing, contacts tools are hidden). `writable: true` enables create/update/delete.
- `mcp` = per-user MCP server overrides (optional; `true` enables, `false` disables)
- `memory` = path to persistent memory directory (optional; if missing, memory tools are hidden). Overridden by `-memory` flag, disabled by `-memory off`
- `userinfo` = path to user settings JSON file (optional; if missing, userinfo tools are hidden). Overridden by `-userinfo` flag, disabled by `-userinfo off`. Settings with `in_prompt=true` or matching `only_for` are automatically injected into the system prompt
- CLI: if only one user exists, it is auto-selected without `-user`

### homeassistant.json — Home Assistant

```json
{
  "url": "http://homeassistant.local:8123",
  "token": "your-long-lived-access-token"
}
```

Generate a token in Home Assistant: Profile → Security → Long-lived access tokens → Create token.

### mcp.json — MCP servers (optional)

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

- `enabled: true` — tools always available, server initialized at startup
- `enabled: false` — only activated via `-enable-mcp name` or `/mcp name` prefix

See `mcp.json.example` for a template.

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

The `bot` section is optional (only required for `-telegram-bot`). Chat routing and user access are configured in `users.json`.

## Usage

```
./ai-webfetch [flags] <query>
./ai-webfetch .                                         # interactive mode with cwd filesystem + git
./ai-webfetch -interactive [flags]                      # interactive chat REPL
./ai-webfetch -news-interactive [flags]                 # interactive news REPL
./ai-webfetch -news-summary [topic] [flags]             # news digest (full, by category, or by topic)
./ai-webfetch -mail-summary [flags]
./ai-webfetch -telegram-bot [-telegram-config path] [-config path] [-mcp-config path]
./ai-webfetch -export-default-prompts <dir>
```

### Flags

- `-no-think` — hide model thinking output (shown dimmed by default)
- `-enable-thinking` — explicitly enable model thinking/reasoning (sends `enable_thinking: true` to the API)
- `-disable-thinking` — disable model thinking/reasoning entirely (sends `enable_thinking: false` to the API); also forces `-no-think`
- `-request-debug` — dump API request JSON to stderr (base64 data truncated)
- `-show-subagents` — show sub-agent activity: input, thinking, and output (indented with ` | `)
- `-verbose-tools` — show tool call arguments and results (results truncated to 500 chars)
- `-user name` — select user from `users.json` by name (auto-selects if only one user); enables IMAP, HA, MCP per user config
- `-interactive` (alias: `-cli`) — interactive chat REPL with tools, skills, MCP, context tracking, `/compact`, and `@file` support
- `-news-interactive` — interactive news analysis REPL (same as `-interactive` but news-focused prompt)
- `-mail-summary` — standalone mail digest: fetch unread, group by sender, categorize (no tool-loop)
- `-news-summary [topic]` — news digest. Without arguments: full cross-referenced summary. With a category name (e.g. `europe`): interactive browse. With free text: topic search across all sources with keyword pre-filtering
- `-news-config path` — path to news config file (default: `<config-dir>/news.json`)
- `-image path` — attach an image file to the query (vision); the image is sent as a base64 data URI
- `-video path` — attach a video file to the query (vision); the video is sent as a base64 data URI
- `-quiet` — suppress all non-error output (for cron); implies `-no-think`
- `-telegram` — send output to Telegram instead of stdout (requires `telegram.json`)
- `-telegram-config path` — path to Telegram config (default: `<config-dir>/telegram.json`)
- `-telegram-chatid id` — override chat ID for a single invocation (all categories go to one chat)
- `-telegram-bot` — run as Telegram webhook bot service (requires `bot` section in `telegram.json`)
- `-config path` — path to config.json; also sets the base directory for all other configs (default: `~/.config/tgbot/config.json`)
- `-language lang` — response language (overrides config.json; default `русский`)
- `-enable-mcp name1,name2` — activate MCP servers for this query (comma-separated)
- `-mcp-config path` — path to MCP config file (default: `<config-dir>/mcp.json`)
- `-skills name1,name2` — activate skills by name (comma-separated); also available as `/skills name1,name2` query prefix
- `-skills-dir path` — override skills directory (default: searches multiple locations, see below)
- `-filesystem path` — enable filesystem tools (`fs_list`, `fs_read`, `fs_info`, `fs_grep`) sandboxed to this directory
- `-filesystem-rw` — also enable write tools (`fs_write`, `fs_patch`, etc.); requires `-filesystem`
- `-git` — enable git history tools (`git_log`, `git_show`, `git_diff`); repo = `-filesystem` dir or cwd
- `-git-dir path` — enable git history tools on a specific repo (implies `-git`)
- `-no-ask` — disable the `ask_user` tool (for cron/scripting; the tool is also hidden in `-quiet`, `-telegram`, `-mail-summary`, and `-news-summary` modes)
- `-memory path` — enable persistent memory tools at this directory; overrides `memory` from `users.json`. Use `-memory off` to disable memory even if configured in user settings
- `-userinfo path` — enable user settings tools with this JSON file; overrides `userinfo` from `users.json`. Use `-userinfo off` to disable even if configured in user settings
- `-export-default-prompts dir` — export default prompts to a directory and exit
- `-prompts-dir dir` — load prompts from directory (missing files fall back to built-in defaults)

## Tools

| Tool | Description |
|------|-------------|
| `web_fetch` | Fetch URL contents |
| `imap_list_mailboxes` | List mailbox folders |
| `imap_list_messages` | List messages (by count or time period) |
| `imap_read_message` | Full message content by UID |
| `imap_summarize_message` | AI summarization via sub-agent (saves context) |
| `imap_digest_message` | Full analysis: summary + category + conversation history (all in sub-agent) |
| `ha_list` | Discover Home Assistant areas (with aliases) and entities in an area |
| `ha_state` | Detailed entity state with domain-specific attributes |
| `ha_call` | Call a Home Assistant service (turn on/off, set temperature, etc.) |
| `ha_camera_snapshot` | Capture a snapshot from a Home Assistant camera (image returned for visual analysis) |
| `cal_list` | List CalDAV calendars and iCal subscriptions |
| `cal_events` | Query events by date range, search text, calendar filter |
| `cal_event` | Full event details by path (CalDAV only) |
| `cal_create_event` | Create a new CalDAV event (requires `writable: true`) |
| `cal_update_event` | Update an existing CalDAV event (requires `writable: true`) |
| `cal_delete_event` | Delete a CalDAV event (requires `writable: true`) |
| `contacts_search` | Search contacts by name, email, or phone |
| `contacts_get` | Full contact details by path |
| `contacts_create` | Create a new CardDAV contact (requires `writable: true`) |
| `contacts_update` | Update an existing contact (requires `writable: true`) |
| `contacts_delete` | Delete a contact (requires `writable: true`) |
| `fs_list` | List directory contents (requires `-filesystem`) |
| `fs_read` | Read file contents (requires `-filesystem`) |
| `fs_info` | File/directory metadata (requires `-filesystem`) |
| `fs_grep` | Search file contents by text or regex (requires `-filesystem`) |
| `fs_write` | Write file (requires `-filesystem -filesystem-rw`) |
| `fs_append` | Append to file (requires `-filesystem -filesystem-rw`) |
| `fs_patch` | Patch file with search/replace (requires `-filesystem -filesystem-rw`) |
| `fs_mkdir` | Create directory (requires `-filesystem -filesystem-rw`) |
| `fs_rm` | Remove file/directory (requires `-filesystem -filesystem-rw`) |
| `git_log` | Git commit history (requires `-git`) |
| `git_show` | Show commit details (requires `-git`) |
| `git_diff` | Diff between commits/refs (requires `-git`) |
| `ask_user` | Ask user a question with optional choices (interactive CLI and Telegram bot) |
| `memory_store` | Store/update an entity (contact, topic, preference) or log a timestamped episode (requires `-memory` or `memory` in users.json) |
| `memory_search` | Search memories by text, type, or tags (requires `-memory`) |
| `memory_recall` | Full details of an entity: facts, relations, linked episodes (requires `-memory`) |
| `memory_forget` | Delete an entity (with linked episodes) or a specific episode (requires `-memory`) |
| `userinfo_set` | Set a user preference with optional `in_prompt` and `only_for` flags (requires `-userinfo` or `userinfo` in users.json) |
| `userinfo_get` | Get a specific user setting by key (requires `-userinfo`) |
| `userinfo_list` | List all user settings, optionally with full details (requires `-userinfo`) |
| `userinfo_delete` | Delete a user setting by key (requires `-userinfo`) |

## Prompt customization

Export default prompts for editing:

```bash
./ai-webfetch -export-default-prompts ./my-prompts
```

Creates 7 files: `system-prompt.txt`, `mail-digest-subagent.txt`, `mail-digest-final.txt`, `news-source-subagent.txt`, `news-final-synthesis.txt`, `imap-summarize.txt`, `imap-digest.txt`.

Using edited prompts:

```bash
./ai-webfetch -prompts-dir ./my-prompts "query"
./ai-webfetch -prompts-dir ./my-prompts -mail-summary
```

Changing language without editing prompts:

```bash
./ai-webfetch -language English -news-summary
./ai-webfetch -language čeština -mail-summary
```

Language priority: `-language` flag > `language` field in config.json > `"русский"`.

## Examples

### Web

```bash
./ai-webfetch "Briefly summarize what's on https://example.com"
```

### Mail — last N messages

```bash
./ai-webfetch "Show the last 5 emails"
```

### Mail — by time period

```bash
./ai-webfetch "What emails arrived in the last 3 hours?"
```

### Mail — efficient summarization (sub-agent)

Each email is processed in a separate context; only a brief summary enters the main context:

```bash
./ai-webfetch "Get the list of emails from the last 12 hours via imap_list_messages. \
Then call imap_summarize_message for each email by UID. \
Finally, output all summaries together."
```

### Mail — quick digest (standalone)

Standalone mode without tool-loop — Go directly fetches emails, groups by sender,
runs a sub-agent on each group, then performs final categorization:

```bash
./ai-webfetch -mail-summary
./ai-webfetch -mail-summary -user alice
./ai-webfetch -mail-summary -show-subagents
```

### News — cross-referenced digest

Sub-agents analyze each news source (with ability to open full articles via `web_fetch`), then produces a final summary grouped by events:

```bash
./ai-webfetch -news-summary
./ai-webfetch -news-summary -news-config my-news.json
./ai-webfetch -news-summary -quiet -telegram    # cron mode
```

### News — interactive category browse

Browse a specific news category, see clustered topics, pick one for deep analysis:

```bash
./ai-webfetch -news-summary europe           # browse Europe category
./ai-webfetch -news-summary война            # browse War category
./ai-webfetch -news-summary экономика        # browse Economics category
```

Category names are matched flexibly: `europe`, `европа`, `eu`, `war`, `война`, `economics`, `экономика`, `world`, `мир`, `czech`, `чехия`, `cz`, etc.

### News — topic search

Search for a specific topic across all news sources:

```bash
./ai-webfetch -news-summary "2026 elections in Czech Republic"
./ai-webfetch -news-summary "выборы 2026 в Чехии"
./ai-webfetch -news-summary "Trump tariffs"
```

The search uses keyword pre-filtering: the LLM first generates multilingual keywords, then only pages containing matching keywords are processed (saving time on irrelevant sources).

### Interactive mode

Start an interactive REPL session with full tool support:

```bash
# General interactive mode
./ai-webfetch -interactive

# With MCP and skills
./ai-webfetch -interactive -enable-mcp github -skills code-review

# News-focused interactive mode
./ai-webfetch -news-interactive

# Dot shortcut: interactive + filesystem (cwd, read-write) + git
./ai-webfetch .
```

The dot shortcut (`.`) is the quickest way to start chatting with the AI about files in the current directory. It auto-enables filesystem tools (read+write, sandboxed to cwd) and git history tools.

REPL commands:
- `<text>` — send query to AI (with full conversation history)
- `/news [topic]` — news commands (full summary, category browse, or topic search)
- `@file` — attach file contents to the query (`@"path with spaces"` for quoted paths)
- `/compact` — compact context (summarize conversation history to save tokens)
- `/help` — show available commands
- `/exit` — exit (or `/quit`, `/q`, Ctrl+D)

Context tracking: after each response, the current context usage is displayed with a progress bar. When usage exceeds 80%, the context is automatically compacted.

```
[контекст: ~12k/32k токенов [████████░░░░░░░░░░░░] 38%]
```

### Telegram bot — interactive news

The Telegram bot also supports interactive news modes:

```
/news              — full news digest
/news europe       — browse Europe category (with inline keyboard for topic selection)
/news выборы 2026  — search for a specific topic across all sources
```

### Mail — full digest with conversation history

For each email, a sub-agent fetches content, searches conversation history in INBOX and Sent, and outputs summary, category, and context:

```bash
./ai-webfetch "Get unread emails from the last 24 hours via imap_list_messages. \
Then call imap_digest_message for each email by UID. \
Finally, group results by category."
```

### Telegram — sending output

Instead of terminal output, results are sent to a Telegram chat:

```bash
./ai-webfetch -telegram "Show the last 3 emails"
./ai-webfetch -mail-summary -telegram
./ai-webfetch -telegram -telegram-config /path/to/tg.json "Summarize https://example.com"
```

### Multi-chat routing

Results are sent to the user's configured chat by category (from `users.json`):

```bash
./ai-webfetch -user alice -news-summary -telegram   # → alice's chats.news
./ai-webfetch -user alice -mail-summary -telegram   # → alice's chats.mail
./ai-webfetch -user alice -telegram "query"          # → alice's chats.other
```

Override chat ID for a single invocation:

```bash
./ai-webfetch -telegram -telegram-chatid 123456789 "query"
```

### Telegram bot

Start the webhook bot:

```bash
./ai-webfetch -telegram-bot -telegram-config telegram.json
```

Bot commands:
- `/news` — full news digest
- `/news <category>` — interactive category browse (e.g. `/news europe`, `/news война`)
- `/news <topic>` — topic search across all sources (e.g. `/news выборы 2026`)
- `/mail [hours]` — mail digest (default 24 hours)
- `/think <query>` — enable model thinking for this query
- `/nothink <query>` — disable model thinking for this query
- `/mcp server1,server2 <query>` — query with MCP tools activated
- `/mcp server /news` — news digest with MCP tools
- `/mcp server /mail [hours]` — mail digest with MCP tools
- `/skills name1,name2 <query>` — query with skills injected into system prompt
- `/<skillname> <query>` — skill shortcut (auto-loads the skill if it exists and is not a reserved command)
- any text — free-form query with tool-loop
- photo with caption — vision query (caption is the prompt; no caption = "Describe this image")
- video with caption — vision query (caption is the prompt; no caption = "Describe this video")
- **reply to any message** — continues the conversation with full context

Prefixes can be combined: `/think /skills code-review /mcp github what's new?` or use skill shortcuts: `/think /reminder take out trash tomorrow`

#### Conversation threading

The bot supports conversation threading via Telegram replies. When you reply to a bot message (or your own), the entire reply chain is reconstructed and passed to the AI model as conversation history.

How it works:
1. You send "What is Python?" — the bot replies with an answer
2. You reply to the bot's answer with "And Java?" — the bot sees the full chain: your question, its answer, and your follow-up
3. You can continue replying to build multi-turn conversations (up to 20 messages deep)

The bot always sends its responses as replies to your message, so you can naturally continue any conversation by replying to it. Messages are stored in memory (up to 1000 per chat); if the bot restarts, it can still use the text from Telegram's `reply_to_message` as a one-message fallback.

Skill and MCP context is preserved across reply chains: if you start a conversation with a skill shortcut (e.g. `/eat 150g chicken`), subsequent replies in the same thread automatically re-activate the same skill and MCP servers without needing to repeat the prefix.

### MCP tools

Use external tools from MCP servers (requires `mcp.json`):

```bash
# Activate a disabled MCP server for this query
./ai-webfetch -enable-mcp github "List open issues in myrepo"

# Using /mcp prefix in query
./ai-webfetch "/mcp github List open issues in myrepo"

# Multiple servers
./ai-webfetch -enable-mcp github,filesystem "Find README in myrepo"

# News digest with MCP tools available to sub-agents
./ai-webfetch -news-summary -enable-mcp search
```

Servers with `"enabled": true` in `mcp.json` are always available without `-enable-mcp`.

Tool names are prefixed with the server name: `github__list_issues`, `filesystem__read_file`, etc.

### Skills

Skills are markdown instruction files that get injected into the system prompt, giving the model additional instructions or context.

A skill can be either a flat file (`name.md`) or a directory with `SKILL.md` inside (`name/SKILL.md`).

Skills support optional YAML frontmatter to auto-enable MCP servers:

```markdown
---
mcp: server1,server2
---
Skill instructions here...
```

When a skill with `mcp:` frontmatter is loaded, the listed MCP servers are automatically activated (equivalent to `/mcp server1,server2`). The frontmatter is stripped before injecting the skill text into the system prompt.

Search directories (first match wins):

**Global** (from `$HOME`):
- `~/.claude/skills/`
- `~/.agents/skills/`
- `~/.copilot/skills/`

**Local** (from working directory):
- `.github/skills/`
- `.claude/skills/`
- `.agents/skills/`

Use `-skills-dir path` to override and search only in a specific directory.

```bash
# Activate a skill via CLI flag
./ai-webfetch -skills code-review "make code review"

# Multiple skills
./ai-webfetch -skills code-review,haiku "review my code"

# Via /skills prefix in query
./ai-webfetch "/skills code-review make code review"

# Skill shortcut — auto-loads the skill by name
./ai-webfetch "/reminder take out trash tomorrow"

# Combined with other prefixes
./ai-webfetch "/nothink /skills haiku hello"
./ai-webfetch "/think /reminder buy groceries"
```

Skill shortcuts work for any `/name` that matches an existing skill file and is not a reserved command (`/news`, `/mail`, `/think`, `/nothink`, `/mcp`, `/skills`, `/start`, `/help`).

### Thinking mode

Some models (e.g. Qwen3, Qwen3.5) support a thinking/reasoning mode. By default the model decides; you can explicitly enable or disable it:

```bash
# Enable thinking via CLI flag (global)
./ai-webfetch -enable-thinking "solve this math problem"

# Enable thinking via /think prefix (per-query)
./ai-webfetch "/think what is 25*37?"

# Disable thinking via CLI flag (global)
./ai-webfetch -disable-thinking "query"

# Disable thinking via /nothink prefix (per-query)
./ai-webfetch "/nothink what is 2+2?"

# Combined with other prefixes
./ai-webfetch "/think /mcp github what's new?"
./ai-webfetch "/nothink /skills code-review review this"
```

For `-news-summary`, thinking is automatically disabled for Phase 1 (headline extraction) and Phase 2 (topic clustering) regardless of flags; Phase 3 (deep dive) respects the flag.

### Image (vision)

Attach an image to a query for vision-capable models:

```bash
./ai-webfetch -image photo.jpg "What's in this image?"
./ai-webfetch -disable-thinking -image screenshot.png "Describe what you see"
```

In Telegram bot mode, send a photo with an optional caption (the caption is used as the prompt). If no caption is provided, the bot uses a default prompt to describe the image.

### Video (vision)

Attach a video to a query for vision-capable models:

```bash
./ai-webfetch -video clip.mp4 "What happens in this video?"
./ai-webfetch -disable-thinking -video recording.mp4 "Describe what you see"
```

In Telegram bot mode, send a video or video note with an optional caption. If no caption is provided, the bot uses a default prompt to describe the video.

### Smart home

Control Home Assistant devices using natural language (requires `homeassistant.json`):

```bash
./ai-webfetch "Turn off all lights on the second floor"
./ai-webfetch "What's the temperature in the bedroom?"
./ai-webfetch "Open the blinds in the living room"
```

The assistant automatically discovers areas and entities via `ha_list`, reads states with `ha_state`, controls devices with `ha_call`, and captures camera snapshots via `ha_camera_snapshot` for visual analysis. Area and entity aliases configured in Home Assistant (including Voice assistant aliases) are used for matching.

Example Telegram conversation:

> **You:** What's going on in the living room?
>
> **Bot:** Here's what's happening in the living room right now:
>
> **People:**
> - One person at the desk working on a laptop
> - Another person sitting in an armchair with a white blanket
>
> **Conditions:**
> - Temperature: 22.0°C
> - Humidity: 24%
> - Lights: off
> - Blinds: open
>
> **Notes:**
> - Various items on the tables (books, boxes, fruit, bottles, plants)
> - Camera is actively recording
>
> The living room is currently being used as a workspace.

The assistant used `ha_list` to find entities in the area, `ha_camera_snapshot` to capture a frame from the camera, and `ha_state` to read sensor values — all automatically from a single natural-language question.

### Calendar

Query calendars and events (requires `calendar` in `users.json`):

```bash
./ai-webfetch -user alice "What's on my calendar this week?"
./ai-webfetch -user alice "Create a meeting tomorrow at 14:00-15:00 called Team Standup"
./ai-webfetch -user alice "Show my events for March 2026"
```

### Contacts

Search and manage contacts (requires `contacts` in `users.json`):

```bash
./ai-webfetch -user alice "Find John's phone number"
./ai-webfetch -user alice "What's the email for ACME Corp?"
```

### Interactive questions (ask_user)

When the AI needs clarification, it can ask questions with options. In CLI mode, numbered choices are printed to the terminal; in Telegram, an inline keyboard with buttons is sent.

```bash
# Interactive mode (default) — AI can ask questions
./ai-webfetch "Set up a cron job for backups"

# Disable for scripting/cron
./ai-webfetch -no-ask "Set up a cron job for backups"
```

The tool is automatically hidden in `-quiet`, `-telegram` (one-shot send), `-mail-summary`, and `-news-summary` modes. In Telegram bot mode, it is always available — questions appear as inline keyboard buttons, and the bot waits for the user to press one (no timeout).

### Persistent memory

The assistant can remember facts, contacts, topics, and events across conversations. Memory is stored as a JSON file (`memory.json`) in the specified directory.

Two types of records:
- **Entities** — persistent facts with an ID, e.g. `contact:user@example.com`, `topic:ai-regulation`, `pref:language`. Facts are merged on update.
- **Episodes** — timestamped events linked to entities, e.g. "discussed project deadline", "analyzed article about AI regulation".

```bash
# Enable via CLI flag
./ai-webfetch -memory ~/.ai-memory "What do you remember about Ivan?"

# Configured in users.json — no flag needed
./ai-webfetch -user alice "Remember that I prefer concise answers"

# Disable for a single invocation despite users.json config
./ai-webfetch -memory off -user alice "query without memory"
```

Priority: `-memory` flag > `memory` in `users.json` > disabled. In Telegram bot mode, memory path comes from user config.

### User settings (userinfo)

Persistent key-value settings that the AI can set and read. Settings can be automatically injected into the system prompt based on their flags:

- **`in_prompt=true`** — always included in the system prompt (e.g. timezone, preferred name)
- **`only_for="module"`** — included in the system prompt only when a specific skill, MCP server, or command is active (e.g. `only_for="eat"` for nutrition tracker username)
- **`in_prompt=false, only_for=""`** — stored but not injected; accessible via `userinfo_get`/`userinfo_list`

```bash
# Enable via CLI flag
./ai-webfetch -userinfo ./userinfo.json "Remember that my timezone is Europe/Prague"

# Configured in users.json — no flag needed
./ai-webfetch -user alice "Set my username for /eat to anton"

# Disable for a single invocation
./ai-webfetch -userinfo off -user alice "query without userinfo"
```

Example of stored settings (in userinfo JSON file):

```json
{
  "alice": {
    "timezone": {
      "value": "Europe/Prague",
      "in_prompt": true
    },
    "preferred_name": {
      "value": "Alice",
      "in_prompt": true
    },
    "eat_username": {
      "value": "anton",
      "only_for": "eat"
    },
    "github_style": {
      "value": "Use conventional commits",
      "only_for": "github"
    }
  }
}
```

When a query uses `/eat`, the system prompt automatically includes:
```
User info (eat):
  eat_username: anton
```

Global `in_prompt` settings are always included:
```
User info:
  timezone: Europe/Prague
  preferred_name: Alice
```

Priority: `-userinfo` flag > `userinfo` in `users.json` > disabled. In Telegram bot mode, the path comes from user config.

### Debugging sub-agents

With `-show-subagents`, each sub-agent's activity is shown indented with ` | `:

```bash
./ai-webfetch -show-subagents "Summarize emails from the last 12 hours"
```

Output looks roughly like:

```
[tool: imap_list_messages({"since_hours":12})]
[tool: imap_summarize_message({"uid":5247})]
 | --- sub-agent ---
 | System: Summarize the following email concisely...
 | Input: From: news@example.com...
 |
 | [thinking in dim color...]
 |
 | Email from Forbes Espresso about a Supreme Court ruling...
 |
 | --- end sub-agent ---
[tool: imap_summarize_message({"uid":5246})]
 | --- sub-agent ---
 | ...
 | --- end sub-agent ---
```

With nested sub-agents (agent calls agent), indentation increases:
` | ` → ` |  | ` → ` |  |  | ` etc.

### Debugging tool calls

With `-verbose-tools`, arguments and results of each call are shown:

```bash
./ai-webfetch -verbose-tools "Show the last 3 emails"
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

## Adding new tools

Create a file in `tools/`, register via `init()`:

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

For tools that need AI (sub-agent), use `SubAgentFn`:

```go
summary, err := SubAgentFn(systemPrompt, userMessage)
```
