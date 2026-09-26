// Plain-language explanations shown behind the "?" buttons: one per page, plus a few concepts that are easy to mix up.
export const PAGE_HELP = {
  today: 'What needs you right now: tasks waiting on an answer or review, what agents are doing, what they produced today, commitments coming up and projects that lack a next step.',
  chat: 'Talk to Atlas, the coordinator. It answers directly or hands work to specialist agents. Each chat keeps its own history; use projects to give a chat focus and merge chats about one topic.',
  agents: 'The agents, their souls (standing instructions), tools and history. Metis proposes improvements here; nothing changes until you apply it unless auto-apply is on.',
  tasks: 'Every unit of work: what was asked, who did it, the transcript and the result. Tasks that stalled or failed land in "needs your attention" with options to continue, split or improve the agent.',
  memory: 'What PRISM knows about you and your projects. Facts are stored in banks; reflection, analysis and synthesis derive conclusions from them, each keeping its evidence.',
  kb: 'Documents and pages you added for agents to search. Unlike memory, these are looked up verbatim, not distilled into facts.',
  tools: 'Every tool agents can call, built-in and from MCP servers. "Armed" tools run without asking; the rest need your confirmation.',
  autonomy: 'Things that happen without you asking: schedules, intents and watches, briefings, soul evolution and an audit of it all.',
  library: 'Files and artifacts agents produced or fetched, plus code workspaces. Keep what matters; unkept artifacts expire.',
  trackers: 'Tables agents keep up to date for you (prices, reading list, job applications…). A tracker stores data with its change history; it never triggers anything by itself.',
  usage: 'Tokens and model calls per agent and model over time.',
  settings: 'Models, integrations, guardrails, memory pipeline, retention and notifications.',
};

export const AUTONOMY_HELP = {
  compare: [
    ['Schedule', 'By the clock', 'At set times (cron) an agent is woken with a standing prompt in a fresh session — "every Monday 9:00, summarise my inbox". It checks no condition.'],
    ['Intent', 'By a condition', '"Tell me when X happens." A cheap check runs on a cadence; when it holds, the owning agent is woken with the evidence and acts. One-off unless marked repeat.'],
    ['Watch', 'Progress only', 'The same kind of check, but for something in flight (a download, a build). It shows progress and notifies you when done; no agent is woken and no model is called. Expires on its own.'],
    ['Tracker', 'Data, not trigger', 'A table agents fill in and update (rows, columns, change history). Nothing fires from it; it is where information is kept.'],
  ],
};
