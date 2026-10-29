-- The first schema of the local store (design D2). Times are Unix
-- nanoseconds in UTC, NULL when unset. Refs are kept twice: a text key
-- (provider:path#number) to query by, and the full ref as JSON. Nested
-- domain structs such as the budget and change events are JSON.

-- The session index: one row per ant. An item's current ant is its
-- latest row; earlier ants stay.
CREATE TABLE ants (
    id         INTEGER PRIMARY KEY,
    item_key   TEXT    NOT NULL,
    item       TEXT    NOT NULL,
    state      TEXT    NOT NULL,
    base_ns    INTEGER NOT NULL,
    budget     TEXT    NOT NULL,
    runner     TEXT    NOT NULL,
    session_id TEXT    NOT NULL,
    worktree   TEXT    NOT NULL,
    branch     TEXT    NOT NULL,
    pr         TEXT
);
CREATE INDEX ants_item ON ants (item_key, id);
CREATE INDEX ants_branch ON ants (branch, id);

-- Every state change of an ant, in order.
CREATE TABLE transitions (
    ant_id     INTEGER NOT NULL REFERENCES ants (id),
    seq        INTEGER NOT NULL,
    from_state TEXT    NOT NULL,
    to_state   TEXT    NOT NULL,
    at         INTEGER,
    reason     TEXT    NOT NULL,
    PRIMARY KEY (ant_id, seq)
);

-- Runs and their usage. NULL tokens or cost is unknown.
CREATE TABLE runs (
    id         INTEGER PRIMARY KEY,
    ant_id     INTEGER NOT NULL REFERENCES ants (id),
    kind       TEXT    NOT NULL,
    session_id TEXT    NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER,
    outcome    TEXT    NOT NULL,
    reason     TEXT    NOT NULL,
    tokens     INTEGER,
    cost_usd   REAL
);
CREATE INDEX runs_ant ON runs (ant_id);
CREATE INDEX runs_started ON runs (started_at);

-- Shepherd rounds run by this instance and their usage.
CREATE TABLE rounds (
    id         INTEGER PRIMARY KEY,
    pr_key     TEXT    NOT NULL,
    pr         TEXT    NOT NULL,
    head       TEXT    NOT NULL,
    requested  INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER,
    tokens     INTEGER,
    cost_usd   REAL
);
CREATE INDEX rounds_pr ON rounds (pr_key);
CREATE INDEX rounds_started ON rounds (started_at);

-- One row per provider connection.
CREATE TABLE poll_state (
    connection         TEXT PRIMARY KEY,
    cursor             TEXT NOT NULL,
    last_success       INTEGER,
    last_error         TEXT NOT NULL,
    last_error_at      INTEGER,
    rate_limited_until INTEGER
);

-- Content hashes per field group (design D7).
CREATE TABLE hashes (
    ref_key     TEXT NOT NULL,
    field_group TEXT NOT NULL,
    hash        TEXT NOT NULL,
    PRIMARY KEY (ref_key, field_group)
);

-- Change events still to handle (design D7). done_at is NULL while
-- pending.
CREATE TABLE outbox (
    id          INTEGER PRIMARY KEY,
    ref_key     TEXT NOT NULL,
    event       TEXT NOT NULL,
    detected_at INTEGER,
    done_at     INTEGER
);
CREATE INDEX outbox_pending ON outbox (id) WHERE done_at IS NULL;

-- Changes kept for an ant's next run. source is the outbox event they
-- came from; it is unique so that storing twice does nothing.
CREATE TABLE stored_changes (
    id          INTEGER PRIMARY KEY,
    ant_id      INTEGER NOT NULL REFERENCES ants (id),
    source      INTEGER UNIQUE,
    event       TEXT    NOT NULL,
    stored_at   INTEGER,
    consumed_at INTEGER
);
CREATE INDEX stored_changes_ant ON stored_changes (ant_id);

-- Questions an ant asked, by the provider ID of the question comment.
CREATE TABLE questions (
    comment_id  TEXT PRIMARY KEY,
    ant_id      INTEGER NOT NULL REFERENCES ants (id),
    on_ref      TEXT    NOT NULL,
    asked_at    INTEGER,
    answered_at INTEGER,
    answer_id   TEXT    NOT NULL,
    stale_at    INTEGER
);

-- Extension requests and their decisions.
CREATE TABLE extension_requests (
    id         INTEGER PRIMARY KEY,
    ant_id     INTEGER NOT NULL REFERENCES ants (id),
    minutes    INTEGER NOT NULL,
    reason     TEXT    NOT NULL,
    at         INTEGER,
    status     TEXT    NOT NULL,
    granted    INTEGER NOT NULL,
    message    TEXT    NOT NULL,
    decided_at INTEGER
);
CREATE INDEX extension_requests_ant ON extension_requests (ant_id);

-- Proposals the queen declined (until NULL) or snoozed on this machine.
CREATE TABLE hidden_proposals (
    item_key  TEXT PRIMARY KEY,
    item      TEXT NOT NULL,
    hidden_at INTEGER,
    until     INTEGER,
    item_hash TEXT NOT NULL
);
