-- The ant's base branch, resolved when its branch was made: the target
-- of its PR and the branch new commits are counted from (spec ant-tools,
-- "Finish tool"). Ants stored before have none.
ALTER TABLE ants ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';
