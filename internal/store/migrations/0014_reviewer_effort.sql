-- Reasoning effort per API reviewer profile (WU-034): low, medium, high or '' for the vendor's default.
ALTER TABLE reviewer_profiles ADD COLUMN effort TEXT NOT NULL DEFAULT '';
