package postgres_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/migrations"
	"github.com/yeomyeonggeori/bluememo/postgres"
)

func TestMigrationUpgradePreservesFactsAndLegacyProfiles(t *testing.T) {
	database, _ := openMigrationDatabase(t)
	list, errorValue := migrations.List()
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, errorValue := database.Exec(list[0].SQL); errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, errorValue := database.Exec(`
CREATE TABLE memory_schema_migration (file_name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO memory_schema_migration (file_name) VALUES ('001_memory_store.sql');
INSERT INTO memory_episode (episode_id, source_kind, source_id, requester_person_id, content, occurred_at)
VALUES ('legacy-episode', 'explicit', 'legacy-source', 'reader', 'legacy source', now());
INSERT INTO memory_fact (fact_id, episode_id, owner_person_id, subject_person_id, kind, content, valid_from)
VALUES ('legacy-fact', 'legacy-episode', 'reader', 'reader', 'preference', 'legacy preference', now());
INSERT INTO memory_profile (person_id, identity_lines, built_from_fact_count, built_at)
VALUES ('reader', ARRAY['legacy summary'], 1, now())`); errorValue != nil {
		t.Fatal(errorValue)
	}
	if errorValue := postgres.ApplyMigrations(context.Background(), database); errorValue != nil {
		t.Fatal(errorValue)
	}
	repository := postgres.NewFactRepository(database)
	receipt, isFound, errorValue := repository.FindEpisodeReceipt(context.Background(), bluememo.Episode{SourceKind: "explicit", SourceID: "legacy-source", RequesterPersonID: "reader", Content: "legacy source"})
	if errorValue != nil || !isFound || receipt.EpisodeID != "legacy-episode" || len(receipt.FactIDs) != 1 || receipt.FactIDs[0] != "legacy-fact" {
		t.Fatalf("upgrade must reconstruct legacy fact provenance: %+v (%v)", receipt, errorValue)
	}
	profiles := postgres.NewProfileRepository(database)
	profile, isFound, errorValue := profiles.FindProfile(context.Background(), "reader")
	if errorValue != nil || !isFound || len(profile.IdentityLines) != 1 || profile.IdentityLines[0] != "legacy summary" {
		t.Fatalf("upgrade must retain the legacy cache: %+v (%v)", profile, errorValue)
	}
	store := bluememo.Store{Facts: repository, Profiles: profiles, Jobs: postgres.NewJobRepository(database)}
	recall, errorValue := store.Recall(context.Background(), bluememo.RecallRequest{Reader: bluememo.NewReader("reader", nil, nil, 0, nil)})
	if errorValue != nil || len(recall.ProfileLines()) != 0 {
		t.Fatalf("unverified legacy cache must be withheld: %+v (%v)", recall, errorValue)
	}
	var profileJobCount int
	if errorValue := database.QueryRow(`SELECT count(*) FROM memory_job WHERE kind = 'profile' AND subject_id = 'reader' AND finished_at IS NULL`).Scan(&profileJobCount); errorValue != nil || profileJobCount != 1 {
		t.Fatalf("expected one legacy cache rebuild job, got %d (%v)", profileJobCount, errorValue)
	}
}
