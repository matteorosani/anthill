-- The text of the field groups whose old value a change event carries,
-- such as a title or body, so that an edit can be delivered as a diff
-- (spec change-reaction, "Delivery to an active run"). Empty for the
-- other groups.
ALTER TABLE hashes ADD COLUMN text TEXT NOT NULL DEFAULT '';
