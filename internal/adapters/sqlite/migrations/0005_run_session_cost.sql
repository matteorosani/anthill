-- The running total of its session that the runner reported last during
-- a run, when it reports cost that way. The next run that resumes the
-- session records its cost as the difference from it (spec
-- ant-execution, "Cost reported for a whole session"). NULL is unknown.
ALTER TABLE runs ADD COLUMN session_cost_usd REAL;
