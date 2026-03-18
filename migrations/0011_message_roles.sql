ALTER TABLE messages
ADD COLUMN IF NOT EXISTS role TEXT;

UPDATE messages
SET role = CASE
  WHEN type = 'reply' THEN 'responder'
  ELSE 'prompter'
END
WHERE role IS NULL;

UPDATE messages
SET type = 'text'
WHERE type = 'reply';

ALTER TABLE messages
ALTER COLUMN role SET NOT NULL;

ALTER TABLE messages
ADD CONSTRAINT messages_role_check CHECK (role IN ('prompter', 'responder'));
