## PRISM 0.2.0

The release is about **autonomy with brakes**: the team now works more like a real team, with procedures, reviews and a rhythm, and you decide how far it may change itself.

### Team practices
- **Acceptance criteria** — a lead who hands out work says what "done" means (`done_when`); the report is checked against it before it reaches the lead.
- **Runbooks** — a routine job runs on the main model until it has succeeded twice; then a runbook is written and the small model follows it, going back to the main model when it fails.
- **Four-eyes reviewer** — a second model looks at tool calls that would ask you first; trust earned per agent and tool lets good records through. Sends, deletes, payments and shell never go through it.
- **Incident mode** — when no model is reachable, background work pauses and resumes by itself.
- **Decision log**, **sampled audits** of small-model runs, a **weekly standup** with 👍/👎 ratings, handover notes and a team charter.

### Nature and relations
- **Pain reflex** — a tool that really breaks (timeouts, refused connections, 5xx) is paused for everyone; a bad argument or a dead link never trips it. The status bar shows what is paused.
- **Ant trails** (fading traces of what worked shade routing), **immune lessons** (short, triggered, expiring), **scouting** (read-only recon before risky jobs), apology-and-repair after a thumbs-down, and a person model kept as core memory.

### The autonomy dial
- **Safe / guarded / armed**, separately for *evolution* (changes to agents' souls, tools and traits) and for *scheduled jobs*. Safe is the default.
- Automatic changes are **watched**: if an agent's results clearly drop, the previous version is restored — unless it was edited again since, which is never overwritten.
- **Weekly job review**: near-duplicate jobs are merged, failing jobs are rewritten from their run evidence, lessons and memory — or a group of "check X / Y / Z" jobs becomes **one tracker with a row per item** and a single refresh job. Every change keeps the old version and can be undone. A merge, rewrite or tracker that loses a threshold from the original jobs is discarded.
- New tools: `cron_update`, `cron_finish`.

### Interface
- Compact chat list (a small LED per chat, details on hover, a long press on touch).
- Housekeeping (stalled tasks, staff reports, tool pauses, rollbacks, model down/up) goes to the notification bell instead of the main chat; the activity switch in chat now also filters those messages in existing chats.
- Answered questions no longer linger in Today and briefings.
- Dashed "acting" lines in the 3D agent graph; one shared set of link, chip and button styles.
- Mineral backgrounds are rendered once to a bitmap, which fixes a hang in desktop Safari.

### Fixes
- Opening a new window left the chat empty when the paused-tool refresh failed to load.
- CI no longer fails on a browser-test cleanup race.
- Untouched built-in agents now follow upgraded souls; edited ones are left alone.

### Notes
- Migrations 047–051 run on first start. Nothing to do.
- Needs a model that can follow JSON replies. The job-review prompts were exercised against Qwen 3.6 35B-A3B and Qwen 3.5 9B.
- Not verified on every browser: the Safari background fix and the touch chat list were checked by build only.
