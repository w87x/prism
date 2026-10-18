## PRISM 0.2.1

### ChatGPT subscription as a model provider
A new provider kind, **`codex`**, runs models on your ChatGPT subscription through the Codex CLI — set up like LM Studio, no API key.

- **Setup:** Settings → Models → + Provider → `codex` (or the "ChatGPT (Codex)" preset in first-run setup). The models your plan offers are added automatically.
- **Sign-in:** from Settings (a browser window, or a code to enter on any device) or with `codex login`. The sign-in runs through Codex itself, so PRISM never sees or stores a token.
- **How it talks to the model:** through Codex's own app-server — the supported interface Codex uses for its apps — not a private endpoint.
- **Native tool calls:** PRISM's tools are handed to Codex as real functions, so the model makes proper function calls (several at once when it wants). A Codex that refuses client tools falls back to carrying tool calls in a structured answer.
- **Lean requests:** Codex's own context (skills, plugin and app lists, extra tools) is switched off for these calls: a one-word question carries about 4.5k input tokens instead of about 12.4k. Reasoning effort is set per call (low for judging, medium otherwise) instead of inheriting the one from your own Codex setup.
- **Safe on the plan:** at most three turns run in parallel; a used-up plan looks like a rate limit, so PRISM falls back to the next model in the list.
- **Checked live** with PRISM's Model check on `gpt-6-luna`, `gpt-5.6-terra` and `gpt-5.6-luna` (reply, JSON, tool call, silence).

Needs the Codex CLI installed (`brew install codex`) and signed in with ChatGPT. How the plan's usage limits count these calls is not known yet — watch your usage after a day of real use.

### Artifacts
- Files agents save are **temporary by default** (7 days). An agent marks a deliverable the user asked for with `keep=true`; anything useful can also be kept from Library.
- Saving the same file again reuses the existing one instead of adding a copy.
- Browser screenshots expire after 3 days (downloaded files stay).

### Smaller
- Models imported through discovery keep their vision flag.
- The consult feature shares one Codex binary lookup with the new provider.
