ALTER TABLE agent_profiles ADD COLUMN icon text NOT NULL DEFAULT '';
UPDATE agent_profiles SET icon = CASE name
  WHEN 'Atlas' THEN '界' WHEN 'Forge' THEN '鍛' WHEN 'Metis' THEN '進' WHEN 'Mnemosyne' THEN '憶' WHEN 'Sherpa' THEN '導' WHEN 'Oneiros' THEN '夢'
  WHEN 'Scout' THEN '探' WHEN 'Cipher' THEN '碼' WHEN 'Abacus' THEN '算' WHEN 'Quill' THEN '筆' WHEN 'Hearth' THEN '家' ELSE '' END
WHERE icon = '';
