package bluememo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/yeomyeonggeori/bluememo/openrouter"
)

const (
	streamFactsPath         = "testdata/dedup-stream.json"
	streamStatementsPerFact = 5
	streamStepInterval      = 7 * 24 * time.Hour
	streamSpeakerName       = "이샘플"
)

type streamFact struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Anchors    [][]string `json:"anchors"`
	Statements []string   `json:"statements"`
}

type streamFile struct {
	Facts []streamFact `json:"facts"`
}

func loadStreamFacts(t *testing.T) []streamFact {
	t.Helper()
	content, errorValue := os.ReadFile(streamFactsPath)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var parsed streamFile
	if errorValue := decoder.Decode(&parsed); errorValue != nil {
		t.Fatalf("%s does not parse: %v", streamFactsPath, errorValue)
	}
	seen := map[string]bool{}
	for _, fact := range parsed.Facts {
		if errorValue := validateStreamFact(fact); errorValue != nil {
			t.Fatalf("fact %q: %v", fact.ID, errorValue)
		}
		if seen[fact.ID] {
			t.Fatalf("fact id %q appears twice", fact.ID)
		}
		seen[fact.ID] = true
	}
	return parsed.Facts
}

func validateStreamFact(fact streamFact) error {
	if strings.TrimSpace(fact.ID) == "" || strings.TrimSpace(fact.Question) == "" {
		return errors.New("a fact needs an id and a question")
	}
	if len(fact.Statements) != streamStatementsPerFact {
		return fmt.Errorf("a fact carries %d statements, this one carries %d", streamStatementsPerFact, len(fact.Statements))
	}
	if len(fact.Anchors) == 0 {
		return errors.New("a fact names the values every rewording must keep")
	}
	seen := map[string]bool{}
	for _, statement := range fact.Statements {
		if strings.TrimSpace(statement) == "" || seen[statement] {
			return fmt.Errorf("statement %q is empty or repeated", statement)
		}
		seen[statement] = true
		if errorValue := validateStreamAnchors(fact.Anchors, statement); errorValue != nil {
			return errorValue
		}
	}
	return nil
}

func validateStreamAnchors(anchors [][]string, statement string) error {
	for _, alternatives := range anchors {
		isKept := slices.ContainsFunc(alternatives, func(alternative string) bool { return strings.Contains(statement, alternative) })
		if !isKept {
			return fmt.Errorf("statement %q drops one of %v", statement, alternatives)
		}
	}
	return nil
}

type recordingJudge struct {
	inner    bluememo.DistributionJudge
	trace    *judgeTrace
	outcomes []judgeOutcome
}

func newRecordingJudge(chooser bluememo.Chooser) *recordingJudge {
	judge := &recordingJudge{trace: &judgeTrace{}}
	judge.choose(chooser)
	return judge
}

func (judge *recordingJudge) choose(chooser bluememo.Chooser) {
	judge.inner = bluememo.DistributionJudge{Chooser: tracingChooser{inner: chooser, trace: judge.trace}}
}

func (judge *recordingJudge) Judge(ctx context.Context, proposition string, candidates []string) (bluememo.Judgement, error) {
	*judge.trace = judgeTrace{}
	judgement, errorValue := judge.inner.Judge(ctx, proposition, candidates)
	if errorValue != nil {
		return bluememo.Judgement{}, errorValue
	}
	judge.outcomes = append(judge.outcomes, judgeOutcome{judgement: judgement, trace: *judge.trace})
	return judgement, nil
}

func (judge *recordingJudge) lastOutcome() judgeOutcome {
	return judge.outcomes[len(judge.outcomes)-1]
}

type streamRig struct {
	store *bluememo.Store
	model *bluememotest.ScriptedModel
	clock *clock
	judge *recordingJudge
}

func openStreamRig(t *testing.T, embedder bluememo.Embedder, embeddingModel string, chooser bluememo.Chooser) *streamRig {
	t.Helper()
	rig := &streamRig{
		model: bluememotest.NewScriptedModel(),
		clock: &clock{current: arrivalInstant(evalDefaultArrivalDate)},
		judge: newRecordingJudge(chooser),
	}
	store, errorValue := bluememo.Open(context.Background(), filepath.Join(t.TempDir(), "memory.db"), bluememo.Configuration{
		Embedder:       embedder,
		EmbeddingModel: embeddingModel,
		Model:          rig.model,
		Judge:          rig.judge,
		Now:            rig.clock.now,
	})
	if errorValue != nil {
		t.Fatalf("open store: %v", errorValue)
	}
	t.Cleanup(func() { store.Close() })
	rig.store = store
	return rig
}

func (rig *streamRig) settleStatement(t *testing.T, groupID string, text string) {
	t.Helper()
	ctx := context.Background()
	rig.model.QueueDecomposition(statement(text))
	if errorValue := rig.store.Memorize(ctx, bluememo.Note{GroupID: groupID, Body: text, SpeakerName: streamSpeakerName}); errorValue != nil {
		t.Fatalf("memorize %q: %v", text, errorValue)
	}
	report, errorValue := rig.store.Settle(ctx)
	if errorValue != nil {
		t.Fatalf("settle %q: %v", text, errorValue)
	}
	if report.Rejected != 0 || report.Groups != 1 {
		t.Fatalf("settle %q: unexpected report %+v", text, report)
	}
	rig.clock.advance(streamStepInterval)
}

func (rig *streamRig) liveMemories(t *testing.T) []bluememo.Memory {
	t.Helper()
	memories, errorValue := rig.store.Memories(context.Background())
	if errorValue != nil {
		t.Fatalf("list memories: %v", errorValue)
	}
	return memories
}

type streamStep struct {
	statement string
	outcome   judgeOutcome
	liveCount int
	strengths []float64
}

type streamRun struct {
	fact  streamFact
	steps []streamStep
	live  []bluememo.Memory
}

func strengthsOf(memories []bluememo.Memory) []float64 {
	values := make([]float64, len(memories))
	for index, memory := range memories {
		values[index] = memory.StorageStrength
	}
	slices.Sort(values)
	return values
}

func runStream(t *testing.T, fact streamFact, embedder bluememo.Embedder, embeddingModel string, chooser bluememo.Chooser) streamRun {
	t.Helper()
	rig := openStreamRig(t, embedder, embeddingModel, chooser)
	run := streamRun{fact: fact}
	for index, text := range fact.Statements {
		rig.settleStatement(t, fmt.Sprintf("%s-g%d", fact.ID, index+1), text)
		live := rig.liveMemories(t)
		run.steps = append(run.steps, streamStep{statement: text, outcome: rig.judge.lastOutcome(), liveCount: len(live), strengths: strengthsOf(live)})
	}
	run.live = rig.liveMemories(t)
	return run
}

func (run streamRun) relations() []string {
	relations := make([]string, len(run.steps))
	for index, step := range run.steps {
		relations[index] = string(step.outcome.judgement.Relation)
	}
	return relations
}

func formatStreamSummaryTable(runs []streamRun) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\nfact\tlive of 5\trelation at each step\tsurvivor strength\tfresh strength\t")
	for _, run := range runs {
		fmt.Fprintf(writer, "%s\t%d\t%s\t%v\t%.1f\t\n", run.fact.ID, len(run.live), strings.Join(run.relations(), " > "), strengthsOf(run.live), bluememo.InitialStorageStrength)
	}
	writer.Flush()
	return buffer.String()
}

func formatStreamStepTable(runs []streamRun) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\nfact\tstep\tjudged\ttarget\traw leader\tmargin\tgate blocked\timportance\tlive\tstrengths\t")
	for _, run := range runs {
		for index, step := range run.steps {
			raw, margin, isKnown := step.outcome.rawRelation()
			rawLabel := "(no relation asked)"
			if isKnown {
				rawLabel = string(raw)
			}
			_, importanceMargin := leadingMargin(step.outcome.trace.importance)
			fmt.Fprintf(writer, "%s\t%d\t%s\t%d\t%s\t%.3f\t%t\t%d (%.2f)\t%d\t%v\t\n",
				run.fact.ID, index+1, step.outcome.judgement.Relation, step.outcome.judgement.TargetIndex, rawLabel, margin,
				step.outcome.gateFired(), step.outcome.judgement.Importance, importanceMargin, step.liveCount, step.strengths)
		}
	}
	writer.Flush()
	return buffer.String()
}

func runAllStreams(t *testing.T, embedder bluememo.Embedder, embeddingModel string, newChooser func() bluememo.Chooser) []streamRun {
	t.Helper()
	facts := loadStreamFacts(t)
	runs := make([]streamRun, 0, len(facts))
	for _, fact := range facts {
		runs = append(runs, runStream(t, fact, embedder, embeddingModel, newChooser()))
	}
	return runs
}

type recallCost struct {
	factID     string
	liveTotal  int
	liveOfFact int
	bestRank   int
	topThree   int
	recalled   []string
	relations  []string
}

func measureRecallCost(t *testing.T, fact streamFact, facts []streamFact, embedder bluememo.Embedder, embeddingModel string, subjectChooser bluememo.Chooser) recallCost {
	t.Helper()
	rig := openStreamRig(t, embedder, embeddingModel, unrelatedChooser())
	for _, other := range facts {
		if other.ID != fact.ID {
			rig.settleStatement(t, other.ID+"-background", other.Statements[0])
		}
	}
	rig.judge.choose(subjectChooser)
	backgroundJudgements := len(rig.judge.outcomes)
	for index, text := range fact.Statements {
		rig.settleStatement(t, fmt.Sprintf("%s-g%d", fact.ID, index+1), text)
	}
	result, errorValue := rig.store.Recall(context.Background(), fact.Question, evalRecallDepth)
	if errorValue != nil {
		t.Fatalf("recall %q: %v", fact.Question, errorValue)
	}
	cost := summarizeRecallCost(fact, rig.liveMemories(t), recalledContents(result))
	cost.relations = describeJudgements(rig.judge.outcomes[backgroundJudgements:])
	return cost
}

func describeJudgements(outcomes []judgeOutcome) []string {
	descriptions := make([]string, len(outcomes))
	for index, outcome := range outcomes {
		raw, margin, _ := outcome.rawRelation()
		descriptions[index] = fmt.Sprintf("%s(t%d,raw %s %.2f)", outcome.judgement.Relation, outcome.judgement.TargetIndex, raw, margin)
	}
	return descriptions
}

func summarizeRecallCost(fact streamFact, live []bluememo.Memory, recalled []string) recallCost {
	cost := recallCost{factID: fact.ID, liveTotal: len(live), recalled: recalled}
	for _, memory := range live {
		cost.liveOfFact += boolToCount(slices.Contains(fact.Statements, memory.Content))
	}
	for _, rank := range expectedRanks(fact.Statements, recalled) {
		if rank > 0 && (cost.bestRank == 0 || rank < cost.bestRank) {
			cost.bestRank = rank
		}
	}
	cost.topThree = int(recallAtDepth(expectedRanks(fact.Statements, recalled), 3) * streamStatementsPerFact)
	return cost
}

func formatRecallCostTable(arms map[string][]recallCost, armOrder []string) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\narm\tfact\tlive memories of the fact\tlive in store\trank of best correct memory (0 = none)\tof the 5 statements in top 3\trelation at each step\t")
	for _, arm := range armOrder {
		for _, cost := range arms[arm] {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t\n", arm, cost.factID, cost.liveOfFact, cost.liveTotal, cost.bestRank, cost.topThree, strings.Join(cost.relations, " > "))
		}
	}
	writer.Flush()
	return buffer.String()
}

func measureRecallArms(t *testing.T, embedder bluememo.Embedder, embeddingModel string, realChooser func(streamFact) bluememo.Chooser) string {
	t.Helper()
	facts := loadStreamFacts(t)
	arms := map[string][]recallCost{}
	armOrder := []string{"as settled", "accumulated"}
	for _, fact := range facts {
		arms["as settled"] = append(arms["as settled"], measureRecallCost(t, fact, facts, embedder, embeddingModel, realChooser(fact)))
		arms["accumulated"] = append(arms["accumulated"], measureRecallCost(t, fact, facts, embedder, embeddingModel, unrelatedChooser()))
	}
	return formatRecallCostTable(arms, armOrder)
}

func scriptedRelationChooser(answer string) bluememo.Chooser {
	return bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
		bluememo.ImportanceInstruction: {"3": 0.9},
		bluememo.RelationInstruction:   {answer: 0.9},
		bluememo.TargetInstruction:     {"0": 0.9},
	}}
}

type factAwareChooser struct {
	fact streamFact
}

func (chooser factAwareChooser) Choose(_ context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	heldIndex := openrouter.NoCandidateAnswer
	for _, candidate := range openrouter.SubjectCandidates(request.Subject) {
		if slices.Contains(chooser.fact.Statements, candidate.Gloss) {
			heldIndex = candidate.Key
			break
		}
	}
	switch request.Instruction {
	case bluememo.ImportanceInstruction:
		return map[string]float64{"3": 0.9}, nil
	case bluememo.RelationInstruction:
		if heldIndex == "9" {
			return map[string]float64{"4": 0.9}, nil
		}
		return map[string]float64{"1": 0.9}, nil
	}
	return map[string]float64{heldIndex: 0.9}, nil
}

func sameChooser() bluememo.Chooser {
	return scriptedRelationChooser("1")
}

func unrelatedChooser() bluememo.Chooser {
	return scriptedRelationChooser("4")
}

func TestDedupStreamFactsAreFiveRewordingsThatKeepTheirValues(t *testing.T) {
	facts := loadStreamFacts(t)
	if len(facts) < 5 {
		t.Fatalf("want at least 5 facts, got %d", len(facts))
	}
	valid := facts[0]
	mutations := map[string]func(streamFact) streamFact{
		"four statements": func(fact streamFact) streamFact { fact.Statements = fact.Statements[:4]; return fact },
		"a repeated wording": func(fact streamFact) streamFact {
			fact.Statements = slices.Clone(fact.Statements)
			fact.Statements[1] = fact.Statements[0]
			return fact
		},
		"a dropped value": func(fact streamFact) streamFact {
			fact.Statements = slices.Clone(fact.Statements)
			fact.Statements[2] = "이샘플은 매일 아침에 일어난다."
			return fact
		},
		"no anchors":  func(fact streamFact) streamFact { fact.Anchors = nil; return fact },
		"no question": func(fact streamFact) streamFact { fact.Question = " "; return fact },
	}
	for name, mutate := range mutations {
		if validateStreamFact(mutate(valid)) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestStreamCollapsesToOneMemoryWhenTheJudgeSaysSame(t *testing.T) {
	runs := runAllStreams(t, bluememotest.HashEmbedder{}, "hash", sameChooser)
	for _, run := range runs {
		if len(run.live) != 1 {
			t.Errorf("%s: %d live memories, want 1", run.fact.ID, len(run.live))
		}
		if run.steps[0].outcome.judgement.Relation != bluememo.RelationUnrelated {
			t.Errorf("%s: the first statement meets an empty store and is unrelated, got %s", run.fact.ID, run.steps[0].outcome.judgement.Relation)
		}
		for index, step := range run.steps[1:] {
			if step.outcome.judgement.Relation != bluememo.RelationSame {
				t.Errorf("%s step %d: relation %s, want same", run.fact.ID, index+2, step.outcome.judgement.Relation)
			}
			if step.strengths[0] <= run.steps[index].strengths[0] {
				t.Errorf("%s step %d: strength %v did not rise above %v", run.fact.ID, index+2, step.strengths, run.steps[index].strengths)
			}
		}
	}
	t.Log("scripted chooser that always says same; the numbers check the stream mechanics, not any model" + formatStreamSummaryTable(runs) + formatStreamStepTable(runs))
}

func TestStreamAccumulatesWhenTheJudgeNeverSaysSame(t *testing.T) {
	for _, run := range runAllStreams(t, bluememotest.HashEmbedder{}, "hash", unrelatedChooser) {
		if len(run.live) != streamStatementsPerFact {
			t.Errorf("%s: %d live memories, want %d", run.fact.ID, len(run.live), streamStatementsPerFact)
		}
	}
}

func TestStreamRecallCostIsCountedInBothArms(t *testing.T) {
	facts := loadStreamFacts(t)
	settled := measureRecallCost(t, facts[0], facts, bluememotest.HashEmbedder{}, "hash", factAwareChooser{fact: facts[0]})
	accumulated := measureRecallCost(t, facts[0], facts, bluememotest.HashEmbedder{}, "hash", unrelatedChooser())
	if settled.liveOfFact != 1 || accumulated.liveOfFact != streamStatementsPerFact {
		t.Fatalf("settled holds %d and accumulated holds %d of the fact, want 1 and %d", settled.liveOfFact, accumulated.liveOfFact, streamStatementsPerFact)
	}
	if settled.liveTotal != len(facts) || accumulated.liveTotal != len(facts)-1+streamStatementsPerFact {
		t.Fatalf("store sizes %d and %d do not match the background plus the fact", settled.liveTotal, accumulated.liveTotal)
	}
	if accumulated.topThree > 3 || settled.topThree > 1 {
		t.Fatalf("top-three counts %d and %d exceed what the arms hold", accumulated.topThree, settled.topThree)
	}
	t.Log("deterministic hash embedder; the numbers check the harness, not recall quality" + measureRecallArms(t, bluememotest.HashEmbedder{}, "hash", func(fact streamFact) bluememo.Chooser { return factAwareChooser{fact: fact} }))
}

func restatementCases(t *testing.T) []judgeCase {
	t.Helper()
	var selected []judgeCase
	for _, testCase := range loadJudgeCases(t) {
		if testCase.Kind == judgeKindRestatement {
			selected = append(selected, testCase)
		}
	}
	return selected
}

func formatRestatementTable(outcomes []judgeOutcome) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\ncase\tjudged\ttarget\ttarget wanted\traw leader\tmargin\tp(same)\tgate blocked a correct same\t")
	for _, outcome := range outcomes {
		raw, margin, _ := outcome.rawRelation()
		fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%s\t%.3f\t%.3f\t%t\t\n", outcome.testCase.ID, outcome.judgement.Relation, outcome.judgement.TargetIndex,
			outcome.testCase.Target, raw, margin, outcome.trace.relation["1"], outcome.gateCostACorrectAnswer())
	}
	writer.Flush()
	score := scoreJudgeOutcomes(outcomes)
	fmt.Fprintf(&buffer, "\naccuracy %d/%d  gate fired %d  gate pushed a correct same down to unrelated %d\nrelation margin: %s\n",
		score.correct, score.caseCount, score.gateFired, score.gateCostCorrect, formatMarginSummary(score.relationMargins))
	return buffer.String()
}

func TestRestatementCasesAreAllSameAndAreScoredWithTheScriptedChooser(t *testing.T) {
	cases := restatementCases(t)
	if len(cases) != 6 {
		t.Fatalf("want 6 restatement cases, got %d", len(cases))
	}
	for _, testCase := range cases {
		if testCase.Relation != bluememo.RelationSame {
			t.Errorf("%s expects %s, a restatement is same", testCase.ID, testCase.Relation)
		}
	}
	confident := runJudgeEval(t, cases, 2, sameChooser)
	if score := scoreJudgeOutcomes(confident); score.correct != len(cases) || score.gateFired != 0 {
		t.Fatalf("a confident same should be right on every case, got %+v", score)
	}
	hesitant := runJudgeEval(t, cases, 2, func() bluememo.Chooser {
		return bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
			bluememo.ImportanceInstruction: {"4": 0.9},
			bluememo.RelationInstruction:   {"1": 0.45, "3": 0.30, "4": 0.25},
			bluememo.TargetInstruction:     {"0": 0.9},
		}}
	})
	if score := scoreJudgeOutcomes(hesitant); score.gateCostCorrect != len(cases) || score.correct != 0 {
		t.Fatalf("a hesitant same should be gated to unrelated on every case, got %+v", score)
	}
	t.Log("scripted chooser; the numbers check the harness, not any model" + formatRestatementTable(confident))
}

func liveJevChooser(t *testing.T) func() bluememo.Chooser {
	t.Helper()
	endpoint, model, apiKey := os.Getenv(judgeJevURLVariable), os.Getenv(judgeJevModelVariable), os.Getenv(judgeJevKeyVariable)
	if endpoint == "" || model == "" || apiKey == "" {
		t.Skipf("set %s, %s and %s to measure Jev as the judge", judgeJevURLVariable, judgeJevModelVariable, judgeJevKeyVariable)
	}
	source := jevSource{url: endpoint, model: model, apiKey: apiKey, client: &http.Client{Timeout: time.Minute}}
	return func() bluememo.Chooser { return instrumentChooser{source: source} }
}

func liveEmbedder(t *testing.T) (bluememo.Embedder, string) {
	t.Helper()
	endpoint, model := os.Getenv(evalEmbeddingURLVariable), os.Getenv(evalEmbeddingModelVariable)
	if endpoint == "" || model == "" {
		t.Skipf("set %s and %s (and optionally %s) to embed with a real model", evalEmbeddingURLVariable, evalEmbeddingModelVariable, evalEmbeddingKeyVariable)
	}
	return httpEmbedder{url: endpoint, model: model, apiKey: os.Getenv(evalEmbeddingKeyVariable), client: &http.Client{Timeout: time.Minute}}, model
}

func TestRestatementWithJev(t *testing.T) {
	newChooser := liveJevChooser(t)
	outcomes := runJudgeEval(t, restatementCases(t), judgeHTTPWorkerCount, newChooser)
	t.Log(formatRestatementTable(outcomes))
}

func TestStreamCollapseWithJev(t *testing.T) {
	newChooser := liveJevChooser(t)
	embedder, embeddingModel := liveEmbedder(t)
	runs := runAllStreams(t, embedder, embeddingModel, newChooser)
	t.Log("live components: judge Jev, embedder " + embeddingModel + formatStreamSummaryTable(runs) + formatStreamStepTable(runs))
}

func TestStreamRecallCostWithJev(t *testing.T) {
	newChooser := liveJevChooser(t)
	embedder, embeddingModel := liveEmbedder(t)
	t.Log("live components: judge Jev, embedder " + embeddingModel + measureRecallArms(t, embedder, embeddingModel, func(streamFact) bluememo.Chooser { return newChooser() }))
}
