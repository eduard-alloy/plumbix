-- Triggered pipelines (WU-045): the pipeline under .plumbix/pipelines/ a run ran; empty for the change pipeline.
ALTER TABLE runs ADD COLUMN pipeline TEXT NOT NULL DEFAULT '';
