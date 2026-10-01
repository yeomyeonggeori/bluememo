package bluememo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/openrouter"
)

const (
	locomoDatasetVariable      = "BLUEMEMO_LOCOMO_DATASET"
	locomoOutputVariable       = "BLUEMEMO_LOCOMO_OUTPUT"
	locomoConversationVariable = "BLUEMEMO_LOCOMO_CONVERSATION"
	locomoSessionLimitVariable = "BLUEMEMO_LOCOMO_SESSION_LIMIT"
	locomoArmVariable          = "BLUEMEMO_LOCOMO_ARM"
	locomoCredentialVariable   = "OPENROUTER_API_KEY"

	locomoEmbeddingURL   = "https://openrouter.ai/api/v1/embeddings"
	locomoEmbeddingModel = "perplexity/pplx-embed-v1-4b"
	locomoChatURL        = "https://openrouter.ai/api/v1/chat/completions"
	locomoChatModel      = "openai/gpt-6-luna"
	locomoDecisionsURL   = "https://openrouter.ai/api/alpha/decisions"
	locomoDecisionsModel = "~typesafe/jev-latest"

	locomoDefaultRecallLimit = 20
	locomoRerankDepth        = 30
	locomoSourceLimit        = 10
	locomoQuestionWorkers    = 6
	locomoAttemptLimit       = 8
	locomoBackoffBase        = time.Second
	locomoBackoffCeiling     = 30 * time.Second
	locomoSettleAttempts     = 3
	locomoAdversarialLabel   = 5
	locomoIdentifierSeed     = 20260921
)

type locomoTurn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type locomoSession struct {
	number   int
	dateTime time.Time
	turns    []locomoTurn
}

type locomoQuestion struct {
	Question          string   `json:"question"`
	Answer            any      `json:"answer"`
	AdversarialAnswer any      `json:"adversarial_answer"`
	Evidence          []string `json:"evidence"`
	Category          int      `json:"category"`
}

type locomoRecord struct {
	SampleID  string
	Sessions  []locomoSession
	Questions []locomoQuestion
}

type locomoRawConversation struct {
	SampleID     string                     `json:"sample_id"`
	QA           []locomoQuestion           `json:"qa"`
	Conversation map[string]json.RawMessage `json:"conversation"`
}

var locomoSessionDatePattern = regexp.MustCompile(`^\s*(\d{1,2}):(\d{2})\s*(am|pm)\s+on\s+(\d{1,2})\s+(\w+),?\s+(\d{4})\s*$`)
var locomoEvidencePattern = regexp.MustCompile(`D(\d+):`)

func parseLocomoDateTime(text string) (time.Time, error) {
	match := locomoSessionDatePattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}, fmt.Errorf("session date %q is not the LoCoMo shape", text)
	}
	return time.Parse("3:04 pm 2 January 2006", fmt.Sprintf("%s:%s %s %s %s %s", match[1], match[2], match[3], match[4], match[5], match[6]))
}

func loadLocomoConversations(path string) ([]locomoRecord, error) {
	content, errorValue := os.ReadFile(path)
	if errorValue != nil {
		return nil, errorValue
	}
	var raw []locomoRawConversation
	if errorValue := json.Unmarshal(content, &raw); errorValue != nil {
		return nil, errorValue
	}
	records := make([]locomoRecord, 0, len(raw))
	for _, conversation := range raw {
		sessions, errorValue := locomoSessionsOf(conversation.Conversation)
		if errorValue != nil {
			return nil, errorValue
		}
		records = append(records, locomoRecord{SampleID: conversation.SampleID, Sessions: sessions, Questions: conversation.QA})
	}
	return records, nil
}

func locomoSessionsOf(conversation map[string]json.RawMessage) ([]locomoSession, error) {
	sessions := []locomoSession{}
	for number := 1; ; number++ {
		turnsRaw, isPresent := conversation["session_"+strconv.Itoa(number)]
		if !isPresent {
			return sessions, nil
		}
		var dateText string
		if errorValue := json.Unmarshal(conversation["session_"+strconv.Itoa(number)+"_date_time"], &dateText); errorValue != nil {
			return nil, errorValue
		}
		dateTime, errorValue := parseLocomoDateTime(dateText)
		if errorValue != nil {
			return nil, errorValue
		}
		var turns []locomoTurn
		if errorValue := json.Unmarshal(turnsRaw, &turns); errorValue != nil {
			return nil, errorValue
		}
		sessions = append(sessions, locomoSession{number: number, dateTime: dateTime, turns: turns})
	}
}

func turnsBody(turns []locomoTurn) string {
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		lines = append(lines, turn.Speaker+": "+turn.Text)
	}
	return strings.Join(lines, "\n")
}

func (session locomoSession) body() string {
	return turnsBody(session.turns)
}

func recallLimit() int {
	limit, errorValue := strconv.Atoi(os.Getenv("BLUEMEMO_LOCOMO_RECALL_LIMIT"))
	if errorValue != nil || limit < 1 {
		return locomoDefaultRecallLimit
	}
	return limit
}

func reusesStore() bool {
	return os.Getenv("BLUEMEMO_LOCOMO_REUSE_STORE") != ""
}

func reembedsBeforeAsking() bool {
	return os.Getenv("BLUEMEMO_LOCOMO_REEMBED") != ""
}

func turnWindow() int {
	size, errorValue := strconv.Atoi(os.Getenv("BLUEMEMO_LOCOMO_TURN_WINDOW"))
	if errorValue != nil || size < 1 {
		return 0
	}
	return size
}

func (session locomoSession) notes() []bluememo.Note {
	window := turnWindow()
	if window == 0 {
		return []bluememo.Note{{GroupID: fmt.Sprintf("D%d", session.number), Body: session.body(), SpeakerName: session.turns[0].Speaker}}
	}
	notes := make([]bluememo.Note, 0, len(session.turns)/window+1)
	for start := 0; start < len(session.turns); start += window {
		turns := session.turns[start:min(start+window, len(session.turns))]
		notes = append(notes, bluememo.Note{
			GroupID:     fmt.Sprintf("D%d#%d", session.number, start/window),
			Body:        turnsBody(turns),
			SpeakerName: turns[0].Speaker,
		})
	}
	return notes
}

func (question locomoQuestion) goldText() string {
	if question.Answer != nil {
		return fmt.Sprint(question.Answer)
	}
	return fmt.Sprint(question.AdversarialAnswer)
}

func (question locomoQuestion) isInside(ingestedSessions int) bool {
	if question.Category == locomoAdversarialLabel {
		return true
	}
	numbers := []int{}
	for _, evidence := range question.Evidence {
		for _, match := range locomoEvidencePattern.FindAllStringSubmatch(evidence, -1) {
			number, _ := strconv.Atoi(match[1])
			numbers = append(numbers, number)
		}
	}
	if len(numbers) == 0 {
		return false
	}
	for _, number := range numbers {
		if number > ingestedSessions {
			return false
		}
	}
	return true
}

type locomoCostEntry struct {
	Calls int     `json:"calls"`
	Cost  float64 `json:"cost"`
}

type locomoLedger struct {
	mutex   sync.Mutex
	entries map[string]*locomoCostEntry
}

func (ledger *locomoLedger) record(component string, cost float64) {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()
	if ledger.entries == nil {
		ledger.entries = map[string]*locomoCostEntry{}
	}
	entry, isPresent := ledger.entries[component]
	if !isPresent {
		entry = &locomoCostEntry{}
		ledger.entries[component] = entry
	}
	entry.Calls++
	entry.Cost += cost
}

func (ledger *locomoLedger) snapshot() map[string]locomoCostEntry {
	ledger.mutex.Lock()
	defer ledger.mutex.Unlock()
	snapshot := map[string]locomoCostEntry{}
	for component, entry := range ledger.entries {
		snapshot[component] = *entry
	}
	return snapshot
}

type locomoClock struct {
	mutex   sync.Mutex
	current time.Time
}

func (clock *locomoClock) now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.current
}

func (clock *locomoClock) set(instant time.Time) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.current = instant
}

func locomoIdentifiers(seed int64) func() string {
	var mutex sync.Mutex
	source := rand.New(rand.NewSource(seed))
	return func() string {
		mutex.Lock()
		defer mutex.Unlock()
		return fmt.Sprintf("%016x%016x", source.Uint64(), source.Uint64())
	}
}

func sourcesWanted() bool {
	return os.Getenv("BLUEMEMO_LOCOMO_SOURCES") != ""
}

func spokenContext(result bluememo.RecallResult) string {
	if len(result.Sources) == 0 {
		return ""
	}
	bodies := make(map[string]string, len(result.Sources))
	for _, source := range result.Sources {
		bodies[source.OriginID] = source.Body
	}
	var listing strings.Builder
	taken := map[string]bool{}
	for _, recalled := range result.Memories {
		body, isKnown := bodies[recalled.Memory.OriginID]
		if !isKnown || taken[recalled.Memory.OriginID] {
			continue
		}
		taken[recalled.Memory.OriginID] = true
		fmt.Fprintf(&listing, "%s\n\n", body)
		if len(taken) == locomoSourceLimit {
			break
		}
	}
	return "\nWhat was said:\n" + listing.String()
}

func numberedContext(memories []bluememo.RecalledMemory) string {
	var listing strings.Builder
	for index, recalled := range memories {
		occurrence := recalled.Memory.TimeReference(time.UTC)
		if occurrence == "" {
			fmt.Fprintf(&listing, "%d. %s\n", index+1, recalled.Memory.Content)
			continue
		}
		fmt.Fprintf(&listing, "%d. [%s] %s\n", index+1, occurrence, recalled.Memory.Content)
	}
	return listing.String()
}

const locomoAnswerInstruction = `You answer a question about two people's conversations using only the numbered memories you are given.
A memory may begin with the time it happened in square brackets. Use that time to answer questions about when something happened; give the time at the precision the memory gives it.
A section headed "What was said" may follow, holding the conversation the memories were drawn from; read it for detail a memory left out.
Work through what the memories say step by step, joining several of them where one alone does not answer the question, and then answer.`

const locomoCorrectnessInstruction = `You grade an answer against a gold answer for a question.
Mark it correct when the answer contains the same facts as the gold answer, however it is worded or how much extra it says. For a question about time, it is correct when it names the same date or period as the gold answer, even in another format.
An answer that says the information is not available is incorrect.
Do not add requirements the gold answer does not state. Wording, formatting and extra detail are not failures.`

const locomoDeclineInstruction = `You read a question and an answer to it. Decide whether the answer declines, meaning it says the information is not available, not mentioned or not known, or whether it asserts a specific fact in answer to the question.
Judge only from the question and the answer. Do not add requirements the answer does not state.`

const (
	locomoAnswerSchema      = `{"type":"object","additionalProperties":false,"required":["reasoning","answer"],"properties":{"reasoning":{"type":"string","description":"How the memories were used to reach the answer, step by step, naming which ones."},"answer":{"type":"string","description":"The answer, briefly. If the memories do not bear on the question, say the information is not available."}}}`
	locomoCorrectnessSchema = `{"type":"object","additionalProperties":false,"required":["isCorrect"],"properties":{"isCorrect":{"type":"boolean"}}}`
	locomoDeclineSchema     = `{"type":"object","additionalProperties":false,"required":["declines"],"properties":{"declines":{"type":"boolean"}}}`
)

type locomoOutcome struct {
	Category      int      `json:"category"`
	Question      string   `json:"question"`
	Gold          string   `json:"gold"`
	Answer        string   `json:"answer"`
	Reasoning     string   `json:"reasoning,omitempty"`
	IsCorrect     bool     `json:"isCorrect"`
	Declines      bool     `json:"declines"`
	Degraded      string   `json:"degraded,omitempty"`
	RecalledLines []string `json:"recalledLines"`
	Failure       string   `json:"failure,omitempty"`
}

type locomoMemoryCounts struct {
	Live  int `json:"live"`
	Dated int `json:"dated"`
}

type locomoResult struct {
	Arm              string                     `json:"arm"`
	SampleID         string                     `json:"sampleID"`
	IngestedSessions int                        `json:"ingestedSessions"`
	Outcomes         []locomoOutcome            `json:"outcomes"`
	Memories         locomoMemoryCounts         `json:"memories"`
	Cost             map[string]locomoCostEntry `json:"cost"`
	SettleFailures   int                        `json:"settleFailures"`
	IngestSeconds    float64                    `json:"ingestSeconds"`
	QuestionSeconds  float64                    `json:"questionSeconds"`
}

type locomoRig struct {
	path   string
	store  *bluememo.Store
	clock  *locomoClock
	model  *openrouter.Client
	ledger *locomoLedger
}

func openLocomoRig(t *testing.T, path string, conversationIndex int, credential string) *locomoRig {
	t.Helper()
	ledger := &locomoLedger{}
	client := openrouter.New(credential)
	client.EmbeddingModel = locomoEmbeddingModel
	client.ChatModel = locomoChatModel
	client.RecordCost = ledger.record
	clock := &locomoClock{current: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	decisions := openrouter.NewDecisions(client)
	model := client
	store, errorValue := bluememo.Open(context.Background(), path, bluememo.Configuration{
		Embedder:           client,
		EmbeddingModel:     locomoEmbeddingModel,
		Model:              model,
		Judge:              bluememo.DistributionJudge{Chooser: decisions},
		Reranker:           decisions,
		RerankDepth:        locomoRerankDepth,
		EmbedTimeReference: true,
		RecallSources:      sourcesWanted(),
		ClaimDuration:      time.Minute,
		Now:                clock.now,
		NewIdentifier:      locomoIdentifiers(locomoIdentifierSeed + int64(conversationIndex)),
	})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	t.Cleanup(func() { store.Close() })
	return &locomoRig{path: path, store: store, clock: clock, model: model, ledger: ledger}
}

func (rig *locomoRig) ingest(ctx context.Context, t *testing.T, sessions []locomoSession) int {
	t.Helper()
	failures := 0
	for _, session := range sessions {
		started := time.Now()
		rig.clock.set(session.dateTime)
		for _, note := range session.notes() {
			if errorValue := rig.store.Memorize(ctx, note); errorValue != nil {
				t.Fatal(errorValue)
			}
			failures += rig.settleWithRetry(ctx, t, session)
		}
		t.Logf("session %d settled in %s", session.number, time.Since(started).Round(time.Second))
	}
	return failures
}

func (rig *locomoRig) settleWithRetry(ctx context.Context, t *testing.T, session locomoSession) int {
	t.Helper()
	failures := 0
	for range locomoSettleAttempts {
		report, errorValue := rig.store.Settle(ctx)
		if errorValue == nil {
			t.Logf("session %d report %+v", session.number, report)
			return failures
		}
		failures++
		t.Logf("session %d settle failed: %v", session.number, errorValue)
		rig.clock.set(rig.clock.now().Add(2 * time.Minute))
	}
	return failures
}

func (rig *locomoRig) answer(ctx context.Context, question locomoQuestion, result bluememo.RecallResult) (string, string, error) {
	subject := "Memories:\n" + numberedContext(result.Memories) + spokenContext(result) + "\nQuestion: " + question.Question
	response, errorValue := rig.model.Structured(ctx, "answer", locomoAnswerSchema, locomoAnswerInstruction, subject)
	if errorValue != nil {
		return "", "", errorValue
	}
	var parsed struct {
		Reasoning string `json:"reasoning"`
		Answer    string `json:"answer"`
	}
	if errorValue := json.Unmarshal([]byte(response), &parsed); errorValue != nil {
		return "", "", fmt.Errorf("answer is not the schema: %w", errorValue)
	}
	return parsed.Answer, parsed.Reasoning, nil
}

func (rig *locomoRig) judgeDecline(ctx context.Context, question locomoQuestion, answer string) (bool, error) {
	response, errorValue := rig.model.Structured(ctx, "judge decline", locomoDeclineSchema, locomoDeclineInstruction, "Question: "+question.Question+"\nAnswer: "+answer)
	if errorValue != nil {
		return false, errorValue
	}
	var parsed struct {
		Declines bool `json:"declines"`
	}
	errorValue = json.Unmarshal([]byte(response), &parsed)
	return parsed.Declines, errorValue
}

func (rig *locomoRig) judgeCorrectness(ctx context.Context, question locomoQuestion, answer string) (bool, error) {
	subject := "Question: " + question.Question + "\nGold answer: " + question.goldText() + "\nAnswer: " + answer
	response, errorValue := rig.model.Structured(ctx, "judge correctness", locomoCorrectnessSchema, locomoCorrectnessInstruction, subject)
	if errorValue != nil {
		return false, errorValue
	}
	var parsed struct {
		IsCorrect bool `json:"isCorrect"`
	}
	errorValue = json.Unmarshal([]byte(response), &parsed)
	return parsed.IsCorrect, errorValue
}

func (rig *locomoRig) ask(ctx context.Context, question locomoQuestion) locomoOutcome {
	outcome := locomoOutcome{Category: question.Category, Question: question.Question, Gold: question.goldText()}
	result, errorValue := rig.store.Recall(ctx, question.Question, recallLimit())
	if errorValue != nil {
		outcome.Failure = "recall: " + errorValue.Error()
		return outcome
	}
	outcome.Degraded = result.DegradedReason
	outcome.RecalledLines = strings.Split(strings.TrimSpace(numberedContext(result.Memories)), "\n")
	outcome.Answer, outcome.Reasoning, errorValue = rig.answer(ctx, question, result)
	if errorValue != nil {
		outcome.Failure = "answer: " + errorValue.Error()
		return outcome
	}
	if question.Category == locomoAdversarialLabel {
		outcome.Declines, errorValue = rig.judgeDecline(ctx, question, outcome.Answer)
		outcome.IsCorrect = outcome.Declines
	} else {
		outcome.IsCorrect, errorValue = rig.judgeCorrectness(ctx, question, outcome.Answer)
	}
	if errorValue != nil {
		outcome.Failure = "judge: " + errorValue.Error()
	}
	return outcome
}

func (rig *locomoRig) askAll(ctx context.Context, questions []locomoQuestion) []locomoOutcome {
	outcomes := make([]locomoOutcome, len(questions))
	indexes := make(chan int)
	var workers sync.WaitGroup
	for range locomoQuestionWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range indexes {
				outcomes[index] = rig.ask(ctx, questions[index])
			}
		}()
	}
	for index := range questions {
		indexes <- index
	}
	close(indexes)
	workers.Wait()
	return outcomes
}

func (rig *locomoRig) describeMemories(ctx context.Context, t *testing.T) locomoMemoryCounts {
	t.Helper()
	memories, errorValue := rig.store.Memories(ctx)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	description := locomoMemoryCounts{Live: len(memories)}
	for _, memory := range memories {
		if memory.OccurredAt.IsZero() {
			continue
		}
		description.Dated++
	}
	return description
}

func questionsInside(record locomoRecord, ingestedSessions int) []locomoQuestion {
	kept := []locomoQuestion{}
	for _, question := range record.Questions {
		if question.isInside(ingestedSessions) {
			kept = append(kept, question)
		}
	}
	return kept
}

func TestLoCoMoAccuracy(t *testing.T) {
	datasetPath, outputDirectory, credential := os.Getenv(locomoDatasetVariable), os.Getenv(locomoOutputVariable), os.Getenv(locomoCredentialVariable)
	if datasetPath == "" || outputDirectory == "" || credential == "" {
		t.Skipf("set %s, %s and %s to measure LoCoMo accuracy", locomoDatasetVariable, locomoOutputVariable, locomoCredentialVariable)
	}
	conversationIndex, errorValue := strconv.Atoi(os.Getenv(locomoConversationVariable))
	if errorValue != nil {
		t.Fatalf("%s: %v", locomoConversationVariable, errorValue)
	}
	sessionLimit, errorValue := strconv.Atoi(os.Getenv(locomoSessionLimitVariable))
	if errorValue != nil {
		t.Fatalf("%s: %v", locomoSessionLimitVariable, errorValue)
	}
	records, errorValue := loadLocomoConversations(datasetPath)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	record := records[conversationIndex]
	ingested := record.Sessions[:min(sessionLimit, len(record.Sessions))]
	ctx := context.Background()
	arm := os.Getenv(locomoArmVariable)
	storeArm := arm
	if named := os.Getenv("BLUEMEMO_LOCOMO_STORE_ARM"); named != "" {
		storeArm = named
	}
	rig := openLocomoRig(t, filepath.Join(outputDirectory, fmt.Sprintf("%s-%d.db", storeArm, conversationIndex)), conversationIndex, credential)
	ingestStarted := time.Now()
	settleFailures := 0
	if !reusesStore() {
		settleFailures = rig.ingest(ctx, t, ingested)
	}
	living, errorValue := rig.store.Memories(ctx)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if len(living) == 0 {
		t.Fatalf("the store at %s holds no memory, so every answer would be a refusal", rig.path)
	}
	ingestSeconds := time.Since(ingestStarted).Seconds()
	if reembedsBeforeAsking() {
		report, errorValue := rig.store.Reembed(ctx, 64)
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		t.Logf("reembedded: %+v", report)
	}
	questionStarted := time.Now()
	outcomes := rig.askAll(ctx, questionsInside(record, len(ingested)))
	result := locomoResult{
		Arm: arm, SampleID: record.SampleID, IngestedSessions: len(ingested), Outcomes: outcomes,
		Memories: rig.describeMemories(ctx, t), Cost: rig.ledger.snapshot(), SettleFailures: settleFailures,
		IngestSeconds: ingestSeconds, QuestionSeconds: time.Since(questionStarted).Seconds(),
	}
	writeLocomoResult(t, filepath.Join(outputDirectory, fmt.Sprintf("%s-%d.json", arm, conversationIndex)), result)
	t.Logf("done: %d questions, %d live memories, %d dated", len(outcomes), result.Memories.Live, result.Memories.Dated)
}

func writeLocomoResult(t *testing.T, path string, result locomoResult) {
	t.Helper()
	encoded, errorValue := json.MarshalIndent(result, "", "  ")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if errorValue := os.WriteFile(path, encoded, 0o600); errorValue != nil {
		t.Fatal(errorValue)
	}
}
