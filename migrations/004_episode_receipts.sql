ALTER TABLE memory_episode ADD COLUMN receipt jsonb;
CREATE INDEX memory_fact_episode_idx ON memory_fact (episode_id);
UPDATE memory_episode episode SET receipt = jsonb_build_object(
  'episodeID', episode.episode_id,
  'factIDs', COALESCE((SELECT jsonb_agg(fact_id ORDER BY fact_id) FROM memory_fact WHERE episode_id = episode.episode_id), '[]'::jsonb),
  'supersededFactIDs', COALESCE((SELECT jsonb_agg(previous.fact_id ORDER BY previous.fact_id) FROM memory_fact previous JOIN memory_fact replacement ON replacement.fact_id = previous.superseded_by WHERE replacement.episode_id = episode.episode_id), '[]'::jsonb),
  'reinforcedFactIDs', '[]'::jsonb,
  'candidateCount', 0
);
ALTER TABLE memory_episode ALTER COLUMN receipt SET NOT NULL;
