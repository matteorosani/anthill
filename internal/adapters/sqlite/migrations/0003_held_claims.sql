-- The claims this instance holds (spec work-claiming), so that renewal
-- and release survive a restart. assigned records whether Anthill made
-- the assignment, which only then is undone on release.
CREATE TABLE held_claims (
    ref_key    TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    ref        TEXT    NOT NULL,
    instance   TEXT    NOT NULL,
    comment_id TEXT    NOT NULL,
    label      TEXT    NOT NULL,
    assigned   INTEGER NOT NULL,
    parked     INTEGER NOT NULL,
    lease_ns   INTEGER NOT NULL,
    updated_at INTEGER,
    PRIMARY KEY (ref_key, kind)
);
