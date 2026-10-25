## PRISM 0.3.0

A release about memory that stays tidy, agents that learn and stop wasting work, and guards on everything that leaves the machine.

### Memory
- **Duplicate sweep.** Every pair of look-alike facts is judged once by the main model (same / changed value / distinct — a different name, place, number or date stays apart) and the weaker one is retired. Runs after analysis; *Find duplicates* in Memory lists the merges first; every merge is audited and can be undone.
- **"Not interesting".** A button on review rows, in the fact editor and in the memory-used list: the fact drops to the bottom of the ranking and expires on its own within a week.
- **Stale conclusions settle themselves.** A conclusion whose premise was retired is repaired (the premise swapped for its successor), trimmed or retired by plain rules — no waiting for a reflection that never looked at it.
- **Banks:** a project and a domain bank of one name are merged; the model's grouping reads both kinds in one list.
- **Credentials never enter memory** (facts or core memory); the charter is a core block.
- Memory analysis no longer fails when a model writes fact ids as `"#12"` or strings.

### Skills
- The best-fitting skill's procedure is put in front of a task (matched by embedding, not by shared words); each agent sees only the skills pinned to it.
- **Skills from recurring work:** once a day finished tasks are grouped by what they were for, and a job done three times on two days that no skill covers is handed to Daedalus. Skills that teach the same procedure are merged weekly (originals are switched off, not deleted).
- Tools → Skills shows how much each skill is used and who has it.

### Agents and onboarding
- **Mixed onboarding:** built-in agents for PRISM's own tools; for your MCP tools the model works in three small steps — sort the tools into domains, describe each group's agent, write its prompt. Planning is judged by whether the model is still writing, not by a clock, so a slow local model finishes. Onboarding is a full-page screen.
- Hearth is split (routines vs. waiting for an event: **Vigil**, trackers: **Ledger**); generated agents no longer get invented hard rules.
- **A promise to follow up gets a watch.** "I'll keep an eye on the download and let you know" in an answer to your own request is now reviewed; if nothing will do it, Sentinel starts the monitor.

### Guards (things that leave the machine, or run away)
- **Web search guard:** private details (addresses of your network, emails, keys) are cut out of a query; a small model answers in one word whether a search is worth making (SEARCH / REWRITE / PRIVATE / SKIP); near-repeats and runaway search runs are stopped.
- **Fetch guard:** a host that blocks (403/439/451) is left alone after three refusals — except a site the task itself names; a 429 is "wait", never a block.
- **No credentials in prompts to cloud models** (OpenAI-type providers, Codex), in Telegram messages, or in `consult`.
- **Delegation guard:** a near-identical request to the same colleague is pointed at the earlier task instead of starting another run.
- **Token budget per background task** (warn 700k, stop 1.2M tokens read; chats with you are never limited).
- **Watches:** a repeating watch fires when its condition becomes true, not at every check; checks run in parallel without holding up the schedules; failing checks back off; tool-polling watches only call read-only tools.

### Fixes
- The acceptance check judged reports against an empty tool trail; a reset kept old criteria (task_meta), ratings and lessons while task ids restarted — a data reset now wipes every table except configuration.
- Auto-recall and `memory_find` no longer put facts about another named thing first.
- MCP tools that only read (search, list, status) are read-only even when the server does not say so.
- Telegram: a topic deleted in Telegram is recreated; a topic called "None" is never made; topics can be removed from Settings.
- Cron jobs get the real agent name and refuse a day-of-month + weekday combination that runs on both.
- Model calls made on the side of the work wait up to 15 minutes instead of 45 s to 5 min (a slow local model made them fail quietly).
- Your name shows in the chat instead of "user:".

### Upgrading
Migrations 052–057 run on start. After updating, restart. If you reset your data earlier with an older build, clear the leftovers once: `DELETE FROM task_meta; DELETE FROM trust; DELETE FROM trails; DELETE FROM antibodies; DELETE FROM runbooks;`.
