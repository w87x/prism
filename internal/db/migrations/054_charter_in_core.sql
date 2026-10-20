-- The charter ("how we work here") becomes a core-memory block: one store for what every agent always knows, one place to edit it.
INSERT INTO core_blocks (label, text, description, position, updated_by)
SELECT 'charter', left(btrim(s.value->>'text'), 1600), 'how we work here: the house rules every agent follows', 0, 'migration'
FROM settings s WHERE s.key = 'charter' AND btrim(COALESCE(s.value->>'text','')) <> ''
ON CONFLICT (label) DO NOTHING;
