-- WU-060: who approved, and through which surface (plumbix or github_review).
ALTER TABLE changes ADD COLUMN approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN approved_source TEXT NOT NULL DEFAULT '';
