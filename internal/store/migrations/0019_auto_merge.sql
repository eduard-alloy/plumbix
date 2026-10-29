-- WU-063: GitHub merged on the requester's behalf (auto-merge), not a person's press.
ALTER TABLE merges ADD COLUMN auto_merge INTEGER NOT NULL DEFAULT 0;
