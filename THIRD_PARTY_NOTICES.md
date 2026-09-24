# Third-party notices

PRISM itself is MIT-licensed (see `LICENSE`). It depends on the open-source components below, which keep their own
licenses. Nothing is vendored into this repository; Go modules and npm packages are fetched at build time, and the
built binary embeds the web UI together with the fonts listed here.

## Go modules linked into the binary

| Module | License |
|---|---|
| github.com/jackc/pgx/v5, pgpassfile, pgservicefile, puddle/v2 | MIT |
| github.com/coder/websocket | ISC |
| github.com/chromedp/chromedp, cdproto, sysutil | MIT |
| github.com/emersion/go-imap/v2, go-message, go-sasl | MIT |
| github.com/gobwas/ws, httphead, pool | MIT |
| github.com/go-json-experiment/json | BSD-3-Clause |
| golang.org/x/net, x/sync, x/text | BSD-3-Clause |

## Web UI (build-time and embedded)

| Package | License | Notes |
|---|---|---|
| svelte, vite, @sveltejs/vite-plugin-svelte | MIT | build tooling / compiled into the UI |
| @fontsource/jetbrains-mono | OFL-1.1 | JetBrains Mono, © The JetBrains Mono Project Authors |
| @fortawesome/fontawesome-free | CC-BY-4.0 (icons), OFL-1.1 (fonts), MIT (code) | © Fonticons, Inc.; https://fontawesome.com/license/free |

## Runtime tools PRISM can call (not bundled)

`aria2`, Google Chrome, PostgreSQL / pgvector, and the `himalaya` CLI are installed separately by the user under
their own licenses. Skills and MCP servers imported at runtime keep the licenses of their authors.

Full license texts are available in each dependency's repository or in `~/go/pkg/mod` and `web/node_modules` after a
build.
