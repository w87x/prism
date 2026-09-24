# PRISM

A personal, single-user, **multi-agent AI assistant**. You talk to one agent — **Atlas** — who either answers
immediately or decomposes the task, delegates the pieces to specialists (max two levels deep) and synthesizes
the result. Small, focused contexts instead of one bloated one.

* **Backend:** Go (stdlib-first; `pgx`, `coder/websocket`, `chromedp`, `x/net/html` are the only runtime deps)
* **Frontend:** Svelte 5 + Vite, talking to the backend over one WebSocket (RPC + pushed events)
* **Storage:** PostgreSQL (pgvector used automatically when the server has it)
* **Models:** any OpenAI-compatible endpoint — LM Studio, OpenAI, Gemini, Grok, OpenRouter, custom —
  with several keys per provider and fallback lists

## Requirements

* macOS (Calendar, Reminders, Shortcuts and notifications use native APIs)
* Go (version in `go.mod`) and Node.js to build
* PostgreSQL 14+ (pgvector optional but recommended) and an OpenAI-compatible model endpoint
* Optional: `aria2` (torrent/FTP downloads), Google Chrome (browser tools) — `make deps` installs them

## Run it

```bash
make ui build       # builds web/dist, then bin/prism (the UI is embedded in the binary)
./bin/prism         # → http://127.0.0.1:7777
```

First start opens the **onboarding** in the browser: PostgreSQL connection (the `prism` database is created for
you), models (with discovery of what your provider offers), a few words about you, and generation of your
specialist agents. Everything afterwards is configurable from the web UI.

Bootstrap config lives in `~/.prism/config.json` (`PRISM_HOME` overrides): listen address, DSN, data dir.
Listening beyond loopback requires a token (generated automatically) because PRISM can run commands.

`make doctor` checks what is installed and what the optional tools unlock (aria2c for magnet/torrent/FTP downloads,
Chrome for the browser tools); `make deps` installs them all through the `Brewfile`.

To reach PRISM from another device, set `"listen": "0.0.0.0:7777"` (a token is generated and shown once in the log; open
the UI with `?token=…`). Any IP address works; for a hostname (`mac.local`, a Tailscale name) add it to
`"allowed_hosts"` in `config.json`.

Only one browser tab edits at a time (PRISM is a single-user app): a second tab is read-only until you click *Use this tab*.

Development: `make dev` (backend on :7777 and the UI on :5173, proxying `/ws`).
Tests: `PRISM_TEST_DSN=postgres://user@host:port/postgres go test ./...` (creates and drops throw-away databases).

## What agents can reach

* **Images** in chat (paste, drop, attach; downscaled in the browser) and photos sent to the Telegram bot.
* **Voice, sound effects and images** through ElevenLabs (key in Settings; a monthly credit cap protects a small plan).
* **Mail** on several tagged accounts (`work`, `personal`…), through PRISM's own IMAP/SMTP client or the `himalaya`
  CLI. Reading is always treated as untrusted; drafts are saved, and **sending always asks you**.
* **Calendar & Reminders** (read and write, repeating events expanded) and **Apple Shortcuts** (run yours; create new
  ones experimentally). Deleting always asks.
* **Documents**: drop PDFs (scans are read with OCR), Word, EPUB, HTML, text or pictures into Library → Documents;
  they are indexed and searchable by meaning. Anything dropped in is treated as outside material.
* **Runtime plugins**: an agent can write a small Python tool; it waits for your review (Tools → Plugins) and then
  runs in a macOS sandbox without network or access to your secrets.
* **MCP servers** with OAuth sign-in and a starter gallery (Tools → MCP servers).
* **Usage dashboard**: tokens, latency, queue wait, failures and tool health for the last 24 h / 7 / 30 days.
* Agents can **ask a colleague** for one thing they cannot do (no recursion), and hire specialists **on probation**
  (no exec tools until you confirm).
* **Attachments in chat**: upload files, or browse this Mac and attach a folder or files (read in place). Attached
  folders get a **folder map** — a one-line summary of every file and subfolder, written in the background by the fast
  model — so agents (and their sub-agents) find files with `folder_map` instead of opening everything.
* **Hand-offs between agents**: `artifact_save` with a lifetime makes a temporary artifact ("see artifact #100"); the
  reader opens it with `artifact_read`. It deletes itself, and carries the sender's untrusted-content taint.
* **Memory** with links between facts, conclusions drawn from several facts (with their evidence), bank merge/split
  with undo, JSON export/import, a graph view, and source-corroborated trust for facts learned from the web. Only
  Mnemosyne may delete, link, reflect, merge or split; every other agent stores and searches.

## How it fits together

| Concept | Where |
|---|---|
| Atlas, well-known staff (Forge hires, Metis evolves, Mnemosyne curates memory, Sherpa picks tools, Oneiros dreams) | `internal/agent/seed.go` |
| Agent runner: iteration budget **and** loop detection, steering with synthetic skipped results, concurrent-safe tool fan-out, taint tracking, tiered compaction (80 % → 20 %) | `internal/agent/runner.go`, `compact.go` |
| Delegation (depth ≤ 2), multi-turn tasks, queue with from/to/status, restart-safe dispatcher | `internal/agent/orchestrator.go`, `internal/tasks` |
| Memory banks (user / profile / project / domain / raw), ranked + decaying facts, supersession history, raw → facts pipeline | `internal/memory` |
| Tool registry: on/off, **SAFE / ARMED**, risk classes, deferred schemas + `tool_search` | `internal/tools` |
| Skills: progressive disclosure, hub import from GitHub, LLM **adaptation** (strips other platforms' branding) | `internal/skills` |
| Web: search (Yandex, AnySearch, Tavily, DuckDuckGo), fetch (HTTP → FlareSolverr → browser), LLM extraction (HTML → JSON) | `internal/web` |
| Browser automation (chromedp, persistent profile or attach to a running Chrome) | `internal/browser` |
| MCP client (stdio + streamable HTTP), tools appear deferred | `internal/mcp` |
| Autonomy: cron, standing intents, watches (files, processes, downloads, pages, feeds), dreams → briefings, soul-evolution proposals | `internal/scheduler`, `internal/cron` |
| Telegram (DM + forum topics with context), macOS (notifications, Reminders, …), Obsidian (iCloud vaults), semantic search | `internal/integrations/*`, `internal/docsearch` |

### Safety model

* Tools have a **risk class** (read / write / exec). Non-read tools ask for confirmation unless **armed**.
* Content from the web, MCP servers and other untrusted sources **taints** the turn (recorded as provenance on
  messages): from then on even armed non-trivial tools ask first. Memory learned from tainted turns is flagged
  *unverified*.
* File writes are limited to the PRISM data dir and configured roots; `~/.ssh`, keychains etc. are never readable.
* Web fetching, feeds, downloads and the browser refuse private/loopback/link-local/CGNAT addresses (SSRF) unless
  explicitly allowed; the check runs on the address actually dialed, so DNS rebinding does not get around it.
* Reading files outside the PRISM data dir asks first while untrusted content is in the turn, and "allow for this task"
  never carries over once a turn is tainted. Secrets (`~/.ssh`, keychains, browser profiles…) are unreadable however the
  path is spelled (case variants, symlinks).
* The WebSocket checks Host (DNS rebinding) and Origin (cross-site hijacking).
* Souls only change through reviewable **proposals** (or auto-apply if you switch it on); every version is kept.


## Support

If PRISM is useful to you, you can buy me a coffee:

| Coin | Address |
|---|---|
| ETH | `0x944bd383a65a5Bf8DB25FA8862b3ACC67Ff0EEBC` |
| USDT (ERC-20) | `0x944bd383a65a5Bf8DB25FA8862b3ACC67Ff0EEBC` |
| BTC | `bc1ql8dktesmjhs9vsxgd8cnrfd9hhxlpvpdgg57x8` |
| XRP | `rgYSWf3G5ssFDxWYJi7HWD1N9TRNbG5SA` |
| LTC | `LLdJ3C3ziEGniuyeThFmJzHm3YRefmWrTd` |

## License

PRISM is released under the [MIT License](LICENSE). Third-party components (Go modules, Svelte/Vite, the embedded
JetBrains Mono and Font Awesome Free fonts) keep their own licenses; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
