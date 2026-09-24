-- Which soul version a proposal was written against, so the review can diff against exactly that text
-- (and warn when the agent has changed since).
ALTER TABLE evolution_proposals ADD COLUMN base_version int NOT NULL DEFAULT 0;
