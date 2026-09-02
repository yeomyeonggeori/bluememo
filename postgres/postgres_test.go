package postgres_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
	"github.com/yeomyeonggeori/bluememo/postgres"
)

type fixture struct {
	database  *sql.DB
	facts     postgres.FactRepository
	jobs      postgres.JobRepository
	profiles  postgres.ProfileRepository
	now       time.Time
	hasVector bool
}

func openFixture(t *testing.T) fixture {
	t.Helper()
	connectionString := os.Getenv("BLUEMEMO_TEST_POSTGRES_URL")
	if connectionString == "" {
		t.Skip("set BLUEMEMO_TEST_POSTGRES_URL to run the postgres checks")
	}
	ctx := context.Background()
	database, errorValue := sql.Open("pgx", connectionString)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	t.Cleanup(func() { _ = database.Close() })
	for range 2 {
		if errorValue := postgres.ApplyMigrations(ctx, database); errorValue != nil {
			t.Fatalf("expected migrations to apply: %v", errorValue)
		}
	}
	if _, errorValue := database.ExecContext(ctx, `TRUNCATE memory_job, memory_profile, memory_fact, memory_episode CASCADE`); errorValue != nil {
		t.Fatal(errorValue)
	}
	facts := postgres.NewFactRepository(database)
	hasVector, errorValue := facts.HasVectorSearch(ctx)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	return fixture{
		database:  database,
		facts:     facts,
		jobs:      postgres.NewJobRepository(database),
		profiles:  postgres.NewProfileRepository(database),
		now:       time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		hasVector: hasVector,
	}
}

func (fixture fixture) episode(requesterPersonID string) bluememo.Episode {
	return bluememo.Episode{EpisodeID: bluememo.NewIdentifier(), SourceKind: bluememo.EpisodeSourceKindTaskRun, SourceID: bluememo.NewIdentifier(), RequesterPersonID: requesterPersonID, Content: "transcript", OccurredAt: fixture.now}
}

func (fixture fixture) privateFact(episodeID string, ownerPersonID string, content string) bluememo.Fact {
	return bluememo.Fact{FactID: bluememo.NewIdentifier(), EpisodeID: episodeID, OwnerPersonID: ownerPersonID, SubjectPersonID: ownerPersonID, Kind: bluememo.FactKindFact, Content: content, ValidFrom: fixture.now.Add(-time.Hour)}
}

func (fixture fixture) circleFact(episodeID string, content string, circleIDs ...string) bluememo.Fact {
	return bluememo.Fact{FactID: bluememo.NewIdentifier(), EpisodeID: episodeID, OwnerPersonID: "carol", CircleIDs: bluememo.NormalizeCircleIDs(circleIDs), Kind: bluememo.FactKindFact, Content: content, ValidFrom: fixture.now.Add(-time.Hour)}
}

func (fixture fixture) workspaceFact(episodeID string, content string) bluememo.Fact {
	return fixture.circleFact(episodeID, content, "member")
}

func (fixture fixture) save(t *testing.T, episode bluememo.Episode, writes ...bluememo.FactWrite) {
	t.Helper()
	if errorValue := fixture.facts.SaveEpisode(context.Background(), bluememo.EpisodeWrite{Episode: episode, Facts: writes}); errorValue != nil {
		t.Fatalf("expected the episode to save: %v", errorValue)
	}
}

func (fixture fixture) search(t *testing.T, reader bluememo.Reader, text string) map[string]bluememo.RankedFact {
	t.Helper()
	hits, errorValue := fixture.facts.SearchFacts(context.Background(), bluememo.FactSearchQuery{Reader: reader, Text: text, ReferenceTime: fixture.now})
	if errorValue != nil {
		t.Fatalf("expected the search to run: %v", errorValue)
	}
	byContent := map[string]bluememo.RankedFact{}
	for _, hit := range hits {
		byContent[hit.Fact.Content] = hit
	}
	return byContent
}

func TestReaderFilterGatesScopeContainmentRankAndClasses(t *testing.T) {
	fixture := openFixture(t)
	episode := fixture.episode("alice")
	own := fixture.privateFact(episode.EpisodeID, "alice", "이샘플 owns the Q3 review 프로젝트")
	other := fixture.privateFact(episode.EpisodeID, "bob", "박예시 owns the Q3 budget 프로젝트")
	platform := fixture.circleFact(episode.EpisodeID, "the platform circle runs the Q3 프로젝트 retro", "platform")
	shared := fixture.circleFact(episode.EpisodeID, "sales and data share the Q3 프로젝트 dashboard", "sales", "data")
	sales := fixture.circleFact(episode.EpisodeID, "the sales circle closes the Q3 프로젝트 books", "sales")
	open := fixture.workspaceFact(episode.EpisodeID, "the Q3 프로젝트 review is on 2026-09-20")
	secret := fixture.workspaceFact(episode.EpisodeID, "the Q3 프로젝트 headcount plan is frozen")
	secret.SecurityLevelRank = 3
	classed := fixture.workspaceFact(episode.EpisodeID, "the Q3 프로젝트 legal hold list")
	classed.RequiredClasses = []string{"legal"}
	fixture.save(t, episode, bluememo.FactWrite{Fact: own}, bluememo.FactWrite{Fact: other}, bluememo.FactWrite{Fact: platform}, bluememo.FactWrite{Fact: shared}, bluememo.FactWrite{Fact: sales}, bluememo.FactWrite{Fact: open}, bluememo.FactWrite{Fact: secret}, bluememo.FactWrite{Fact: classed})

	reader := bluememo.NewReader("alice", []string{"member", "engineering"}, map[string][]string{"engineering": {"platform", "data"}}, 1, nil)
	hits := fixture.search(t, reader, "Q3 프로젝트")
	for _, visible := range []bluememo.Fact{own, platform, shared, open} {
		if _, isVisible := hits[visible.Content]; !isVisible {
			t.Fatalf("expected %q to be readable, got %v", visible.Content, hits)
		}
	}
	for _, hidden := range []bluememo.Fact{other, sales, secret, classed} {
		if _, isVisible := hits[hidden.Content]; isVisible {
			t.Fatalf("expected %q to be hidden", hidden.Content)
		}
	}
	listed, errorValue := fixture.facts.ListReadableFacts(context.Background(), reader, 10, fixture.now)
	if errorValue != nil || len(listed) != 4 {
		t.Fatalf("expected four readable facts listed, got %d (%v)", len(listed), errorValue)
	}
	for _, fact := range listed {
		if fact.Content == shared.Content && len(fact.CircleIDs) != 2 {
			t.Fatalf("expected the circles to round-trip, got %+v", fact)
		}
	}
}

func TestSupersedeExpiryReinforcementAndForget(t *testing.T) {
	fixture := openFixture(t)
	first := fixture.episode("alice")
	old := fixture.privateFact(first.EpisodeID, "alice", "이샘플 works at Google as an engineer")
	preference := fixture.privateFact(first.EpisodeID, "alice", "이샘플 prefers bullet summaries")
	preference.Kind = bluememo.FactKindPreference
	expired := fixture.privateFact(first.EpisodeID, "alice", "이샘플 is out of office until yesterday")
	expired.Kind, expired.ValidUntil = bluememo.FactKindTemporary, fixture.now.Add(-time.Minute)
	current := fixture.privateFact(first.EpisodeID, "alice", "이샘플 is out of office until next week")
	current.Kind, current.ValidUntil = bluememo.FactKindTemporary, fixture.now.Add(7*24*time.Hour)
	fixture.save(t, first, bluememo.FactWrite{Fact: old}, bluememo.FactWrite{Fact: preference}, bluememo.FactWrite{Fact: expired}, bluememo.FactWrite{Fact: current})

	second := fixture.episode("alice")
	replacement := fixture.privateFact(second.EpisodeID, "alice", "이샘플 works at Stripe as a product manager")
	fixture.save(t, second, bluememo.FactWrite{Fact: replacement, SupersedesFactID: old.FactID}, bluememo.FactWrite{ReinforcesFactID: preference.FactID})

	reader := bluememo.NewReader("alice", nil, nil, 1, nil)
	hits := fixture.search(t, reader, "이샘플 works out of office")
	if _, isVisible := hits[old.Content]; isVisible {
		t.Fatal("expected the superseded fact to leave search")
	}
	if _, isVisible := hits[expired.Content]; isVisible {
		t.Fatal("expected the expired fact to leave search")
	}
	for _, expected := range []bluememo.Fact{replacement, current} {
		if _, isVisible := hits[expected.Content]; !isVisible {
			t.Fatalf("expected %q to be found, got %v", expected.Content, hits)
		}
	}
	var supersededBy string
	if errorValue := fixture.database.QueryRow(`SELECT superseded_by FROM memory_fact WHERE fact_id = $1`, old.FactID).Scan(&supersededBy); errorValue != nil || supersededBy != replacement.FactID {
		t.Fatalf("expected the old row to survive pointing at %s, got %q (%v)", replacement.FactID, supersededBy, errorValue)
	}
	facts, errorValue := fixture.facts.ListFactsByID(context.Background(), reader, []string{preference.FactID}, fixture.now)
	if errorValue != nil || len(facts) != 1 || facts[0].ReinforcementCount != 2 {
		t.Fatalf("expected the preference reinforced to 2, got %+v (%v)", facts, errorValue)
	}
	third := fixture.episode("alice")
	stale := fixture.privateFact(third.EpisodeID, "alice", "이샘플 works nowhere")
	if errorValue := fixture.facts.SaveEpisode(context.Background(), bluememo.EpisodeWrite{Episode: third, Facts: []bluememo.FactWrite{{Fact: stale, SupersedesFactID: old.FactID}}}); errorValue == nil {
		t.Fatal("expected superseding an already superseded fact to fail")
	}

	stranger := bluememo.NewReader("bob", nil, nil, 1, nil)
	forgotten, errorValue := fixture.facts.ForgetFacts(context.Background(), stranger, []string{replacement.FactID}, "not mine", fixture.now)
	if errorValue != nil || len(forgotten) != 0 {
		t.Fatalf("expected a stranger to forget nothing, got %v (%v)", forgotten, errorValue)
	}
	forgotten, errorValue = fixture.facts.ForgetFacts(context.Background(), reader, []string{replacement.FactID}, "moved on", fixture.now)
	if errorValue != nil || len(forgotten) != 1 {
		t.Fatalf("expected the owner to forget the fact, got %v (%v)", forgotten, errorValue)
	}
	var reason string
	if errorValue := fixture.database.QueryRow(`SELECT forget_reason FROM memory_fact WHERE fact_id = $1 AND forgotten_at IS NOT NULL`, replacement.FactID).Scan(&reason); errorValue != nil || reason != "moved on" {
		t.Fatalf("expected the forgotten row to keep its reason, got %q (%v)", reason, errorValue)
	}
}

func TestVectorSearchRanksByEmbeddingWhereAvailable(t *testing.T) {
	fixture := openFixture(t)
	episode := fixture.episode("alice")
	near := fixture.privateFact(episode.EpisodeID, "alice", "the standup moved to 10am")
	far := fixture.privateFact(episode.EpisodeID, "alice", "the parking garage closes at midnight")
	fixture.save(t, episode, bluememo.FactWrite{Fact: near, Embedding: unitEmbedding(0)}, bluememo.FactWrite{Fact: far, Embedding: unitEmbedding(1)})
	reader := bluememo.NewReader("alice", nil, nil, 1, nil)
	hits, errorValue := fixture.facts.SearchFacts(context.Background(), bluememo.FactSearchQuery{Reader: reader, Text: "zzzz", Embedding: unitEmbedding(0), ReferenceTime: fixture.now})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if !fixture.hasVector {
		if len(hits) != 0 {
			t.Fatalf("expected a lexical-only database to ignore the embedding, got %+v", hits)
		}
		return
	}
	ranks := map[string]int{}
	for _, hit := range hits {
		ranks[hit.Fact.FactID] = hit.VectorRank
	}
	if ranks[near.FactID] != 1 || ranks[far.FactID] != 2 {
		t.Fatalf("expected vector ranks near=1 far=2, got %v", ranks)
	}
}

func TestJobsDeduplicateClaimLeaseRetryAndFinish(t *testing.T) {
	fixture := openFixture(t)
	ctx := context.Background()
	first, created, errorValue := fixture.jobs.EnqueueJob(ctx, bluememo.JobKindExtract, "run-1", fixture.now)
	if errorValue != nil || !created {
		t.Fatalf("expected the first enqueue to create a job, got created=%v (%v)", created, errorValue)
	}
	if duplicate, created, _ := fixture.jobs.EnqueueJob(ctx, bluememo.JobKindExtract, "run-1", fixture.now); created || duplicate.JobID != first.JobID {
		t.Fatalf("expected the duplicate to return the pending job, got created=%v %+v", created, duplicate)
	}
	claimed, errorValue := fixture.jobs.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, fixture.now, time.Minute, 10)
	if errorValue != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("expected one claim, got %+v (%v)", claimed, errorValue)
	}
	if again, _ := fixture.jobs.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, fixture.now.Add(time.Second), time.Minute, 10); len(again) != 0 {
		t.Fatalf("expected the leased job to stay claimed, got %+v", again)
	}
	if errorValue := fixture.jobs.RetryJob(ctx, first.JobID, "model unavailable", fixture.now.Add(time.Minute)); errorValue != nil {
		t.Fatal(errorValue)
	}
	retried, errorValue := fixture.jobs.ClaimDueJobs(ctx, []string{bluememo.JobKindExtract}, fixture.now.Add(2*time.Minute), time.Minute, 10)
	if errorValue != nil || len(retried) != 1 || retried[0].Attempts != 2 || retried[0].LastError != "model unavailable" {
		t.Fatalf("expected the retried job claimed with attempts=2, got %+v (%v)", retried, errorValue)
	}
	if errorValue := fixture.jobs.FinishJob(ctx, first.JobID, fixture.now.Add(3*time.Minute)); errorValue != nil {
		t.Fatal(errorValue)
	}
	if _, created, _ := fixture.jobs.EnqueueJob(ctx, bluememo.JobKindExtract, "run-1", fixture.now); !created {
		t.Fatal("expected a finished subject to accept a new job")
	}
	if errorValue := fixture.profiles.SaveProfile(ctx, bluememo.Profile{PersonID: "alice", IdentityLines: []string{"이샘플 wants bullets"}, CurrentLines: []string{}, BuiltFromFactCount: 1, BuiltAt: fixture.now}); errorValue != nil {
		t.Fatal(errorValue)
	}
	if profile, isFound, errorValue := fixture.profiles.FindProfile(ctx, "alice"); errorValue != nil || !isFound || profile.IdentityLines[0] != "이샘플 wants bullets" {
		t.Fatalf("expected the profile to round-trip, got %+v found=%v (%v)", profile, isFound, errorValue)
	}
}

func TestIngestThroughPostgresEndsAsRowsAndRecall(t *testing.T) {
	fixture := openFixture(t)
	ctx := context.Background()
	scripted := bluememotest.NewScriptedModel()
	store := bluememo.Store{Facts: fixture.facts, Profiles: fixture.profiles, Jobs: fixture.jobs, Embedder: &bluememotest.HashEmbedder{}, EmbeddingModel: "test-embed", Now: func() time.Time { return fixture.now }}
	ingester := bluememo.Ingester{Store: store, Model: scripted, Now: func() time.Time { return fixture.now }}
	scripted.Queue(bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 works in the platform team", Kind: bluememo.FactKindIdentity, CircleIDs: []string{"platform"}, SubjectPersonHint: "이샘플", Relation: bluememo.FactRelationNew},
		bluememotest.IngestFact{Content: "이샘플 prefers bullet summaries", Kind: bluememo.FactKindPreference, SubjectPersonHint: "이샘플", Relation: bluememo.FactRelationNew},
	))
	result, errorValue := ingester.Ingest(ctx, bluememo.IngestRequest{
		Episode:       bluememo.Episode{EpisodeID: "episode-1", SourceKind: bluememo.EpisodeSourceKindTaskRun, SourceID: "run-1", RequesterPersonID: "alice", Content: "나 플랫폼 팀으로 옮겼어. 요약은 불릿으로.", OccurredAt: fixture.now},
		Reader:        bluememo.NewReader("alice", []string{"platform"}, nil, 1, nil),
		RequesterName: "이샘플",
		Label:         bluememo.SecurityLabel{SecurityLevelRank: 1, RequiredClasses: []string{}},
	})
	if errorValue != nil || len(result.Facts) != 2 {
		t.Fatalf("expected two facts, got %+v (%v)", result, errorValue)
	}
	scripted.Queue(bluememotest.ProfileResponse([]string{"이샘플 is on the platform team and wants bullets"}, []string{}))
	worker := bluememo.JobWorker{Jobs: fixture.jobs, Now: func() time.Time { return fixture.now }, Handlers: map[string]bluememo.JobHandler{
		bluememo.JobKindProfile: bluememo.ProfileJobHandler{Builder: bluememo.ProfileBuilder{Store: store, Model: scripted, Now: func() time.Time { return fixture.now }}}.Handle,
	}}
	if runCount, errorValue := worker.RunOnce(ctx); errorValue != nil || runCount != 1 {
		t.Fatalf("expected the profile job to run once, got %d (%v)", runCount, errorValue)
	}
	recall, errorValue := store.Recall(ctx, bluememo.RecallRequest{Reader: bluememo.NewReader("bob", []string{"engineering"}, map[string][]string{"engineering": {"platform"}}, 1, nil), PersonID: "alice", Query: "platform team"})
	if errorValue != nil || len(recall.Profile.IdentityLines) != 1 || len(recall.Facts) != 1 || recall.Facts[0].Fact.Content != "이샘플 works in the platform team" {
		t.Fatalf("expected the profile and the circle fact through containment, got %+v (%v)", recall, errorValue)
	}
}

func unitEmbedding(axis int) []float32 {
	embedding := make([]float32, bluememo.EmbeddingDimensionCount)
	embedding[axis] = 1
	return embedding
}

func TestReembedMovesFactsToTheStoreModel(t *testing.T) {
	fixture := openFixture(t)
	if !fixture.hasVector {
		t.Skip("the database has no vector extension")
	}
	episode := fixture.episode("alice")
	fact := fixture.privateFact(episode.EpisodeID, "alice", "the standup moved to 10am")
	fact.EmbeddingModel = "old-model"
	fixture.save(t, episode, bluememo.FactWrite{Fact: fact, Embedding: unitEmbedding(0)})
	reader := bluememo.NewReader("alice", nil, nil, 1, nil)
	query := bluememo.FactSearchQuery{Reader: reader, Text: "zzzz", Embedding: unitEmbedding(0), EmbeddingModel: "new-model", ReferenceTime: fixture.now}
	hits, errorValue := fixture.facts.SearchFacts(context.Background(), query)
	if errorValue != nil || len(hits) != 0 {
		t.Fatalf("expected a fact embedded by another model to stay out of vector hits, got %d (%v)", len(hits), errorValue)
	}
	store := bluememo.Store{Facts: fixture.facts, Jobs: fixture.jobs, Embedder: fixedEmbedder{embedding: unitEmbedding(1)}, EmbeddingModel: "new-model", Now: func() time.Time { return fixture.now }}
	job, _, errorValue := store.EnqueueReembed(context.Background())
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if errorValue := (bluememo.ReembedJobHandler{Store: store}).Handle(context.Background(), job); errorValue != nil {
		t.Fatalf("expected the reembed to run: %v", errorValue)
	}
	query.Embedding = unitEmbedding(1)
	hits, errorValue = fixture.facts.SearchFacts(context.Background(), query)
	if errorValue != nil || len(hits) != 1 || hits[0].VectorRank != 1 || hits[0].Fact.EmbeddingModel != "new-model" {
		t.Fatalf("expected the fact to rank by vector under the store model, got %+v (%v)", hits, errorValue)
	}
}

type fixedEmbedder struct {
	embedding []float32
}

func (embedder fixedEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return embedder.embedding, nil
}

func (embedder fixedEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(texts))
	for range texts {
		embeddings = append(embeddings, embedder.embedding)
	}
	return embeddings, nil
}
