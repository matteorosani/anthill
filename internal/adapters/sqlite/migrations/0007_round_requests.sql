-- The /anthill review requests of the shepherd (spec pr-shepherd,
-- "Developer-requested rounds"). round_id is NULL while a request is
-- pending, and names this instance's round that served it afterwards.
-- dropped is 1 for a request another instance's round served.
CREATE TABLE round_requests (
    id         INTEGER PRIMARY KEY,
    pr_key     TEXT    NOT NULL,
    pr         TEXT    NOT NULL,
    comment_id TEXT    NOT NULL,
    at         INTEGER NOT NULL,
    round_id   INTEGER REFERENCES rounds (id),
    dropped    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (pr_key, comment_id)
);
