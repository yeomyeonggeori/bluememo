package bluememo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

type judgeKind string

const (
	judgeKindClearSame      judgeKind = "clear_same"
	judgeKindClearUpdates   judgeKind = "clear_updates"
	judgeKindClearExtends   judgeKind = "clear_extends"
	judgeKindClearUnrelated judgeKind = "clear_unrelated"
	judgeKindAmbiguous      judgeKind = "ambiguous"
	judgeKindTrapSame       judgeKind = "trap_same"
	judgeKindRestatement    judgeKind = "restatement"
)

var judgeKinds = []judgeKind{judgeKindClearSame, judgeKindClearUpdates, judgeKindClearExtends, judgeKindClearUnrelated, judgeKindAmbiguous, judgeKindTrapSame, judgeKindRestatement}

var judgeExpectedRelations = []bluememo.Relation{bluememo.RelationSame, bluememo.RelationUpdates, bluememo.RelationExtends, bluememo.RelationUnrelated}

const (
	judgeCasesPath             = "testdata/judge-eval.json"
	judgeMinimumCasesPerKind   = 5
	judgeMaximumCandidates     = 4
	judgeHTTPWorkerCount       = 4
	judgeQuestionName          = "relation"
	judgeJevURLVariable        = "BLUEMEMO_EVAL_JUDGE_URL"
	judgeJevModelVariable      = "BLUEMEMO_EVAL_JUDGE_MODEL"
	judgeJevKeyVariable        = "BLUEMEMO_EVAL_JUDGE_KEY"
	judgeJuliaCommandVariable  = "BLUEMEMO_EVAL_JUDGE_JULIA_COMMAND"
	judgeJuliaModelDirVariable = "BLUEMEMO_EVAL_JUDGE_JULIA_MODEL_DIR"
)

type judgeCase struct {
	ID         string            `json:"id"`
	Kind       judgeKind         `json:"kind"`
	Split      evalSplit         `json:"split"`
	Statement  string            `json:"statement"`
	Candidates []string          `json:"candidates"`
	Relation   bluememo.Relation `json:"relation"`
	Target     int               `json:"target"`
}

type judgeFile struct {
	Cases []judgeCase `json:"cases"`
}

func loadJudgeCases(t *testing.T) []judgeCase {
	t.Helper()
	content, errorValue := os.ReadFile(judgeCasesPath)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var parsed judgeFile
	if errorValue := decoder.Decode(&parsed); errorValue != nil {
		t.Fatalf("%s does not parse: %v", judgeCasesPath, errorValue)
	}
	seen := map[string]bool{}
	for _, testCase := range parsed.Cases {
		if errorValue := validateJudgeCase(testCase); errorValue != nil {
			t.Fatalf("case %q: %v", testCase.ID, errorValue)
		}
		if seen[testCase.ID] {
			t.Fatalf("case id %q appears twice", testCase.ID)
		}
		seen[testCase.ID] = true
	}
	return parsed.Cases
}

func validateJudgeCase(testCase judgeCase) error {
	if strings.TrimSpace(testCase.ID) == "" || strings.TrimSpace(testCase.Statement) == "" {
		return errors.New("a case needs an id and a statement")
	}
	if !slices.Contains(judgeKinds, testCase.Kind) {
		return fmt.Errorf("kind %q is not one of %v", testCase.Kind, judgeKinds)
	}
	if !slices.Contains(evalSplits, testCase.Split) {
		return fmt.Errorf("split %q is not one of %v", testCase.Split, evalSplits)
	}
	if !slices.Contains(judgeExpectedRelations, testCase.Relation) {
		return fmt.Errorf("relation %q is not one of %v", testCase.Relation, judgeExpectedRelations)
	}
	if len(testCase.Candidates) == 0 || len(testCase.Candidates) > judgeMaximumCandidates {
		return fmt.Errorf("a case holds 1 to %d candidates, this one holds %d", judgeMaximumCandidates, len(testCase.Candidates))
	}
	if testCase.Kind == judgeKindAmbiguous && testCase.Relation != bluememo.RelationUnrelated {
		return errors.New("an ambiguous case expects unrelated, the answer the gate should produce")
	}
	return validateJudgeTarget(testCase)
}

func validateJudgeTarget(testCase judgeCase) error {
	if testCase.Relation == bluememo.RelationUnrelated {
		if testCase.Target != -1 {
			return fmt.Errorf("an unrelated case has target -1, got %d", testCase.Target)
		}
		return nil
	}
	if testCase.Target < 0 || testCase.Target >= len(testCase.Candidates) {
		return fmt.Errorf("target %d is outside the %d candidates", testCase.Target, len(testCase.Candidates))
	}
	return nil
}

type choiceCriterion struct {
	Key   string
	Gloss string
}

type distributionSource interface {
	distribute(ctx context.Context, state string, instruction string, criteria []choiceCriterion) (map[string]float64, error)
}

var (
	instructionAnswerLinePattern = regexp.MustCompile(`(?m)^(\d) (\S.*)$`)
	instructionColumnPattern     = regexp.MustCompile(`\s{2,}`)
	subjectCandidateLinePattern  = regexp.MustCompile(`(?m)^(\d+)\. (.+)$`)
)

func instructionGlosses(instruction string) map[string]string {
	glosses := map[string]string{}
	for _, match := range instructionAnswerLinePattern.FindAllStringSubmatch(instruction, -1) {
		columns := instructionColumnPattern.Split(strings.TrimSpace(match[2]), 2)
		glosses[match[1]] = strings.Join(columns, ": ")
	}
	return glosses
}

func subjectGlosses(subject string) map[string]string {
	glosses := map[string]string{noTargetAnswer: "none of the candidates"}
	for _, match := range subjectCandidateLinePattern.FindAllStringSubmatch(subject, -1) {
		glosses[match[1]] = "candidate " + match[1] + ": " + match[2]
	}
	return glosses
}

const noTargetAnswer = "9"

func criteriaFor(request bluememo.ChoiceRequest) ([]choiceCriterion, error) {
	glosses := instructionGlosses(request.Instruction)
	if request.Instruction == bluememo.TargetInstruction {
		glosses = subjectGlosses(request.Subject)
	}
	criteria := make([]choiceCriterion, 0, len(request.Answers))
	for _, answer := range request.Answers {
		gloss, isPresent := glosses[answer]
		if !isPresent {
			return nil, fmt.Errorf("no gloss for answer %q", answer)
		}
		criteria = append(criteria, choiceCriterion{Key: answer, Gloss: gloss})
	}
	return criteria, nil
}

type instrumentChooser struct {
	source distributionSource
}

func (chooser instrumentChooser) Choose(ctx context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	criteria, errorValue := criteriaFor(request)
	if errorValue != nil {
		return nil, errorValue
	}
	distribution, errorValue := chooser.source.distribute(ctx, request.Subject, request.Instruction, criteria)
	if errorValue != nil {
		return nil, errorValue
	}
	for _, criterion := range criteria {
		if _, isPresent := distribution[criterion.Key]; !isPresent {
			return nil, fmt.Errorf("distribution %v has no probability for answer %q", distribution, criterion.Key)
		}
	}
	return distribution, nil
}

type jevSource struct {
	url    string
	model  string
	apiKey string
	client *http.Client
}

func (source jevSource) distribute(ctx context.Context, state string, instruction string, criteria []choiceCriterion) (map[string]float64, error) {
	criteriaByKey := make(map[string]string, len(criteria))
	for _, criterion := range criteria {
		criteriaByKey[criterion.Key] = criterion.Gloss
	}
	responseBody, errorValue := postJSONWithRetry(ctx, source.client, source.url, source.apiKey, map[string]any{
		"model": source.model,
		"state": state,
		"questions": map[string]any{judgeQuestionName: map[string]any{
			"type":         "choice",
			"instructions": instruction,
			"criteria":     criteriaByKey,
		}},
	})
	if errorValue != nil {
		return nil, errorValue
	}
	var parsed decisionsResponse
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return nil, fmt.Errorf("decisions response is not the expected shape: %w", errorValue)
	}
	answer, isPresent := parsed.Answers[judgeQuestionName]
	if !isPresent || len(answer.Probabilities) == 0 {
		return nil, fmt.Errorf("decisions response carries no probabilities: %s", responseBody)
	}
	return answer.Probabilities, nil
}

type juliaSource struct {
	runner juliaReranker
}

var juliaProbabilityPattern = regexp.MustCompile(`([^\s:,()]+): ([0-9.eE+-]+)`)

func (source juliaSource) distribute(ctx context.Context, state string, instruction string, criteria []choiceCriterion) (map[string]float64, error) {
	arguments := []string{"--state", state, "--question", juliaQuestion(instruction), "--type", "choice"}
	for _, criterion := range criteria {
		arguments = append(arguments, "--option", criterion.Key+"="+criterion.Gloss)
	}
	output, errorValue := source.runner.run(ctx, append(arguments, "--probabilities"))
	if errorValue != nil {
		return nil, errorValue
	}
	return parseJuliaDistribution(output)
}

func juliaQuestion(instruction string) string {
	var kept []string
	for _, line := range strings.Split(instruction, "\n") {
		if instructionAnswerLinePattern.MatchString(line) || strings.HasPrefix(line, "Answer with") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(strings.Fields(strings.Join(kept, " ")), " ")
}

func parseJuliaDistribution(output string) (map[string]float64, error) {
	_, probabilities, isPresent := strings.Cut(output, "(")
	if !isPresent {
		return nil, fmt.Errorf("julia output carries no probabilities: %q", output)
	}
	distribution := map[string]float64{}
	for _, match := range juliaProbabilityPattern.FindAllStringSubmatch(probabilities, -1) {
		probability, errorValue := strconv.ParseFloat(match[2], 64)
		if errorValue != nil {
			return nil, fmt.Errorf("julia probability %q: %w", match[2], errorValue)
		}
		distribution[match[1]] = probability
	}
	if len(distribution) == 0 {
		return nil, fmt.Errorf("julia output has no probability pairs: %q", output)
	}
	return distribution, nil
}

type judgeTrace struct {
	importance  map[string]float64
	relation    map[string]float64
	targetAsked bool
}

type tracingChooser struct {
	inner bluememo.Chooser
	trace *judgeTrace
}

func (chooser tracingChooser) Choose(ctx context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	distribution, errorValue := chooser.inner.Choose(ctx, request)
	if errorValue != nil {
		return nil, errorValue
	}
	switch request.Instruction {
	case bluememo.ImportanceInstruction:
		chooser.trace.importance = distribution
	case bluememo.RelationInstruction:
		chooser.trace.relation = distribution
	case bluememo.TargetInstruction:
		chooser.trace.targetAsked = true
	}
	return distribution, nil
}

type judgeOutcome struct {
	testCase  judgeCase
	judgement bluememo.Judgement
	trace     judgeTrace
}

func leadingMargin(distribution map[string]float64) (string, float64) {
	best, runnerUp, leader := 0.0, 0.0, ""
	for answer, probability := range distribution {
		switch {
		case probability > best:
			best, runnerUp, leader = probability, best, answer
		case probability > runnerUp:
			runnerUp = probability
		}
	}
	return leader, best - runnerUp
}

func relationOfAnswer(answer string) (bluememo.Relation, bool) {
	gloss, isPresent := instructionGlosses(bluememo.RelationInstruction)[answer]
	if !isPresent {
		return "", false
	}
	relation := bluememo.Relation(strings.TrimSuffix(strings.Fields(gloss)[0], ":"))
	return relation, slices.Contains(judgeExpectedRelations, relation)
}

func (outcome judgeOutcome) rawRelation() (bluememo.Relation, float64, bool) {
	if outcome.trace.relation == nil {
		return "", 0, false
	}
	leader, margin := leadingMargin(outcome.trace.relation)
	relation, isKnown := relationOfAnswer(leader)
	return relation, margin, isKnown
}

func (outcome judgeOutcome) gateFired() bool {
	raw, _, isKnown := outcome.rawRelation()
	if !isKnown || outcome.trace.targetAsked {
		return false
	}
	return raw != bluememo.RelationUnrelated && outcome.judgement.Relation == bluememo.RelationUnrelated
}

func isColdRelation(relation bluememo.Relation) bool {
	return relation == bluememo.RelationSame || relation == bluememo.RelationUpdates
}

func isAtRiskRelation(relation bluememo.Relation) bool {
	return relation == bluememo.RelationExtends || relation == bluememo.RelationUnrelated
}

func (outcome judgeOutcome) isUnsafeCold() bool {
	return isColdRelation(outcome.judgement.Relation) && isAtRiskRelation(outcome.testCase.Relation)
}

func (outcome judgeOutcome) isWrongTargetCold() bool {
	return isColdRelation(outcome.judgement.Relation) && !isAtRiskRelation(outcome.testCase.Relation) && outcome.judgement.TargetIndex != outcome.testCase.Target
}

func (outcome judgeOutcome) gatePrevented() bool {
	raw, _, _ := outcome.rawRelation()
	return outcome.gateFired() && isColdRelation(raw) && isAtRiskRelation(outcome.testCase.Relation)
}

func (outcome judgeOutcome) gateCostACorrectAnswer() bool {
	raw, _, _ := outcome.rawRelation()
	return outcome.gateFired() && raw == outcome.testCase.Relation
}

func runJudgeEval(t *testing.T, cases []judgeCase, workerCount int, newChooser func() bluememo.Chooser) []judgeOutcome {
	t.Helper()
	outcomes := make([]judgeOutcome, len(cases))
	errorsByIndex := make([]error, len(cases))
	indexes := make(chan int)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indexes {
				outcomes[index], errorsByIndex[index] = judgeOneCase(cases[index], newChooser())
			}
		}()
	}
	for index := range cases {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	if errorValue := errors.Join(errorsByIndex...); errorValue != nil {
		t.Fatal(errorValue)
	}
	return outcomes
}

func judgeOneCase(testCase judgeCase, chooser bluememo.Chooser) (judgeOutcome, error) {
	trace := &judgeTrace{}
	judge := bluememo.DistributionJudge{Chooser: tracingChooser{inner: chooser, trace: trace}}
	judgement, errorValue := judge.Judge(context.Background(), testCase.Statement, testCase.Candidates)
	if errorValue != nil {
		return judgeOutcome{}, fmt.Errorf("case %s: %w", testCase.ID, errorValue)
	}
	return judgeOutcome{testCase: testCase, judgement: judgement, trace: *trace}, nil
}

type judgeScore struct {
	caseCount         int
	correct           int
	noise             int
	unsafeCold        int
	atRisk            int
	wrongTargetCold   int
	gateFired         int
	gatePrevented     int
	gateCostCorrect   int
	relationMargins   []float64
	importanceMargins []float64
}

func scoreJudgeOutcomes(outcomes []judgeOutcome) judgeScore {
	score := judgeScore{caseCount: len(outcomes)}
	for _, outcome := range outcomes {
		score.correct += boolToCount(outcome.judgement.Relation == outcome.testCase.Relation)
		score.noise += boolToCount(outcome.judgement.Relation == bluememo.RelationNoise)
		score.unsafeCold += boolToCount(outcome.isUnsafeCold())
		score.atRisk += boolToCount(isAtRiskRelation(outcome.testCase.Relation))
		score.wrongTargetCold += boolToCount(outcome.isWrongTargetCold())
		score.gateFired += boolToCount(outcome.gateFired())
		score.gatePrevented += boolToCount(outcome.gatePrevented())
		score.gateCostCorrect += boolToCount(outcome.gateCostACorrectAnswer())
		if outcome.trace.relation != nil {
			_, margin := leadingMargin(outcome.trace.relation)
			score.relationMargins = append(score.relationMargins, margin)
		}
		if outcome.trace.importance != nil {
			_, margin := leadingMargin(outcome.trace.importance)
			score.importanceMargins = append(score.importanceMargins, margin)
		}
	}
	return score
}

func boolToCount(condition bool) int {
	if condition {
		return 1
	}
	return 0
}

func formatMarginSummary(margins []float64) string {
	if len(margins) == 0 {
		return "none"
	}
	sorted := slices.Clone(margins)
	sort.Float64s(sorted)
	distinct := len(slices.Compact(slices.Clone(sorted)))
	return fmt.Sprintf("min %.3f  median %.3f  max %.3f  (%d values, %d distinct)", sorted[0], sorted[len(sorted)/2], sorted[len(sorted)-1], len(sorted), distinct)
}

func judgeOutcomesOfKind(outcomes []judgeOutcome, kind judgeKind) []judgeOutcome {
	var selected []judgeOutcome
	for _, outcome := range outcomes {
		if outcome.testCase.Kind == kind {
			selected = append(selected, outcome)
		}
	}
	return selected
}

func formatJudgeScoreTable(outcomes []judgeOutcome) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\nkind\tn\taccuracy\tunsafe cold\tunsafe/at-risk\twrong-target cold\tnoise\tgate fired\tgate prevented\tgate cost\t")
	writeRow := func(name string, group []judgeOutcome) {
		score := scoreJudgeOutcomes(group)
		fmt.Fprintf(writer, "%s\t%d\t%d/%d = %.3f\t%d/%d = %.3f\t%d/%d\t%d\t%d\t%d\t%d\t%d\t\n",
			name, score.caseCount, score.correct, score.caseCount, ratio(score.correct, score.caseCount),
			score.unsafeCold, score.caseCount, ratio(score.unsafeCold, score.caseCount),
			score.unsafeCold, score.atRisk, score.wrongTargetCold, score.noise,
			score.gateFired, score.gatePrevented, score.gateCostCorrect)
	}
	writeRow("overall", outcomes)
	for _, kind := range judgeKinds {
		writeRow(string(kind), judgeOutcomesOfKind(outcomes, kind))
	}
	writer.Flush()
	overall := scoreJudgeOutcomes(outcomes)
	fmt.Fprintf(&buffer, "\nrelation margin (best - runner-up): %s\nimportance margin (best - runner-up): %s\n",
		formatMarginSummary(overall.relationMargins), formatMarginSummary(overall.importanceMargins))
	return buffer.String()
}

func ratio(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func formatJudgeCaseTable(outcomes []judgeOutcome) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "\ncase\texpected\tjudged\ttarget\traw leader\tmargin\tgate fired\t")
	for _, outcome := range outcomes {
		if outcome.testCase.Kind != judgeKindTrapSame && outcome.testCase.Kind != judgeKindAmbiguous && outcome.testCase.Kind != judgeKindRestatement {
			continue
		}
		raw, margin, _ := outcome.rawRelation()
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\t%.3f\t%t\t\n", outcome.testCase.ID, outcome.testCase.Relation, outcome.judgement.Relation, outcome.judgement.TargetIndex, raw, margin, outcome.gateFired())
	}
	writer.Flush()
	return buffer.String()
}

func TestJudgeEvalCasesParseAndCarryOnlyDeclaredValues(t *testing.T) {
	cases := loadJudgeCases(t)
	perKind := map[judgeKind]int{}
	perSplit := map[evalSplit]int{}
	for _, testCase := range cases {
		perKind[testCase.Kind]++
		perSplit[testCase.Split]++
	}
	for _, kind := range judgeKinds {
		if perKind[kind] < judgeMinimumCasesPerKind {
			t.Errorf("kind %s has %d cases, want at least %d", kind, perKind[kind], judgeMinimumCasesPerKind)
		}
	}
	for _, split := range evalSplits {
		if perSplit[split] == 0 {
			t.Errorf("split %s has no cases", split)
		}
	}
}

func TestJudgeEvalRefusesAnUnknownKindOrAnInconsistentTarget(t *testing.T) {
	valid := judgeCase{ID: "x", Kind: judgeKindClearSame, Split: evalSplitEvolve, Statement: "s", Candidates: []string{"c"}, Relation: bluememo.RelationSame, Target: 0}
	if errorValue := validateJudgeCase(valid); errorValue != nil {
		t.Fatalf("a valid case was refused: %v", errorValue)
	}
	mutations := map[string]func(judgeCase) judgeCase{
		"unknown kind":            func(c judgeCase) judgeCase { c.Kind = "vague"; return c },
		"unknown split":           func(c judgeCase) judgeCase { c.Split = "test"; return c },
		"noise expected":          func(c judgeCase) judgeCase { c.Relation = bluememo.RelationNoise; return c },
		"target out of range":     func(c judgeCase) judgeCase { c.Target = 1; return c },
		"unrelated with a target": func(c judgeCase) judgeCase { c.Relation = bluememo.RelationUnrelated; return c },
		"too many candidates":     func(c judgeCase) judgeCase { c.Candidates = make([]string, 5); return c },
		"ambiguous but decisive":  func(c judgeCase) judgeCase { c.Kind = judgeKindAmbiguous; return c },
	}
	for name, mutate := range mutations {
		if validateJudgeCase(mutate(valid)) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestJudgeEvalGlossesComeFromTheLibraryInstructions(t *testing.T) {
	glosses := instructionGlosses(bluememo.RelationInstruction)
	for answer, relation := range map[string]bluememo.Relation{"1": bluememo.RelationSame, "2": bluememo.RelationUpdates, "3": bluememo.RelationExtends, "4": bluememo.RelationUnrelated} {
		derived, isKnown := relationOfAnswer(answer)
		if !isKnown || derived != relation {
			t.Errorf("answer %s derives %q from %q, want %q", answer, derived, glosses[answer], relation)
		}
	}
	if len(instructionGlosses(bluememo.ImportanceInstruction)) != 5 {
		t.Errorf("importance glosses: %v", instructionGlosses(bluememo.ImportanceInstruction))
	}
}

func TestJudgeEvalParsesTheJuliaDistribution(t *testing.T) {
	distribution, errorValue := parseJuliaDistribution("1 (1: 0.967, 2: 0.003, 3: 3.0e-02, 4: 0.000)\n")
	if errorValue != nil || distribution["1"] != 0.967 || distribution["3"] != 0.03 || len(distribution) != 4 {
		t.Fatalf("got %v (%v)", distribution, errorValue)
	}
	if _, errorValue := parseJuliaDistribution("1"); errorValue == nil {
		t.Fatal("output without probabilities was accepted")
	}
}

func TestJudgeEvalHarnessRunsEveryCaseWithTheScriptedChooser(t *testing.T) {
	cases := loadJudgeCases(t)
	scripted := func() bluememo.Chooser {
		return bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
			bluememo.ImportanceInstruction: {"4": 0.9},
			bluememo.RelationInstruction:   {"3": 0.6, "4": 0.3},
			bluememo.TargetInstruction:     {"0": 0.9},
		}}
	}
	outcomes := runJudgeEval(t, cases, 2, scripted)
	score := scoreJudgeOutcomes(outcomes)
	if score.caseCount != len(cases) || score.unsafeCold != 0 || score.gateFired != 0 {
		t.Fatalf("a chooser that only ever says extends took nothing cold, got %+v", score)
	}
	if len(score.relationMargins) != len(cases) {
		t.Fatalf("every case should have asked for a relation, got %d margins", len(score.relationMargins))
	}
	t.Log("scripted chooser; the numbers check the harness, not any model" + formatJudgeScoreTable(outcomes))
}

func TestJudgeEvalGateActivityIsMeasuredFromTheRealJudge(t *testing.T) {
	cases := loadJudgeCases(t)
	hesitant := func() bluememo.Chooser {
		return bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
			bluememo.ImportanceInstruction: {"4": 0.9},
			bluememo.RelationInstruction:   {"1": 0.45, "3": 0.30, "4": 0.25},
			bluememo.TargetInstruction:     {"0": 0.9},
		}}
	}
	outcomes := runJudgeEval(t, cases, 2, hesitant)
	score := scoreJudgeOutcomes(outcomes)
	if score.gateFired != len(cases) || score.unsafeCold != 0 {
		t.Fatalf("a hesitant same should be gated on every case, got %+v", score)
	}
}

func liveJudgeEval(t *testing.T, workerCount int, source distributionSource) {
	t.Helper()
	cases := loadJudgeCases(t)
	outcomes := runJudgeEval(t, cases, workerCount, func() bluememo.Chooser { return instrumentChooser{source: source} })
	t.Log(formatJudgeScoreTable(outcomes) + formatJudgeCaseTable(outcomes))
}

func TestJudgeSafetyWithJev(t *testing.T) {
	endpoint, model, apiKey := os.Getenv(judgeJevURLVariable), os.Getenv(judgeJevModelVariable), os.Getenv(judgeJevKeyVariable)
	if endpoint == "" || model == "" || apiKey == "" {
		t.Skipf("set %s, %s and %s to measure Jev as the judge", judgeJevURLVariable, judgeJevModelVariable, judgeJevKeyVariable)
	}
	liveJudgeEval(t, judgeHTTPWorkerCount, jevSource{url: endpoint, model: model, apiKey: apiKey, client: &http.Client{Timeout: time.Minute}})
}

func TestJudgeSafetyWithJulia(t *testing.T) {
	command, modelDir := os.Getenv(judgeJuliaCommandVariable), os.Getenv(judgeJuliaModelDirVariable)
	if command == "" || modelDir == "" {
		t.Skipf("set %s and %s to measure Julia-1 as the judge", judgeJuliaCommandVariable, judgeJuliaModelDirVariable)
	}
	liveJudgeEval(t, 1, juliaSource{runner: juliaReranker{command: command, modelDir: modelDir}})
}
