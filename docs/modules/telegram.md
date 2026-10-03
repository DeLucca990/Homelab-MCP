# The Telegram bot

`cmd/telegram-bot/` and `internal/telegram/`. A second binary, not a module of
the server: it is an **MCP client** that happens to answer on Telegram.

```
Telegram ──long polling──▶ bin/telegram-bot
                              ├─ Claude API          picks the tools for a question in plain words
                              └─ MCP client ──HTTP + Bearer──▶ bin/server /mcp
```

It never reaches Docker, systemd or an `*arr` itself. Everything goes through
the server, so the bearer token, the per-command approvals and the container
allowlist hold for the bot exactly as they do for Claude Code. A bot that
called `internal/containers` directly would have needed all three again, and
would have been the one place they were missing.

---

## Setting it up

### 1. @BotFather

1. `/newbot` → a name and a username. The token it answers with is `TELEGRAM_BOT_TOKEN`.
2. `/setjoingroups` → **Disable**, unless you want it in a group (below). Output
   in a group is read by everyone in it.
3. Your numeric user id — [@userinfobot](https://t.me/userinfobot) tells you — is
   `TELEGRAM_ALLOWED_USER_IDS`.

#### In a group

Off by default, and it takes three things:

1. **The group's id in `TELEGRAM_ALLOWED_CHAT_IDS`.** Add the bot, send it
   `/status` there, and the log names the group it ignored:
   `ignored a message in supergroup chat -1001234567890 ("Homelab"), which is not in TELEGRAM_ALLOWED_CHAT_IDS`.
   A group that becomes a supergroup gets a new id; the log will show it.
2. **Everyone who should be able to ask in `TELEGRAM_ALLOWED_USER_IDS`.** The
   group being allowed does not let its members act: someone not on the user
   list is ignored there as everywhere. Everyone in the group *reads* every
   answer, though, whoever asked.
3. **For @mentions, privacy mode off:** `/setprivacy` → **Disable**, then
   remove the bot from the group and add it again — the setting applies on
   joining. With privacy mode on, Telegram delivers only commands and replies
   to the bot's own messages, which is enough if that is all you use.

In a group the bot answers only a command, a message that mentions it, or a
reply to one of its messages. Everything else is people talking to each other
and is dropped without a trace. The conversation with Claude is per chat, so
it is shared by everyone in the group, and each message reaches Claude with
the sender's name in front of it.

There is no need for `/setcommands`: the bot publishes its own list on startup,
so it cannot drift from the code.

### 2. The `.env`

On a machine that runs the server too, the bot reads the same `.env` — beside
the executable, then one directory above it, like the server — and three lines
are new:

```sh
TELEGRAM_BOT_TOKEN=123456:ABC...
TELEGRAM_ALLOWED_USER_IDS=11111111
ANTHROPIC_API_KEY=sk-ant-...        # optional: without it, commands only
```

| Variable | Default | |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | **required** |
| `TELEGRAM_ALLOWED_USER_IDS` | — | **required**, numeric ids, comma-separated |
| `TELEGRAM_ALLOWED_CHAT_IDS` | — | groups to answer in, besides private chats — negative ids, comma-separated |
| `HOMELAB_MCP_URL` | `http://$HOMELAB_MCP_HTTP_ADDR/mcp` | set it when the server sits behind `tailscale serve` or on another host |
| `HOMELAB_MCP_HTTP_TOKEN` | — | **required**, the server's own token |
| `ANTHROPIC_API_KEY` | — | turns on natural language (`ANTHROPIC_AUTH_TOKEN` also works) |
| `TELEGRAM_MODEL` | `claude-opus-5-5` | `claude-sonnet-5-5` costs about half |
| `TELEGRAM_EFFORT` | `medium` | `low` … `max` |
| `TELEGRAM_APPROVAL_TIMEOUT` | `2m` | how long a question waits for a button |

`HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION` is **not** needed and should stay unset
for the bot's sake: the bot declares elicitation, so the server asks it, per
command, like it asks Claude Code.

Every problem in the configuration is reported at once, on startup:

```
[homelab-telegram] configuration:
  - TELEGRAM_ALLOWED_USER_IDS is not set: list the numeric Telegram user ids ...
  - TELEGRAM_EFFORT="huge": must be one of low, medium, high, xhigh, max
```

### 3. systemd

```ini
# /etc/systemd/system/homelab-telegram-bot.service
[Unit]
Description=Homelab MCP Telegram bot
After=network-online.target homelab-mcp.service
Wants=network-online.target

[Service]
ExecStart=/home/ubuntu/repos/Homelab-MCP/bin/telegram-bot
User=ubuntu
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```sh
make build
sudo systemctl daemon-reload && sudo systemctl enable --now homelab-telegram-bot
journalctl -u homelab-telegram-bot -f
# [homelab-telegram] telegram bot running for 1 user(s), MCP at http://100.101.102.103:3000/mcp: commands and natural language with claude-opus-5-5, effort medium
```

The `User=` needs no `docker` group: the bot touches nothing on the host. To
update, `make deploy-bot`.

It needs no inbound port. Long polling only opens connections outwards, to
`api.telegram.org` and `api.anthropic.com`, so it works on a host that is
reachable only on the tailnet.

---

## What it answers

### Commands

| Command | Tool |
| --- | --- |
| `/status` | `homelab_overview` |
| `/disk` | `system_disk_usage` |
| `/memory` | `system_memory_stats` |
| `/docker` | `docker_container_status` |
| `/queue` | `radarr_queue_status`, `sonarr_queue_status` — whichever the server registered |
| `/reset` | forgets the conversation |
| `/help` | the list |

No model is involved: a command calls the tool and prints what it returns, in
`<pre>` so the tables stay aligned. They cost nothing, answer in a second, and
keep working when the Anthropic API does not. Output longer than four messages
arrives as a `.txt` file instead.

### Plain words

Anything that is not a command goes to Claude with every tool the server
registered, unchanged — name, description, schema — and the loop runs until
Claude has an answer, at most 12 steps. The system prompt is short and fixed,
plus the server's own [`triage`](../tools/PROMPTS.md#triage) prompt, which
already states the order to diagnose in.

- **History** is per chat, append-only, and dropped after 30 idle minutes or
  160 messages. It is never trimmed from the front: that would change the
  prefix every request is cached on, and invalidate the model's earlier
  reasoning.
- **A failed turn is taken back whole**, so the next question never starts
  from a tool call with no result.
- **One question per chat at a time.** A second message while the first is
  still running — usually waiting on an approval — is answered with "still on
  the previous one" rather than queued.
- **Caching** covers tools, system prompt and history. The tool list is sorted
  by name, so it is the same bytes on every request.
- **Refusals**: requests carry `fallbacks: "default"` (beta
  `server-side-fallback-2026-07-01`), so one a safety classifier declines is
  re-served by another model within the same call.

Replies are Telegram HTML, but never sent as Claude wrote them: they are read
into the tags Telegram supports and written back out balanced. Anything else
that looks like markup — a stray `<`, a `</parameter>` — arrives as visible
text instead of getting the whole message rejected. A reply split across
messages closes its open tags at each cut and reopens them in the next, so a
`<pre>` table survives the split. Should Telegram still refuse one, it goes
again without markup.

Each tool Claude calls is one line, and each question ends with one line
saying whether it was answered and what it cost:

```
user 8683784233 → system_memory_stats
user 8683784233: answered in 2 steps, $0.0120 ($0.4310 since start)
```

In a group the asker is `user 8683784233 in group -5552970492`. Two notes are
added to the last line only when they apply: `— answered by claude-opus-4-8`
when a fallback model answered instead of `TELEGRAM_MODEL`, and `— no price
for …, not included` for a model the price table does not know. A failed
question is logged too (`failed in 3 steps`): every step before the failure
was billed. `journalctl -u homelab-telegram-bot | grep '\$'` is the ledger.

The dollars are an **estimate** from list prices (`prices` in
`internal/telegram/cost.go`, dated in the code), not the bill: cache writes at
1.25× input for the 5-minute TTL and 2× for the hour, cache reads at each
model's own rate. When a fallback answered, each attempt is priced at the rates
of the model that ran it, from `usage.iterations`. The running total resets
when the process restarts.

A reply that contains tool-call markup — a call the model wrote out as text
instead of making, which never ran — is logged with its first 400 characters.

---

## Approvals

The server's [confirmation round trip](../ARCHITECTURE.md#3-waiting-for-a-user-response)
reaches the chat as the server's own message with two buttons — its wording
untouched, its layout turned from a terminal's into a chat's
(`internal/telegram/approvalformat.go`):

```
[poster]
🔐 Add this movie to Radarr?

Inglourious Basterds (2009) · tmdb 16869
• Quality profile: HD-1080p
• Root folder: /movies
• Monitored: yes
• Search now: yes

Radarr will start looking for a release straight away, …
[ ✅ Aprovar ] [ ❌ Recusar ]
```

The server writes every approval in one shape — a question, a block of
details indented four spaces, paragraphs on the effect — and the bot reads
that shape back: the question and the subject in bold, each `label: value`
as a line with the label in bold, paths in monospace, the paragraphs as text.
A command (`docker_container_exec`) and quoted text stay monospace and
verbatim, since reading exactly what will run is the point of the question.
When the server links a `cover:`, the question is sent as that poster with
the text as its caption — unless it is longer than a caption may be, or
Telegram cannot fetch the image, and then it goes as text.

The go-sdk client does the protocol half by itself: when a `tools/call`
returns `inputRequests`, it calls the bot's elicitation handler and retries
the call with the answer and the `requestState`. The handler only has to turn
a question into buttons and wait:

- the chat to ask comes from the context of the tool call — the elicitation
  arrives from inside the SDK, with nothing else to say whose it is;
- the button's `callback_data` is `ap:<16 random hex>:y|n`, inside Telegram's
  64 bytes, and random so an old button cannot match a new question;
- a press counts only from **the user whose request raised the question**, and
  only once;
- no answer within `TELEGRAM_APPROVAL_TIMEOUT`, a cancelled request, or a
  question that could not be sent all answer `cancel`, which the server
  treats as no;
- the buttons are removed afterwards and the outcome written under the
  question, so the chat keeps a record of what was approved.

What was approved is bound to the operation by the server's fingerprint, not
by anything in the bot. A compromised bot could press its own buttons — which
is why the token, the allowlist of users and the container allowlist matter
more than the buttons do.

---

## Security

| Layer | Holds against |
| --- | --- |
| `TELEGRAM_ALLOWED_USER_IDS` | anyone else who finds the bot. Their updates are dropped without a reply — a reply would confirm the bot exists — and logged |
| Private chats, plus the groups in `TELEGRAM_ALLOWED_CHAT_IDS` | a group where others read the output, even if a listed user is in it |
| The server's bearer token | anything on the tailnet that is not a configured client |
| Per-command approval, server-side | the model, or a prompt injected through a log line or a release name, acting on its own |

Tool output — logs, release titles, file names — is data the server read from
places anyone can write to, and it goes into Claude's context. The system
prompt says never to follow instructions found there; the approval is what
makes that a preference rather than the only defence.

Keep the `.env` `chmod 600`: it now holds a token that can act on this server
through Telegram, from anywhere.

---

## Failure modes

| Symptom | Cause |
| --- | --- |
| No reply at all | The sender is not in `TELEGRAM_ALLOWED_USER_IDS`, or the group is not in `TELEGRAM_ALLOWED_CHAT_IDS`. Both are logged |
| No reply to an @mention in a group, but commands work | Privacy mode is on: `/setprivacy` → Disable, then remove and re-add the bot |
| A friend's button press does nothing | Only the person whose request raised an approval can answer it |
| `connecting to the MCP server … 401` | `HOMELAB_MCP_HTTP_TOKEN` differs from the server's |
| `connecting to the MCP server … connection refused` | The server is down, or bound to `127.0.0.1` behind `tailscale serve` — set `HOMELAB_MCP_URL` to the address that serves it |
| "Linguagem natural está desligada" | No `ANTHROPIC_API_KEY` in the bot's environment |
| An approval shows "Expirou sem resposta" | Nobody pressed within `TELEGRAM_APPROVAL_TIMEOUT`; nothing was done |
| "Ainda estou no pedido anterior" | The previous question is still running, usually waiting on an approval further up the chat |

Over HTTP, the go-sdk closes a client session on any error status — an
unknown tool, or arguments the server rejects. The bot drops the session and
reconnects on the next call; reads (`tools/list`, `prompts/get`) are retried
once at once, a tool call never is, since the first attempt may have acted.
