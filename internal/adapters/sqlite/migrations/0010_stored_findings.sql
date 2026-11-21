-- The shepherd findings a stored change hands to the ant (spec
-- change-reaction, "Shepherd findings on ant PRs"), as JSON, and the key
-- of a change that does not come from the outbox, such as the findings
-- of one round, so that it is stored once. Outbox changes have no key.
ALTER TABLE stored_changes ADD COLUMN findings TEXT NOT NULL DEFAULT '[]';
ALTER TABLE stored_changes ADD COLUMN key TEXT;
CREATE UNIQUE INDEX stored_changes_key ON stored_changes (key);
