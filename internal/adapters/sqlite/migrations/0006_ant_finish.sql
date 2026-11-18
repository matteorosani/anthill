-- The finish of an ant's active run that Anthill has not carried out
-- yet, as JSON, stored with the move to finishing and cleared with the
-- ant's next state (design D22). NULL when there is none.
ALTER TABLE ants ADD COLUMN finish TEXT;
