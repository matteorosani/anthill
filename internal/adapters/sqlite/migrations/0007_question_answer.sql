-- The question's text, for answers that quote it, the thread it opened on
-- a threaded provider, and the answer found to it as a JSON change event,
-- which the run resuming the ant's session gets as input (spec
-- agent-conversation, "Answers resume the session"). answer is NULL until
-- the question is answered.
ALTER TABLE questions ADD COLUMN text TEXT NOT NULL DEFAULT '';
ALTER TABLE questions ADD COLUMN thread TEXT NOT NULL DEFAULT '';
ALTER TABLE questions ADD COLUMN answer TEXT;
