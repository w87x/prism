-- Distinguishes a dream/digest briefing from a genuinely important open question memory analysis raises (see
-- internal/memory/analyze.go's "question" insights and internal/app's AskUser wiring): same table, UI and reply
-- flow, but its own Telegram topic ("Questions" instead of "Briefings") and its own badge in the Briefings list.
ALTER TABLE briefings ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'briefing';
