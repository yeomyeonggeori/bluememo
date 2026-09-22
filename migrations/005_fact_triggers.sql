CREATE TABLE IF NOT EXISTS memory_fact_trigger (
  trigger_id text PRIMARY KEY,
  fact_id text NOT NULL REFERENCES memory_fact (fact_id) ON DELETE CASCADE,
  phrase text NOT NULL CHECK (char_length(phrase) BETWEEN 1 AND 80),
  embedding_model text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS memory_fact_trigger_phrase_idx
  ON memory_fact_trigger (fact_id, phrase);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
    RETURN;
  END IF;
  EXECUTE 'CREATE TABLE IF NOT EXISTS memory_fact_trigger_embedding (
    trigger_id text PRIMARY KEY REFERENCES memory_fact_trigger (trigger_id) ON DELETE CASCADE,
    embedding vector(1024) NOT NULL
  )';
END $$;
