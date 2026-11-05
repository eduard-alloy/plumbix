-- WU-071: the reviewer's own words when it gave a claimed review back
-- (checkout_failed, reviewer_error); empty otherwise.
ALTER TABLE reviews ADD COLUMN expired_message TEXT NOT NULL DEFAULT '';
