-- WU-068: why the platform closed a review without a verdict (timed_out,
-- run_cancelled, server_stopped, server_restart); empty unless expired.
ALTER TABLE reviews ADD COLUMN expired_reason TEXT NOT NULL DEFAULT '';
