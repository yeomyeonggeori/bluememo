package bluememo_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

var ingestNow = time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)

type ingestFixture struct {
	sequence   *int
	repository *bluememo.InMemoryRepository
	embedder   *bluememotest.HashEmbedder
	model      *bluememotest.ScriptedModel
	ingester   bluememo.Ingester
}

func newIngestFixture() ingestFixture {
	repository := bluememo.NewInMemoryRepository()
	embedder := &bluememotest.HashEmbedder{}
	scripted := bluememotest.NewScriptedModel()
	store := bluememo.Store{Facts: repository, Profiles: repository, Jobs: repository, Embedder: embedder, EmbeddingModel: "test-embed", Now: func() time.Time { return ingestNow }}
	sequence := 0
	return ingestFixture{
		sequence:   &sequence,
		repository: repository,
		embedder:   embedder,
		model:      scripted,
		ingester:   bluememo.Ingester{Store: store, Model: scripted, Now: func() time.Time { return ingestNow }},
	}
}

func (fixture ingestFixture) request(content string) bluememo.IngestRequest {
	*fixture.sequence++
	sequence := strconv.Itoa(*fixture.sequence)
	return bluememo.IngestRequest{
		Episode: bluememo.Episode{
			EpisodeID:         "episode-" + sequence,
			SourceKind:        bluememo.EpisodeSourceKindExplicit,
			SourceID:          "source-" + sequence,
			RequesterPersonID: "person-alice",
			Content:           content,
			OccurredAt:        ingestNow,
		},
		Reader:        bluememo.NewReader("person-alice", []string{"member", "engineering", "platform"}, map[string][]string{"engineering": {"platform", "data"}}, 1, nil),
		RequesterName: "이샘플",
		Label:         bluememo.SecurityLabel{SecurityLevelRank: 1, RequiredClasses: []string{}},
	}
}

func (fixture ingestFixture) ingest(t *testing.T, content string, response map[string]any, mutate func(*bluememo.IngestRequest)) bluememo.IngestResult {
	t.Helper()
	fixture.model.Queue(response)
	request := fixture.request(content)
	if mutate != nil {
		mutate(&request)
	}
	result, errorValue := fixture.ingester.Ingest(context.Background(), request)
	if errorValue != nil {
		t.Fatalf("expected ingest to succeed: %v", errorValue)
	}
	return result
}

func TestIngestRecordsNewFactsWithScopeLabelAndSubject(t *testing.T) {
	fixture := newIngestFixture()
	result := fixture.ingest(t, "이샘플 moved to the platform team and prefers bullet summaries", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 works in the platform team", Kind: bluememo.FactKindIdentity, CircleIDs: []string{"member"}, SubjectPersonHint: "이샘플", Relation: bluememo.FactRelationNew},
		bluememotest.IngestFact{Content: "이샘플 prefers bullet summaries", Kind: bluememo.FactKindPreference, Relation: bluememo.FactRelationNew},
	), nil)
	if len(result.Facts) != 2 || len(result.SupersededFactIDs) != 0 {
		t.Fatalf("expected two new facts, got %+v", result)
	}
	sharedFact, ownFact := result.Facts[0], result.Facts[1]
	if strings.Join(sharedFact.CircleIDs, ",") != "member" || sharedFact.OwnerPersonID != "person-alice" || sharedFact.SubjectPersonID != "person-alice" || sharedFact.SecurityLevelRank != 1 {
		t.Fatalf("expected a labelled fact shared with member and owned by the requester, got %+v", sharedFact)
	}
	if ownFact.IsShared() || ownFact.OwnerPersonID != "person-alice" || ownFact.SubjectPersonID != "person-alice" || ownFact.SecurityLevelRank != 0 {
		t.Fatalf("expected an unlabelled fact for the requester alone, got %+v", ownFact)
	}
	if stored, isFound := fixture.repository.FindFact(ownFact.FactID); !isFound || stored.EmbeddingModel != "test-embed" || stored.EpisodeID != result.EpisodeID {
		t.Fatalf("expected the fact stored with its embedding model and episode, got %+v", stored)
	}
	if _, isCreated, _ := fixture.repository.EnqueueJob(context.Background(), bluememo.JobKindProfile, "person-alice", ingestNow); isCreated {
		t.Fatal("expected a profile rebuild to be pending for the subject")
	}
	subject := fixture.model.LastSubject()
	if !strings.Contains(subject, "Requester's circles: engineering, member, platform") || !strings.Contains(subject, "Existing facts closest to the source:\n(none)") {
		t.Fatalf("expected the requester's circles and an empty candidate list, got %s", subject)
	}
}

func TestIngestSharesOnlyWithCirclesTheRequesterIsIn(t *testing.T) {
	fixture := newIngestFixture()
	shared := fixture.ingest(t, "the platform and data circles meet on Mondays", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "the platform and data circles meet on Mondays", Kind: bluememo.FactKindFact, CircleIDs: []string{"Platform", "data", "sales"}, Relation: bluememo.FactRelationNew},
	), nil)
	if strings.Join(shared.Facts[0].CircleIDs, ",") != "platform" || shared.Facts[0].SecurityLevelRank != 1 {
		t.Fatalf("expected only the circles the requester is a member of, with the label kept, got %+v", shared.Facts[0])
	}
	narrowed := fixture.ingest(t, "said in a direct message", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "sales closes the quarter on Friday", Kind: bluememo.FactKindFact, CircleIDs: []string{"sales"}, Relation: bluememo.FactRelationNew},
	), nil)
	if narrowed.Facts[0].IsShared() || narrowed.Facts[0].OwnerPersonID != "person-alice" || narrowed.Facts[0].SecurityLevelRank != 0 {
		t.Fatalf("expected a fact for a foreign circle to fall back to the requester alone, got %+v", narrowed.Facts[0])
	}
}

func TestIngestSupersedesAndReinforcesOnlyCandidates(t *testing.T) {
	fixture := newIngestFixture()
	first := fixture.ingest(t, "이샘플 works at Google as an engineer and likes terse notes", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 works at Google as an engineer", Kind: bluememo.FactKindFact, Relation: bluememo.FactRelationNew},
		bluememotest.IngestFact{Content: "이샘플 likes terse notes", Kind: bluememo.FactKindPreference, Relation: bluememo.FactRelationNew},
	), nil)
	jobFact, preferenceFact := first.Facts[0], first.Facts[1]
	second := fixture.ingest(t, "이샘플 works at Stripe now as a product manager and still likes terse notes", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 works at Stripe as a product manager", Kind: bluememo.FactKindFact, Relation: bluememo.FactRelationSupersedes, RelatedFactID: jobFact.FactID},
		bluememotest.IngestFact{Content: "이샘플 likes terse notes", Kind: bluememo.FactKindPreference, Relation: bluememo.FactRelationReinforces, RelatedFactID: preferenceFact.FactID},
	), nil)
	if len(second.Facts) != 1 || len(second.SupersededFactIDs) != 1 || second.SupersededFactIDs[0] != jobFact.FactID || len(second.ReinforcedFactIDs) != 1 {
		t.Fatalf("expected one supersede and one reinforcement, got %+v", second)
	}
	if !strings.Contains(fixture.model.LastSubject(), "id="+jobFact.FactID) {
		t.Fatalf("expected the earlier fact offered as a candidate, got %s", fixture.model.LastSubject())
	}
	if oldFact, _ := fixture.repository.FindFact(jobFact.FactID); oldFact.SupersededBy != second.Facts[0].FactID {
		t.Fatalf("expected the old fact to point at its replacement, got %+v", oldFact)
	}
	if reinforced, _ := fixture.repository.FindFact(preferenceFact.FactID); reinforced.ReinforcementCount != 2 {
		t.Fatalf("expected the preference reinforced to 2, got %d", reinforced.ReinforcementCount)
	}
	fixture.model.Queue(bluememotest.IngestResponse(bluememotest.IngestFact{Content: "이샘플 works at Acme", Kind: bluememo.FactKindFact, Relation: bluememo.FactRelationSupersedes, RelatedFactID: "never-offered"}))
	_, errorValue := fixture.ingester.Ingest(context.Background(), fixture.request("이샘플 works at Acme now"))
	var terminal bluememo.TerminalJobError
	if !errors.As(errorValue, &terminal) || len(fixture.repository.AllFacts()) != 3 {
		t.Fatalf("expected a terminal error and nothing written for an invented fact ID, got %v with %d facts", errorValue, len(fixture.repository.AllFacts()))
	}
}

func TestIngestTemporaryFactsCarryTheirExpiry(t *testing.T) {
	fixture := newIngestFixture()
	result := fixture.ingest(t, "이샘플 is out of office until 2026-09-05", bluememotest.IngestResponse(bluememotest.IngestFact{Content: "이샘플 is out of office until 2026-09-05", Kind: bluememo.FactKindTemporary, Relation: bluememo.FactRelationNew, ValidUntil: "2026-09-05"}), nil)
	if result.Facts[0].ValidUntil != time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("expected validUntil at the end of the day, got %s", result.Facts[0].ValidUntil)
	}
	for name, fact := range map[string]bluememotest.IngestFact{
		"temporary without expiry": {Content: "이샘플 is away", Kind: bluememo.FactKindTemporary, Relation: bluememo.FactRelationNew},
		"expiry in the past":       {Content: "이샘플 was away", Kind: bluememo.FactKindTemporary, Relation: bluememo.FactRelationNew, ValidUntil: "2026-08-01"},
		"durable fact with expiry": {Content: "이샘플 leads the team", Kind: bluememo.FactKindFact, Relation: bluememo.FactRelationNew, ValidUntil: "2026-09-05"},
	} {
		fixture.model.Queue(bluememotest.IngestResponse(fact))
		_, errorValue := fixture.ingester.Ingest(context.Background(), fixture.request("이샘플 availability "+name))
		var terminal bluememo.TerminalJobError
		if !errors.As(errorValue, &terminal) {
			t.Fatalf("%s: expected a terminal error, got %v", name, errorValue)
		}
	}
}

func TestIngestFailuresAreRetryableWhenTheModelOrEmbedderIsDown(t *testing.T) {
	fixture := newIngestFixture()
	fixture.model.Failure = errors.New("gateway timeout")
	_, errorValue := fixture.ingester.Ingest(context.Background(), fixture.request("이샘플 said something worth keeping"))
	var terminal bluememo.TerminalJobError
	if errorValue == nil || errors.As(errorValue, &terminal) {
		t.Fatalf("expected a retryable model failure, got %v", errorValue)
	}
	fixture.model.Failure = nil
	fixture.embedder.Failure = errors.New("embedding gateway down")
	fixture.model.Queue(bluememotest.IngestResponse())
	_, errorValue = fixture.ingester.Ingest(context.Background(), fixture.request("이샘플 said something else"))
	if errorValue == nil || errors.As(errorValue, &terminal) || len(fixture.repository.AllFacts()) != 0 {
		t.Fatalf("expected a retryable embedding failure with nothing written, got %v", errorValue)
	}
}

func TestProfileBuilderCondensesFactsAndSkipsTheModelWhenEmpty(t *testing.T) {
	fixture := newIngestFixture()
	builder := bluememo.ProfileBuilder{Store: fixture.ingester.Store, Model: fixture.model, Now: func() time.Time { return ingestNow }}
	if empty, errorValue := builder.Rebuild(context.Background(), "person-nobody"); errorValue != nil || len(empty.IdentityLines) != 0 || fixture.model.RequestCount() != 0 {
		t.Fatalf("expected an empty profile without a model call, got %+v (%v)", empty, errorValue)
	}
	fixture.ingest(t, "이샘플 works in the platform team and prefers bullet summaries", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 works in the platform team", Kind: bluememo.FactKindIdentity, Relation: bluememo.FactRelationNew},
		bluememotest.IngestFact{Content: "이샘플 is migrating admind config this week", Kind: bluememo.FactKindFact, Relation: bluememo.FactRelationNew},
	), nil)
	fixture.model.Queue(bluememotest.ProfileResponse([]string{"이샘플 is on the platform team"}, []string{"이샘플 is migrating admind config", "", "  "}))
	profile, errorValue := builder.Rebuild(context.Background(), "person-alice")
	if errorValue != nil || len(profile.IdentityLines) != 1 || len(profile.CurrentLines) != 1 || profile.BuiltFromFactCount != 2 {
		t.Fatalf("expected a condensed profile, got %+v (%v)", profile, errorValue)
	}
	if !strings.Contains(fixture.model.LastSubject(), "[identity, 2026-09-02] 이샘플 works in the platform team") {
		t.Fatalf("expected the facts listed to the model, got %s", fixture.model.LastSubject())
	}
}

func TestStoreSearchRecallAndBudget(t *testing.T) {
	fixture := newIngestFixture()
	fixture.ingest(t, "이샘플 prefers bullet summaries and owns the Q3 review", bluememotest.IngestResponse(
		bluememotest.IngestFact{Content: "이샘플 prefers bullet summaries", Kind: bluememo.FactKindPreference, Relation: bluememo.FactRelationNew},
		bluememotest.IngestFact{Content: "이샘플 owns the Q3 review", Kind: bluememo.FactKindFact, CircleIDs: []string{"platform"}, Relation: bluememo.FactRelationNew},
	), nil)
	if errorValue := fixture.repository.SaveProfile(context.Background(), bluememo.Profile{PersonID: "person-alice", IdentityLines: []string{"이샘플 wants bullets"}, CurrentLines: []string{strings.Repeat("가", 300)}}); errorValue != nil {
		t.Fatal(errorValue)
	}
	store := fixture.ingester.Store
	engineeringReader := bluememo.NewReader("person-bob", []string{"engineering"}, map[string][]string{"engineering": {"platform"}}, 1, nil)
	result, errorValue := store.Search(context.Background(), engineeringReader, "Q3 review", 5)
	if errorValue != nil || result.Mode != bluememo.SearchModeHybrid || len(result.Facts) != 1 || result.Facts[0].Fact.Content != "이샘플 owns the Q3 review" {
		t.Fatalf("expected a member of the containing circle to find the platform fact, got %+v (%v)", result, errorValue)
	}
	hidden, _ := store.Search(context.Background(), engineeringReader, "bullet summaries", 5)
	for _, scoredFact := range hidden.Facts {
		if !scoredFact.Fact.IsShared() {
			t.Fatalf("expected a fact with no circles to stay with its owner, got %+v", scoredFact.Fact)
		}
	}
	recall, errorValue := store.Recall(context.Background(), bluememo.RecallRequest{Reader: fixture.request("").Reader, PersonID: "person-alice", Query: "bullet summaries", ProfileBudget: 100})
	if errorValue != nil || len(recall.Profile.IdentityLines) != 1 || len(recall.Profile.CurrentLines) != 0 || len(recall.Facts) == 0 {
		t.Fatalf("expected the profile trimmed to budget and a recalled fact, got %+v (%v)", recall, errorValue)
	}
	lexical := bluememo.Store{Facts: fixture.repository, Embedder: &bluememotest.HashEmbedder{Failure: errors.New("gateway down")}}
	degraded, errorValue := lexical.Search(context.Background(), engineeringReader, "Q3 review", 5)
	if errorValue != nil || degraded.Mode != bluememo.SearchModeLexical || degraded.DegradedReason != "query embedding failed: gateway down" {
		t.Fatalf("expected a lexical fallback that says why, got %+v (%v)", degraded, errorValue)
	}
	forgotten, errorValue := store.Forget(context.Background(), engineeringReader, []string{result.Facts[0].Fact.FactID}, "asked")
	if errorValue != nil || len(forgotten) != 1 {
		t.Fatalf("expected the containing-circle reader to forget the fact, got %v (%v)", forgotten, errorValue)
	}
}

func TestRenderTranscriptClampsLongSteps(t *testing.T) {
	transcript := bluememo.RenderTranscript(bluememo.Transcript{
		Prompt:  "p",
		Steps:   []bluememo.TranscriptStep{{Instruction: strings.Repeat("a", 5000), Status: "completed", Output: strings.Repeat("b", 5000)}},
		Result:  "r",
		Outcome: "completed",
	})
	if len([]rune(transcript)) > 2700 || !strings.Contains(transcript, "Final reply:\nr") || !strings.Contains(transcript, "Outcome: completed") {
		t.Fatalf("expected a clamped transcript with the reply, got %d runes", len([]rune(transcript)))
	}
}
