-- A person's approval of a needs_human or escalated change (WU-021).
ALTER TABLE changes ADD COLUMN approved_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN approved_head TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN approved_at TEXT NOT NULL DEFAULT '';
