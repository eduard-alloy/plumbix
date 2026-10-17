-- Step records carry retries, tolerated failures, skip reasons and stages (WU-019).
ALTER TABLE steps ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN allowed_failure INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN stage TEXT NOT NULL DEFAULT '';
