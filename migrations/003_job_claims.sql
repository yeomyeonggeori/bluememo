ALTER TABLE memory_job
  ADD COLUMN generation bigint NOT NULL DEFAULT 1,
  ADD COLUMN claim_token text NOT NULL DEFAULT '';
