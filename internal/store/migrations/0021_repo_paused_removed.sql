-- WU-065: a repository can be paused (no polling, no intake) or removed
-- (gone from every listing and lookup; its changes stay for the record).
ALTER TABLE repos ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repos ADD COLUMN removed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE repos ADD COLUMN removed_by TEXT NOT NULL DEFAULT '';
