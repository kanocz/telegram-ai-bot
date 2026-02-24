# ai-webfetch

Telegram bot and CLI tool: AI assistant with web, email (IMAP), and Home Assistant access.

## Configuration

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

### imap.json — email access

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
  "chat_id": {
    "news": [-1234585163, 987654321],
    "mail": [-1234585163],
    "other": [-1234885163]
  },
  "bot": {
    "webhook_url": "https://example.com/hook/SECRET",
    "listen": ":8443",
    "allowed_users": [123456789]
  }
}
```

The `bot` section is optional (only required for `-telegram-bot`).

## Usage

```
./ai-webfetch [-no-think] [-quiet] [-show-subagents] [-verbose-tools] [-telegram] [-language lang] [-config path] [-enable-mcp name1,name2] [-mcp-config path] <query>
./ai-webfetch -mail-summary [-no-think] [-quiet] [-show-subagents] [-telegram] [-language lang] [-config path]
./ai-webfetch -news-summary [-news-urls path] [-no-think] [-quiet] [-telegram] [-language lang] [-config path]
./ai-webfetch -telegram-bot [-telegram-config path] [-config path] [-mcp-config path]
./ai-webfetch -export-default-prompts <dir>
```

### Flags

- `-no-think` — hide model thinking output (shown dimmed by default)
- `-show-subagents` — show sub-agent activity: input, thinking, and output (indented with ` | `)
- `-verbose-tools` — show tool call arguments and results (results truncated to 500 chars)
- `-mail-summary` — standalone mail digest: fetch unread, group by sender, categorize (no tool-loop)
- `-news-summary` — cross-referenced news digest: load URLs from file, sub-agents analyze each source (with `web_fetch` access), final summary grouped by events with Europe focus
- `-news-urls path` — path to file with news site URLs (default `news.urls`)
- `-quiet` — suppress all non-error output (for cron); implies `-no-think`
- `-telegram` — send output to Telegram instead of stdout (requires `telegram.json`)
- `-telegram-config path` — path to Telegram config (default `telegram.json`)
- `-telegram-chatid id` — override chat ID for a single invocation (all categories go to one chat)
- `-telegram-bot` — run as Telegram webhook bot service (requires `bot` section in `telegram.json`)
- `-config path` — path to config.json (default `config.json`)
- `-language lang` — response language (overrides config.json; default `русский`)
- `-enable-mcp name1,name2` — activate MCP servers for this query (comma-separated)
- `-mcp-config path` — path to MCP config file (default `mcp.json`)
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
./ai-webfetch -mail-summary -show-subagents
```

### News — cross-referenced digest

Loads URLs from `news.urls`, sub-agents analyze each source (with ability to open full articles via `web_fetch`), then produces a final summary grouped by events:

```bash
./ai-webfetch -news-summary
./ai-webfetch -news-summary -news-urls my-urls.txt
./ai-webfetch -news-summary -quiet -telegram    # cron mode
```

`news.urls` file format:

```
# Comments start with #
https://www.novinky.cz/
https://www.chinadaily.com.cn/world/europe
https://www.bbc.com/news
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

Results are sent to different chats by category:

```bash
./ai-webfetch -news-summary -telegram              # → chats.news[]
./ai-webfetch -mail-summary -telegram              # → chats.mail[]
./ai-webfetch -telegram "query"                    # → chats.other[]
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
- `/news` — news digest
- `/mail [hours]` — mail digest (default 24 hours)
- `/mcp server1,server2 <query>` — query with MCP tools activated
- `/mcp server /news` — news digest with MCP tools
- `/mcp server /mail [hours]` — mail digest with MCP tools
- any text — free-form query with tool-loop

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

### Smart home

Control Home Assistant devices using natural language (requires `homeassistant.json`):

```bash
./ai-webfetch "Turn off all lights on the second floor"
./ai-webfetch "What's the temperature in the bedroom?"
./ai-webfetch "Open the blinds in the living room"
```

The assistant automatically discovers areas and entities via `ha_list`, reads states with `ha_state`, and controls devices with `ha_call`. Area and entity aliases configured in Home Assistant (including Voice assistant aliases) are used for matching.

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
