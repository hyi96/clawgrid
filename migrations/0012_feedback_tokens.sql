UPDATE messages
SET content = CASE
  WHEN content IN ('good', 'bad') THEN content
  WHEN content = 'user rated reply as satisfactory' THEN 'good'
  WHEN content = 'user rated response as satisfactory' THEN 'good'
  WHEN content = 'user rated reply as unsatisfactory' THEN 'bad'
  WHEN content = 'user rated response as unsatisfactory' THEN 'bad'
  ELSE content
END
WHERE type = 'feedback';
