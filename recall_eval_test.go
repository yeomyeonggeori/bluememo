package bluememo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type evalCategory string

const evalIdentifierSeed = 20260921

const (
	evalCategoryTemporal   evalCategory = "temporal"
	evalCategoryPeriodic   evalCategory = "periodic"
	evalCategoryCrossNote  evalCategory = "cross_note"
	evalCategoryPreference evalCategory = "preference"
	evalCategoryUpdate     evalCategory = "update"
	evalCategorySingleNote evalCategory = "single_note"
)

var evalCategories = []evalCategory{evalCategoryTemporal, evalCategoryPeriodic, evalCategoryCrossNote, evalCategoryPreference, evalCategoryUpdate, evalCategorySingleNote}

type evalSplit string

const (
	evalSplitEvolve  evalSplit = "evolve"
	evalSplitHoldout evalSplit = "holdout"
)

var evalSplits = []evalSplit{evalSplitEvolve, evalSplitHoldout}

const (
	evalCasesPath            = "testdata/recall-eval.json"
	evalRecallDepth          = 10
	evalDefaultArrivalDate   = "2026-09-21"
	evalArrivalHour          = 9
	evalMinimumCasesPerGroup = 6

	evalEmbeddingURLVariable   = "BLUEMEMO_EVAL_EMBEDDING_URL"
	evalEmbeddingModelVariable = "BLUEMEMO_EVAL_EMBEDDING_MODEL"
	evalEmbeddingKeyVariable   = "BLUEMEMO_EVAL_EMBEDDING_KEY"
)

type evalNote struct {
	GroupID     string `json:"groupID"`
	SpeakerName string `json:"speakerName"`
	ArrivedOn   string `json:"arrivedOn"`
	Body        string `json:"body"`
}

type evalProposition struct {
	GroupID    string          `json:"groupID"`
	Content    string          `json:"content"`
	IsStatic   bool            `json:"isStatic"`
	OccurredOn string          `json:"occurredOn"`
	Expiry     bluememo.Expiry `json:"expiry"`
	ExpiryDate string          `json:"expiryDate"`
	Supersedes string          `json:"supersedes"`
}

type evalCase struct {
	ID           string            `json:"id"`
	Category     evalCategory      `json:"category"`
	Split        evalSplit         `json:"split"`
	Notes        []evalNote        `json:"notes"`
	Propositions []evalProposition `json:"propositions"`
	Question     string            `json:"question"`
	Expected     []string          `json:"expected"`
}

type evalFile struct {
	Cases []evalCase `json:"cases"`
}

type evalOutcome struct {
	testCase evalCase
	recalled []string
}

type evalScore struct {
	caseCount      int
	recallAtOne    float64
	recallAtThree  float64
	reciprocalRank float64
}

func loadEvalCases(t *testing.T) []evalCase {
	t.Helper()
	content, errorValue := os.ReadFile(evalCasesPath)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var parsed evalFile
	if errorValue := decoder.Decode(&parsed); errorValue != nil {
		t.Fatalf("%s does not parse: %v", evalCasesPath, errorValue)
	}
	seen := map[string]bool{}
	for _, testCase := range parsed.Cases {
		if errorValue := validateEvalCase(testCase); errorValue != nil {
			t.Fatalf("case %q: %v", testCase.ID, errorValue)
		}
		if seen[testCase.ID] {
			t.Fatalf("case id %q appears twice", testCase.ID)
		}
		seen[testCase.ID] = true
	}
	return parsed.Cases
}

func validateEvalCase(testCase evalCase) error {
	if strings.TrimSpace(testCase.ID) == "" {
		return errors.New("a case needs an id")
	}
	if !slices.Contains(evalCategories, testCase.Category) {
		return fmt.Errorf("category %q is not one of %v", testCase.Category, evalCategories)
	}
	if !slices.Contains(evalSplits, testCase.Split) {
		return fmt.Errorf("split %q is not one of %v", testCase.Split, evalSplits)
	}
	if strings.TrimSpace(testCase.Question) == "" || len(testCase.Notes) == 0 || len(testCase.Expected) == 0 {
		return errors.New("a case needs notes, a question and expected sentences")
	}
	if errorValue := validateEvalNotes(testCase); errorValue != nil {
		return errorValue
	}
	return validateEvalPropositions(testCase)
}

func validateEvalNotes(testCase evalCase) error {
	for _, note := range testCase.Notes {
		if note.GroupID == "" || note.SpeakerName == "" || strings.TrimSpace(note.Body) == "" {
			return fmt.Errorf("note %+v needs a group, a speaker and a body", note)
		}
		if _, errorValue := time.Parse(time.DateOnly, note.ArrivedOn); errorValue != nil {
			return fmt.Errorf("note arrivedOn %q is not a date", note.ArrivedOn)
		}
	}
	return nil
}

func validateEvalPropositions(testCase evalCase) error {
	groupIDs := map[string]bool{}
	for _, note := range testCase.Notes {
		groupIDs[note.GroupID] = true
	}
	held := []string{}
	for _, proposition := range testCase.Propositions {
		if !groupIDs[proposition.GroupID] {
			return fmt.Errorf("proposition %q names group %q, which has no note", proposition.Content, proposition.GroupID)
		}
		if !slices.Contains(bluememo.Expiries, proposition.Expiry) {
			return fmt.Errorf("proposition %q has undeclared expiry %q", proposition.Content, proposition.Expiry)
		}
		if proposition.Supersedes != "" && !slices.Contains(held, proposition.Supersedes) {
			return fmt.Errorf("proposition %q supersedes %q, which no earlier proposition says", proposition.Content, proposition.Supersedes)
		}
		held = append(held, proposition.Content)
	}
	for _, expected := range testCase.Expected {
		if !slices.Contains(held, expected) {
			return fmt.Errorf("expected sentence %q is not a proposition of the case", expected)
		}
	}
	return nil
}

type supersedingJudge struct {
	supersedes map[string]string
}

func (judge supersedingJudge) Judge(_ context.Context, proposition string, candidates []string) (bluememo.Judgement, error) {
	target, isSuperseding := judge.supersedes[proposition]
	if !isSuperseding {
		return bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: bluememo.DefaultImportance}, nil
	}
	targetIndex := slices.Index(candidates, target)
	if targetIndex < 0 {
		return bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: bluememo.DefaultImportance}, nil
	}
	return bluememo.Judgement{Relation: bluememo.RelationUpdates, TargetIndex: targetIndex, Importance: bluememo.DefaultImportance}, nil
}

func arrivalInstant(date string) time.Time {
	day, _ := time.Parse(time.DateOnly, date)
	return day.Add(evalArrivalHour * time.Hour)
}

func groupIDsInOrder(notes []evalNote) []string {
	groupIDs := []string{}
	for _, note := range notes {
		if !slices.Contains(groupIDs, note.GroupID) {
			groupIDs = append(groupIDs, note.GroupID)
		}
	}
	return groupIDs
}

func propositionsOfGroup(testCase evalCase, groupID string) []bluememo.Proposition {
	propositions := []bluememo.Proposition{}
	for _, proposition := range testCase.Propositions {
		if proposition.GroupID == groupID {
			propositions = append(propositions, bluememo.Proposition{
				Content:    proposition.Content,
				IsStatic:   proposition.IsStatic,
				OccurredOn: proposition.OccurredOn,
				Expiry:     proposition.Expiry,
				ExpiryDate: proposition.ExpiryDate,
			})
		}
	}
	return propositions
}

func supersessionsOf(testCase evalCase) map[string]string {
	supersedes := map[string]string{}
	for _, proposition := range testCase.Propositions {
		if proposition.Supersedes != "" {
			supersedes[proposition.Content] = proposition.Supersedes
		}
	}
	return supersedes
}

func seededIdentifiers() func() string {
	source := rand.New(rand.NewSource(evalIdentifierSeed))
	return func() string {
		return fmt.Sprintf("%016x%016x", source.Uint64(), source.Uint64())
	}
}

func runEvalCase(t *testing.T, embedder bluememo.Embedder, embeddingModel string, testCase evalCase) []string {
	t.Helper()
	ctx := context.Background()
	testClock := &clock{current: arrivalInstant(evalDefaultArrivalDate)}
	model := bluememotest.NewScriptedModel()
	store, errorValue := bluememo.Open(ctx, filepath.Join(t.TempDir(), "memory.db"), bluememo.Configuration{
		Embedder:       embedder,
		EmbeddingModel: embeddingModel,
		Model:          model,
		Judge:          supersedingJudge{supersedes: supersessionsOf(testCase)},
		Now:            testClock.now,
		NewIdentifier:  seededIdentifiers(),
	})
	if errorValue != nil {
		t.Fatalf("%s: open store: %v", testCase.ID, errorValue)
	}
	defer store.Close()
	for _, groupID := range groupIDsInOrder(testCase.Notes) {
		memorizeAndSettleGroup(t, store, model, testClock, testCase, groupID)
	}
	result, errorValue := store.Recall(ctx, testCase.Question, evalRecallDepth)
	if errorValue != nil {
		t.Fatalf("%s: recall: %v", testCase.ID, errorValue)
	}
	return recalledContents(result)
}

func memorizeAndSettleGroup(t *testing.T, store *bluememo.Store, model *bluememotest.ScriptedModel, testClock *clock, testCase evalCase, groupID string) {
	t.Helper()
	ctx := context.Background()
	model.QueueDecomposition(propositionsOfGroup(testCase, groupID)...)
	for _, note := range testCase.Notes {
		if note.GroupID != groupID {
			continue
		}
		testClock.current = arrivalInstant(note.ArrivedOn)
		errorValue := store.Memorize(ctx, bluememo.Note{GroupID: note.GroupID, Body: note.Body, SpeakerName: note.SpeakerName})
		if errorValue != nil {
			t.Fatalf("%s: memorize: %v", testCase.ID, errorValue)
		}
	}
	report, errorValue := store.Settle(ctx)
	if errorValue != nil {
		t.Fatalf("%s: settle: %v", testCase.ID, errorValue)
	}
	if report.Rejected != 0 {
		t.Fatalf("%s: settle rejected %d propositions", testCase.ID, report.Rejected)
	}
}

func runEval(t *testing.T, embedder bluememo.Embedder, embeddingModel string) []evalOutcome {
	t.Helper()
	cases := loadEvalCases(t)
	outcomes := make([]evalOutcome, 0, len(cases))
	for _, testCase := range cases {
		outcomes = append(outcomes, evalOutcome{testCase: testCase, recalled: runEvalCase(t, embedder, embeddingModel, testCase)})
	}
	return outcomes
}

func expectedRanks(expected []string, recalled []string) []int {
	ranks := make([]int, len(expected))
	for index, sentence := range expected {
		ranks[index] = slices.Index(recalled, sentence) + 1
	}
	return ranks
}

func recallAtDepth(ranks []int, depth int) float64 {
	found := 0
	for _, rank := range ranks {
		if rank > 0 && rank <= depth {
			found++
		}
	}
	return float64(found) / float64(len(ranks))
}

func reciprocalRankOf(ranks []int) float64 {
	best := 0
	for _, rank := range ranks {
		if rank > 0 && (best == 0 || rank < best) {
			best = rank
		}
	}
	if best == 0 {
		return 0
	}
	return 1 / float64(best)
}

func scoreOutcomes(outcomes []evalOutcome) evalScore {
	score := evalScore{caseCount: len(outcomes)}
	if len(outcomes) == 0 {
		return score
	}
	for _, outcome := range outcomes {
		ranks := expectedRanks(outcome.testCase.Expected, outcome.recalled)
		score.recallAtOne += recallAtDepth(ranks, 1)
		score.recallAtThree += recallAtDepth(ranks, 3)
		score.reciprocalRank += reciprocalRankOf(ranks)
	}
	count := float64(len(outcomes))
	score.recallAtOne /= count
	score.recallAtThree /= count
	score.reciprocalRank /= count
	return score
}

func outcomesWhere(outcomes []evalOutcome, keep func(evalCase) bool) []evalOutcome {
	kept := []evalOutcome{}
	for _, outcome := range outcomes {
		if keep(outcome.testCase) {
			kept = append(kept, outcome)
		}
	}
	return kept
}

func formatScoreTable(outcomes []evalOutcome) string {
	var table bytes.Buffer
	writer := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	writeScoreRow := func(label string, selected []evalOutcome) {
		score := scoreOutcomes(selected)
		fmt.Fprintf(writer, "%s\t%d\t%.3f\t%.3f\t%.3f\n", label, score.caseCount, score.recallAtOne, score.recallAtThree, score.reciprocalRank)
	}
	fmt.Fprintln(writer, "group\tcases\trecall@1\trecall@3\tMRR")
	for _, category := range evalCategories {
		writeScoreRow("category "+string(category), outcomesWhere(outcomes, func(testCase evalCase) bool { return testCase.Category == category }))
	}
	for _, split := range evalSplits {
		writeScoreRow("split "+string(split), outcomesWhere(outcomes, func(testCase evalCase) bool { return testCase.Split == split }))
	}
	writeScoreRow("overall", outcomes)
	writer.Flush()
	return "\n" + table.String()
}

func formatCaseTable(outcomes []evalOutcome) string {
	var table bytes.Buffer
	writer := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "case\tsplit\trank of each expected sentence (0 = not recalled)")
	for _, outcome := range outcomes {
		fmt.Fprintf(writer, "%s\t%s\t%v\n", outcome.testCase.ID, outcome.testCase.Split, expectedRanks(outcome.testCase.Expected, outcome.recalled))
	}
	writer.Flush()
	return "\n" + table.String()
}

type httpEmbedder struct {
	url    string
	model  string
	apiKey string
	client *http.Client
}

func (embedder httpEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	embeddings, errorValue := embedder.EmbedDocuments(ctx, []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	return embeddings[0], nil
}

func (embedder httpEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	requestBody, errorValue := json.Marshal(map[string]any{"model": embedder.model, "input": texts})
	if errorValue != nil {
		return nil, errorValue
	}
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, embedder.url, bytes.NewReader(requestBody))
	if errorValue != nil {
		return nil, errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	if embedder.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+embedder.apiKey)
	}
	response, errorValue := embedder.client.Do(request)
	if errorValue != nil {
		return nil, errorValue
	}
	defer response.Body.Close()
	responseBody, errorValue := io.ReadAll(response.Body)
	if errorValue != nil {
		return nil, errorValue
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding endpoint returned %d: %s", response.StatusCode, responseBody)
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return nil, fmt.Errorf("embedding response is not the expected shape: %w", errorValue)
	}
	embeddings := make([][]float32, len(parsed.Data))
	for index, entry := range parsed.Data {
		embeddings[index] = entry.Embedding
	}
	return embeddings, nil
}

func TestEvalCasesParseAndCarryOnlyDeclaredValues(t *testing.T) {
	cases := loadEvalCases(t)
	perCategory := map[evalCategory]int{}
	perSplit := map[evalSplit]int{}
	for _, testCase := range cases {
		perCategory[testCase.Category]++
		perSplit[testCase.Split]++
	}
	for _, category := range evalCategories {
		if perCategory[category] < evalMinimumCasesPerGroup {
			t.Errorf("category %s has %d cases, want at least %d", category, perCategory[category], evalMinimumCasesPerGroup)
		}
	}
	for _, split := range evalSplits {
		if perSplit[split] == 0 {
			t.Errorf("split %s has no cases", split)
		}
	}
}

func TestEvalRefusesAnUnknownCategoryOrSplit(t *testing.T) {
	valid := evalCase{
		ID:           "guard",
		Category:     evalCategoryUpdate,
		Split:        evalSplitHoldout,
		Notes:        []evalNote{{GroupID: "guard-g1", SpeakerName: "이샘플", ArrivedOn: evalDefaultArrivalDate, Body: "서울에 산다"}},
		Propositions: []evalProposition{{GroupID: "guard-g1", Content: "이샘플은 서울에 산다.", Expiry: bluememo.ExpiryNone}},
		Question:     "이샘플은 어디 살아?",
		Expected:     []string{"이샘플은 서울에 산다."},
	}
	if errorValue := validateEvalCase(valid); errorValue != nil {
		t.Fatalf("a well-formed case was refused: %v", errorValue)
	}
	unknownCategory := valid
	unknownCategory.Category = "temporal_ish"
	unknownSplit := valid
	unknownSplit.Split = "train"
	unlisted := valid
	unlisted.Expected = []string{"이샘플은 부산에 산다."}
	for name, refused := range map[string]evalCase{"category": unknownCategory, "split": unknownSplit, "expected sentence": unlisted} {
		if validateEvalCase(refused) == nil {
			t.Errorf("a case with an unknown %s was accepted", name)
		}
	}
}

func TestEvalScoringIsComputedFromRanks(t *testing.T) {
	recalled := []string{"가", "나", "다", "라"}
	cases := map[string]struct {
		expected       []string
		recallAtOne    float64
		recallAtThree  float64
		reciprocalRank float64
	}{
		"first":         {[]string{"가"}, 1, 1, 1},
		"third":         {[]string{"다"}, 0, 1, 1.0 / 3},
		"fourth":        {[]string{"라"}, 0, 0, 0.25},
		"absent":        {[]string{"마"}, 0, 0, 0},
		"first of many": {[]string{"가", "라"}, 0.5, 0.5, 1},
	}
	for name, want := range cases {
		ranks := expectedRanks(want.expected, recalled)
		if got := recallAtDepth(ranks, 1); got != want.recallAtOne {
			t.Errorf("%s: recall@1 %v, want %v", name, got, want.recallAtOne)
		}
		if got := recallAtDepth(ranks, 3); got != want.recallAtThree {
			t.Errorf("%s: recall@3 %v, want %v", name, got, want.recallAtThree)
		}
		if got := reciprocalRankOf(ranks); got != want.reciprocalRank {
			t.Errorf("%s: reciprocal rank %v, want %v", name, got, want.reciprocalRank)
		}
	}
}

func TestEvalHarnessRunsEveryCaseWithTheDeterministicStack(t *testing.T) {
	outcomes := runEval(t, bluememotest.HashEmbedder{}, "hash")
	if len(outcomes) != len(loadEvalCases(t)) {
		t.Fatalf("scored %d outcomes for %d cases", len(outcomes), len(loadEvalCases(t)))
	}
	for _, outcome := range outcomes {
		if len(outcome.recalled) == 0 {
			t.Errorf("%s recalled nothing, so the seeding did not reach the store", outcome.testCase.ID)
		}
	}
	overall := scoreOutcomes(outcomes)
	for name, value := range map[string]float64{"recall@1": overall.recallAtOne, "recall@3": overall.recallAtThree, "MRR": overall.reciprocalRank} {
		if value < 0 || value > 1 {
			t.Errorf("%s is %v, outside [0, 1]", name, value)
		}
	}
	if overall.recallAtThree < overall.recallAtOne {
		t.Errorf("recall@3 %v is below recall@1 %v", overall.recallAtThree, overall.recallAtOne)
	}
	t.Log("deterministic hash embedder; the numbers check the harness, not recall quality" + formatScoreTable(outcomes))
}

func TestRecallQualityWithARealEmbedder(t *testing.T) {
	endpoint := os.Getenv(evalEmbeddingURLVariable)
	embeddingModel := os.Getenv(evalEmbeddingModelVariable)
	if endpoint == "" || embeddingModel == "" {
		t.Skipf("set %s and %s (and optionally %s) to score recall with a real embedder", evalEmbeddingURLVariable, evalEmbeddingModelVariable, evalEmbeddingKeyVariable)
	}
	embedder := httpEmbedder{url: endpoint, model: embeddingModel, apiKey: os.Getenv(evalEmbeddingKeyVariable), client: &http.Client{Timeout: time.Minute}}
	outcomes := runEval(t, embedder, embeddingModel)
	t.Log("embedder " + embeddingModel + formatScoreTable(outcomes) + formatCaseTable(outcomes))
}
