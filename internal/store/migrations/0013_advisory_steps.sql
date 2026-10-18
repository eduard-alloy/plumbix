-- Advisory validators (WU-030, A31): a step whose verdict was recorded but not counted.
ALTER TABLE steps ADD COLUMN advisory INTEGER NOT NULL DEFAULT 0;
