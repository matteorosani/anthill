-- The follow-up budget a follow-up run started for changes got, which
-- counts it against the follow-up limit (spec change-reaction, "Follow-up
-- limits"). 0 for a run that kept the ant's budget.
ALTER TABLE runs ADD COLUMN new_budget_ns INTEGER NOT NULL DEFAULT 0;
-- Where the follow-up proposal for an ant's stored changes stands (spec
-- change-reaction, "Follow-ups start automatically by default"): '' for
-- none, 'pending', 'accepted' or 'declined'.
ALTER TABLE ants ADD COLUMN follow_up_proposal TEXT NOT NULL DEFAULT '';
