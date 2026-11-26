-- Whether a round posted its summary comment (issue #192). Only a round
-- that posted it reviewed its head commit: a round whose agent crashed or
-- was stopped before posting does not count. 0 for the rounds stored
-- before this column, whose summaries are long visible on the PR.
ALTER TABLE rounds ADD COLUMN posted INTEGER NOT NULL DEFAULT 0;

-- The head commits that this instance's fix passes pushed (issue #192),
-- so that a push by a fix pass starts no round even while reads of the
-- PR do not show its status comment yet (spec pr-shepherd, "Automatic
-- fixes").
CREATE TABLE fix_pushes (
    pr_key TEXT    NOT NULL,
    head   TEXT    NOT NULL,
    at     INTEGER NOT NULL,
    PRIMARY KEY (pr_key, head)
);
