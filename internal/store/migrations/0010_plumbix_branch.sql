-- Plumbix created the change's source branch, so it may refresh it from the target (WU-022).
ALTER TABLE changes ADD COLUMN plumbix_branch INTEGER NOT NULL DEFAULT 0;
