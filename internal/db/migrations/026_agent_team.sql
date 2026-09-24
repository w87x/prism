-- An agent's team: the specialists it may delegate to and is accountable for (task → await → consolidate),
-- like Atlas does for the whole roster. Empty = no team (an agent with can_delegate but no team may still
-- delegate to anyone, as before).
ALTER TABLE agent_profiles ADD COLUMN team text[] NOT NULL DEFAULT '{}';
