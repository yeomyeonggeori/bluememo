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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type evalCategory string

const evalIdentifierSeed = 20260921

const (
	evalCategoryTemporal     evalCategory = "temporal"
	evalCategoryPeriodic     evalCategory = "periodic"
	evalCategoryCrossNote    evalCategory = "cross_note"
	evalCategoryPreference   evalCategory = "preference"
	evalCategoryUpdate       evalCategory = "update"
	evalCategorySingleNote   evalCategory = "single_note"
	evalCategoryMetadataDate evalCategory = "metadata_date"
)

var evalCategories = []evalCategory{evalCategoryTemporal, evalCategoryPeriodic, evalCategoryCrossNote, evalCategoryPreference, evalCategoryUpdate, evalCategorySingleNote, evalCategoryMetadataDate}

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
	evalTimeReferenceVariable  = "BLUEMEMO_EVAL_TIME_REFERENCE"
	evalRerankURLVariable      = "BLUEMEMO_EVAL_RERANK_URL"
	evalRerankModelVariable    = "BLUEMEMO_EVAL_RERANK_MODEL"
	evalRerankKeyVariable      = "BLUEMEMO_EVAL_RERANK_KEY"
	evalRerankModeVariable     = "BLUEMEMO_EVAL_RERANK_MODE"
	evalRerankWorkerCount      = 4
	evalHTTPAttemptLimit       = 5
	evalHTTPBackoffBase        = 500 * time.Millisecond
	evalJuliaRerankDepth       = 10
	evalChoiceCriteriaLimit    = 30

	evalJuliaCommandVariable  = "BLUEMEMO_EVAL_JULIA_COMMAND"
	evalJuliaModelDirVariable = "BLUEMEMO_EVAL_JULIA_MODEL_DIR"
	evalJuliaModeVariable     = "BLUEMEMO_EVAL_JULIA_MODE"
)

type evalJuliaMode string

const (
	evalJuliaModeListwise  evalJuliaMode = "listwise"
	evalJuliaModePointwise evalJuliaMode = "pointwise"
)

var evalJuliaModes = []evalJuliaMode{evalJuliaModeListwise, evalJuliaModePointwise}

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

func runEvalCase(t *testing.T, embedder bluememo.Embedder, reranker bluememo.Reranker, rerankDepth int, embeddingModel string, embedTimeReference bool, testCase evalCase) []string {
	t.Helper()
	ctx := context.Background()
	testClock := &clock{current: arrivalInstant(evalDefaultArrivalDate)}
	model := bluememotest.NewScriptedModel()
	store, errorValue := bluememo.Open(ctx, filepath.Join(t.TempDir(), "memory.db"), bluememo.Configuration{
		Embedder:           embedder,
		Reranker:           reranker,
		RerankDepth:        rerankDepth,
		EmbeddingModel:     embeddingModel,
		EmbedTimeReference: embedTimeReference,
		Model:              model,
		Judge:              supersedingJudge{supersedes: supersessionsOf(testCase)},
		Now:                testClock.now,
		NewIdentifier:      seededIdentifiers(),
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
	if strings.Contains(result.DegradedReason, bluememo.RerankFailureReason) {
		t.Errorf("%s: the live reranker failed, so this case is not a measurement of reranking: %s", testCase.ID, result.DegradedReason)
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

func runEval(t *testing.T, embedder bluememo.Embedder, reranker bluememo.Reranker, rerankDepth int, embeddingModel string, embedTimeReference bool) []evalOutcome {
	t.Helper()
	cases := loadEvalCases(t)
	outcomes := make([]evalOutcome, 0, len(cases))
	for _, testCase := range cases {
		outcomes = append(outcomes, evalOutcome{testCase: testCase, recalled: runEvalCase(t, embedder, reranker, rerankDepth, embeddingModel, embedTimeReference, testCase)})
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

func postJSONWithRetry(ctx context.Context, client *http.Client, url string, apiKey string, payload any) ([]byte, error) {
	requestBody, errorValue := json.Marshal(payload)
	if errorValue != nil {
		return nil, errorValue
	}
	var lastError error
	for attempt := range evalHTTPAttemptLimit {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(evalHTTPBackoffBase << (attempt - 1)):
			}
		}
		responseBody, isRetryable, errorValue := postJSONOnce(ctx, client, url, apiKey, requestBody)
		if errorValue == nil {
			return responseBody, nil
		}
		lastError = errorValue
		if !isRetryable {
			return nil, errorValue
		}
	}
	return nil, fmt.Errorf("gave up after %d attempts: %w", evalHTTPAttemptLimit, lastError)
}

func postJSONOnce(ctx context.Context, client *http.Client, url string, apiKey string, requestBody []byte) ([]byte, bool, error) {
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
	if errorValue != nil {
		return nil, false, errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, errorValue := client.Do(request)
	if errorValue != nil {
		return nil, true, errorValue
	}
	defer response.Body.Close()
	responseBody, errorValue := io.ReadAll(response.Body)
	if errorValue != nil {
		return nil, true, errorValue
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return nil, true, fmt.Errorf("endpoint returned %d: %s", response.StatusCode, responseBody)
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("endpoint returned %d: %s", response.StatusCode, responseBody)
	}
	return responseBody, false, nil
}

func (embedder httpEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	responseBody, errorValue := postJSONWithRetry(ctx, embedder.client, embedder.url, embedder.apiKey,
		map[string]any{"model": embedder.model, "input": texts})
	if errorValue != nil {
		return nil, errorValue
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
	outcomes := runEval(t, bluememotest.HashEmbedder{}, nil, 0, "hash", false)
	t.Log("time reference on" + formatScoreTable(runEval(t, bluememotest.HashEmbedder{}, nil, 0, "hash", true)))
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

type evalRerankMode string

const (
	evalRerankModePointwise evalRerankMode = "pointwise"
	evalRerankModeListwise  evalRerankMode = "listwise"
)

var evalRerankModes = []evalRerankMode{evalRerankModePointwise, evalRerankModeListwise}

type decisionsReranker struct {
	url    string
	model  string
	apiKey string
	mode   evalRerankMode
	client *http.Client
	ledger *rerankCostLedger
}

type rerankCostLedger struct {
	mutex sync.Mutex
	total float64
	calls int
}

func (ledger *rerankCostLedger) record(cost float64) {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()
	ledger.total += cost
	ledger.calls++
}

type decisionsResponse struct {
	Answers map[string]struct {
		Noul          *float64           `json:"noul"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
	Usage struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

func (reranker decisionsReranker) Rerank(ctx context.Context, query string, contents []string) ([]float64, error) {
	switch reranker.mode {
	case evalRerankModePointwise:
		return reranker.rerankPointwise(ctx, query, contents)
	case evalRerankModeListwise:
		return reranker.rerankListwise(ctx, query, contents)
	}
	return nil, fmt.Errorf("unknown rerank mode %q", reranker.mode)
}

func (reranker decisionsReranker) rerankPointwise(ctx context.Context, query string, contents []string) ([]float64, error) {
	scores := make([]float64, len(contents))
	errorsByIndex := make([]error, len(contents))
	indexes := make(chan int)
	var workers sync.WaitGroup
	for range min(evalRerankWorkerCount, len(contents)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indexes {
				scores[index], errorsByIndex[index] = reranker.scorePointwise(ctx, query, contents[index])
			}
		}()
	}
	for index := range contents {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	if errorValue := errors.Join(errorsByIndex...); errorValue != nil {
		return nil, errorValue
	}
	return scores, nil
}

func (reranker decisionsReranker) scorePointwise(ctx context.Context, query string, content string) (float64, error) {
	parsed, responseBody, errorValue := reranker.decide(ctx, content, map[string]any{
		"relevant": map[string]any{
			"type":         "noul",
			"instructions": "Does this memory answer the question: " + query,
		},
	})
	if errorValue != nil {
		return 0, errorValue
	}
	answer, isPresent := parsed.Answers["relevant"]
	if !isPresent || answer.Noul == nil {
		return 0, fmt.Errorf("rerank response carries no relevant score: %s", responseBody)
	}
	return *answer.Noul, nil
}

func (reranker decisionsReranker) rerankListwise(ctx context.Context, query string, contents []string) ([]float64, error) {
	if len(contents) > evalChoiceCriteriaLimit {
		return nil, fmt.Errorf("a choice question takes at most %d criteria and this shortlist holds %d; set RerankDepth to the reranker's capacity", evalChoiceCriteriaLimit, len(contents))
	}
	criteria := make(map[string]string, len(contents))
	for index, content := range contents {
		criteria[fmt.Sprintf("m%d", index)] = content
	}
	parsed, responseBody, errorValue := reranker.decide(ctx, query, map[string]any{
		"pick": map[string]any{
			"type":         "choice",
			"instructions": "Which memory answers this question?",
			"criteria":     criteria,
		},
	})
	if errorValue != nil {
		return nil, errorValue
	}
	answer, isPresent := parsed.Answers["pick"]
	if !isPresent {
		return nil, fmt.Errorf("rerank response carries no pick answer: %s", responseBody)
	}
	scores := make([]float64, len(contents))
	for index := range scores {
		score, isPresent := answer.Probabilities[fmt.Sprintf("m%d", index)]
		if !isPresent {
			return nil, fmt.Errorf("rerank response has no probability for m%d: %s", index, responseBody)
		}
		scores[index] = score
	}
	return scores, nil
}

func (reranker decisionsReranker) decide(ctx context.Context, state string, questions map[string]any) (decisionsResponse, []byte, error) {
	responseBody, errorValue := postJSONWithRetry(ctx, reranker.client, reranker.url, reranker.apiKey, map[string]any{
		"model":     reranker.model,
		"state":     state,
		"questions": questions,
	})
	if errorValue != nil {
		return decisionsResponse{}, nil, errorValue
	}
	var parsed decisionsResponse
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return decisionsResponse{}, nil, fmt.Errorf("rerank response is not the expected shape: %w", errorValue)
	}
	reranker.ledger.record(parsed.Usage.Cost)
	return parsed, responseBody, nil
}

type juliaReranker struct {
	command  string
	modelDir string
	mode     evalJuliaMode
}

func (reranker juliaReranker) Rerank(ctx context.Context, query string, contents []string) ([]float64, error) {
	switch reranker.mode {
	case evalJuliaModeListwise:
		return reranker.rerankListwise(ctx, query, contents)
	case evalJuliaModePointwise:
		return reranker.rerankPointwise(ctx, query, contents)
	}
	return nil, fmt.Errorf("unknown julia mode %q", reranker.mode)
}

func (reranker juliaReranker) run(ctx context.Context, arguments []string) (string, error) {
	command := exec.CommandContext(ctx, reranker.command, append([]string{"decide", "--model-dir", reranker.modelDir}, arguments...)...)
	var standardError bytes.Buffer
	command.Stderr = &standardError
	output, errorValue := command.Output()
	if errorValue != nil {
		return "", fmt.Errorf("julia decide failed: %w: %s", errorValue, standardError.String())
	}
	return string(output), nil
}

func (reranker juliaReranker) rerankListwise(ctx context.Context, query string, contents []string) ([]float64, error) {
	arguments := []string{"--state", query, "--question", "Which memory answers this question?", "--type", "choice"}
	for index, content := range contents {
		arguments = append(arguments, "--option", fmt.Sprintf("m%d=%s", index, content))
	}
	output, errorValue := reranker.run(ctx, append(arguments, "--probabilities"))
	if errorValue != nil {
		return nil, errorValue
	}
	return parseListwiseScores(output, len(contents))
}

var listwiseScorePattern = regexp.MustCompile(`\b(m\d+): ([0-9.eE+-]+)`)

func parseListwiseScores(output string, count int) ([]float64, error) {
	_, probabilities, isPresent := strings.Cut(output, "(")
	if !isPresent {
		return nil, fmt.Errorf("julia listwise output carries no probabilities: %q", output)
	}
	scoresByName := map[string]float64{}
	for _, match := range listwiseScorePattern.FindAllStringSubmatch(probabilities, -1) {
		score, errorValue := strconv.ParseFloat(match[2], 64)
		if errorValue != nil {
			return nil, fmt.Errorf("julia listwise probability %q: %w", match[2], errorValue)
		}
		scoresByName[match[1]] = score
	}
	scores := make([]float64, count)
	for index := range scores {
		score, isPresent := scoresByName[fmt.Sprintf("m%d", index)]
		if !isPresent {
			return nil, fmt.Errorf("julia listwise output has no probability for m%d: %q", index, output)
		}
		scores[index] = score
	}
	return scores, nil
}

func (reranker juliaReranker) rerankPointwise(ctx context.Context, query string, contents []string) ([]float64, error) {
	questions := make(map[string]any, len(contents))
	for index, content := range contents {
		questions[fmt.Sprintf("c%d", index)] = map[string]any{
			"type":         "noul",
			"instructions": "Does this memory answer the question? Memory: " + content,
		}
	}
	requestBody, errorValue := json.Marshal(map[string]any{"state": query, "questions": questions})
	if errorValue != nil {
		return nil, errorValue
	}
	inputPath := filepath.Join(os.TempDir(), fmt.Sprintf("bluememo-julia-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	if errorValue := os.WriteFile(inputPath, requestBody, 0o600); errorValue != nil {
		return nil, errorValue
	}
	defer os.Remove(inputPath)
	output, errorValue := reranker.run(ctx, []string{"--input", inputPath, "--probabilities"})
	if errorValue != nil {
		return nil, errorValue
	}
	return parsePointwiseScores(output, len(contents))
}

func parsePointwiseScores(output string, count int) ([]float64, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != count {
		return nil, fmt.Errorf("julia pointwise printed %d lines for %d questions: %q", len(lines), count, output)
	}
	scores := make([]float64, count)
	for index, line := range lines {
		leading, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		score, errorValue := strconv.ParseFloat(leading, 64)
		if errorValue != nil {
			return nil, fmt.Errorf("julia pointwise line %q: %w", line, errorValue)
		}
		scores[index] = score
	}
	return scores, nil
}

func newJuliaReranker(t *testing.T) (bluememo.Reranker, string) {
	t.Helper()
	command := os.Getenv(evalJuliaCommandVariable)
	if command == "" {
		return nil, ""
	}
	mode := evalJuliaMode(os.Getenv(evalJuliaModeVariable))
	if !slices.Contains(evalJuliaModes, mode) {
		t.Fatalf("%s is %q, want one of %v", evalJuliaModeVariable, mode, evalJuliaModes)
	}
	modelDir := os.Getenv(evalJuliaModelDirVariable)
	if modelDir == "" {
		t.Fatalf("%s is required when %s is set", evalJuliaModelDirVariable, evalJuliaCommandVariable)
	}
	return juliaReranker{command: command, modelDir: modelDir, mode: mode}, "julia " + string(mode)
}

func TestRecallQualityWithARealEmbedder(t *testing.T) {
	endpoint := os.Getenv(evalEmbeddingURLVariable)
	embeddingModel := os.Getenv(evalEmbeddingModelVariable)
	if endpoint == "" || embeddingModel == "" {
		t.Skipf("set %s and %s (and optionally %s) to score recall with a real embedder", evalEmbeddingURLVariable, evalEmbeddingModelVariable, evalEmbeddingKeyVariable)
	}
	embedder := httpEmbedder{url: endpoint, model: embeddingModel, apiKey: os.Getenv(evalEmbeddingKeyVariable), client: &http.Client{Timeout: time.Minute}}
	components := "embedder " + embeddingModel + ", time reference \"" + os.Getenv(evalTimeReferenceVariable) + "\""
	var reranker bluememo.Reranker
	var rerankLedger *rerankCostLedger
	rerankDepth := 0
	rerankURL, rerankModel := os.Getenv(evalRerankURLVariable), os.Getenv(evalRerankModelVariable)
	if rerankURL != "" && rerankModel != "" {
		rerankMode := evalRerankModeFromEnvironment(t)
		rerankLedger = &rerankCostLedger{}
		reranker = decisionsReranker{url: rerankURL, model: rerankModel, apiKey: os.Getenv(evalRerankKeyVariable), mode: rerankMode, client: &http.Client{Timeout: time.Minute}, ledger: rerankLedger}
		rerankDepth = evalChoiceCriteriaLimit
		components += ", reranker " + rerankModel + " " + string(rerankMode)
	}
	if juliaReranker, name := newJuliaReranker(t); juliaReranker != nil {
		reranker = juliaReranker
		rerankDepth = evalJuliaRerankDepth
		components += ", reranker " + name
	}
	outcomes := runEval(t, embedder, reranker, rerankDepth, embeddingModel, os.Getenv(evalTimeReferenceVariable) != "")
	t.Log("live components: " + components + formatScoreTable(outcomes) + formatCaseTable(outcomes))
	if rerankLedger != nil {
		t.Logf("rerank usage: %d calls, cost %.6f", rerankLedger.calls, rerankLedger.total)
	}
}

func evalRerankModeFromEnvironment(t *testing.T) evalRerankMode {
	t.Helper()
	mode := evalRerankMode(os.Getenv(evalRerankModeVariable))
	if mode == "" {
		return evalRerankModePointwise
	}
	if !slices.Contains(evalRerankModes, mode) {
		t.Fatalf("%s is %q, want one of %v", evalRerankModeVariable, mode, evalRerankModes)
	}
	return mode
}
