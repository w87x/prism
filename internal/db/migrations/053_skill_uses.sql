-- Which skills actually get used: one row each time a saved skill was put in front of an agent for a task (how = injected) or loaded by it (loaded).
CREATE TABLE IF NOT EXISTS skill_uses (
	id      bigserial PRIMARY KEY,
	skill   text NOT NULL,
	agent   text NOT NULL DEFAULT '',
	task_id bigint NOT NULL DEFAULT 0,
	how     text NOT NULL,
	at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS skill_uses_skill ON skill_uses (skill, at DESC);
