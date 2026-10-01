package bluememo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
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

type locomoTransport struct {
	credential string
	client     *http.Client
	ledger     *locomoLedger
}

func (transport locomoTransport) post(ctx context.Context, component string, url string, payload any) ([]byte, error) {
	requestBody, errorValue := json.Marshal(payload)
	if errorValue != nil {
		return nil, errorValue
	}
	var lastError error
	for attempt := range locomoAttemptLimit {
		if attempt > 0 {
			if errorValue := sleepWithJitter(ctx, attempt); errorValue != nil {
				return nil, errorValue
			}
		}
		responseBody, isRetryable, errorValue := transport.postOnce(ctx, url, requestBody)
		if errorValue == nil {
			transport.ledger.record(component, costOf(responseBody))
			return responseBody, nil
		}
		lastError = errorValue
		if !isRetryable {
			return nil, errorValue
		}
	}
	return nil, fmt.Errorf("%s gave up after %d attempts: %w", component, locomoAttemptLimit, lastError)
}

func sleepWithJitter(ctx context.Context, attempt int) error {
	delay := min(locomoBackoffBase<<(attempt-1), locomoBackoffCeiling)
	delay += time.Duration(rand.Int63n(int64(delay)/2 + 1))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (transport locomoTransport) postOnce(ctx context.Context, url string, requestBody []byte) ([]byte, bool, error) {
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
	if errorValue != nil {
		return nil, false, errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+transport.credential)
	response, errorValue := transport.client.Do(request)
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

func costOf(responseBody []byte) float64 {
	var parsed struct {
		Usage struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return 0
	}
	return parsed.Usage.Cost
}

type locomoEmbedder struct {
	transport locomoTransport
}

func (embedder locomoEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	embeddings, errorValue := embedder.embed(ctx, "embed query", []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	return embeddings[0], nil
}

func (embedder locomoEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return embedder.embed(ctx, "embed document", texts)
}

func (embedder locomoEmbedder) embed(ctx context.Context, component string, texts []string) ([][]float32, error) {
	responseBody, errorValue := embedder.transport.post(ctx, component, locomoEmbeddingURL, map[string]any{"model": locomoEmbeddingModel, "input": texts})
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
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response holds %d vectors for %d texts", len(parsed.Data), len(texts))
	}
	embeddings := make([][]float32, len(parsed.Data))
	for index, entry := range parsed.Data {
		embeddings[index] = entry.Embedding
	}
	return embeddings, nil
}

type locomoModel struct {
	transport locomoTransport
}

func (model locomoModel) GenerateStructured(ctx context.Context, request bluememo.StructuredRequest) (string, error) {
	return model.structured(ctx, request.SchemaName, request.SchemaDocument, request.Instruction, request.Subject)
}

func (model locomoModel) structured(ctx context.Context, name string, schemaDocument string, instruction string, subject string) (string, error) {
	var schema any
	if errorValue := json.Unmarshal([]byte(schemaDocument), &schema); errorValue != nil {
		return "", fmt.Errorf("schema %s is not JSON: %w", name, errorValue)
	}
	responseBody, errorValue := model.transport.post(ctx, name, locomoChatURL, map[string]any{
		"model": locomoChatModel,
		"messages": []map[string]string{
			{"role": "system", "content": instruction},
			{"role": "user", "content": subject},
		},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": strings.ReplaceAll(name, " ", "_"), "strict": true, "schema": schema},
		},
	})
	if errorValue != nil {
		return "", errorValue
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat response for %s is not the expected shape: %s", name, responseBody)
	}
	return parsed.Choices[0].Message.Content, nil
}

type locomoDecisions struct {
	transport locomoTransport
}

type locomoDecisionsResponse struct {
	Answers map[string]struct {
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
}

func (decisions locomoDecisions) choose(ctx context.Context, component string, state string, instruction string, criteria map[string]string) (map[string]float64, error) {
	responseBody, errorValue := decisions.transport.post(ctx, component, locomoDecisionsURL, map[string]any{
		"model": locomoDecisionsModel,
		"state": state,
		"questions": map[string]any{"answer": map[string]any{
			"type":         "choice",
			"instructions": instruction,
			"criteria":     criteria,
		}},
	})
	if errorValue != nil {
		return nil, errorValue
	}
	var parsed locomoDecisionsResponse
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return nil, fmt.Errorf("decisions response is not the expected shape: %w", errorValue)
	}
	answer, isPresent := parsed.Answers["answer"]
	if !isPresent || len(answer.Probabilities) == 0 {
		return nil, fmt.Errorf("decisions response carries no probabilities: %s", responseBody)
	}
	return answer.Probabilities, nil
}

func (decisions locomoDecisions) Choose(ctx context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	criteria, errorValue := criteriaFor(request)
	if errorValue != nil {
		return nil, errorValue
	}
	glosses := make(map[string]string, len(criteria))
	for _, criterion := range criteria {
		glosses[criterion.Key] = criterion.Gloss
	}
	return decisions.choose(ctx, "judge", request.Subject, request.Instruction, glosses)
}

func (decisions locomoDecisions) Rerank(ctx context.Context, query string, contents []string) ([]float64, error) {
	scores := make([]float64, len(contents))
	if len(contents) < 2 {
		return scores, nil
	}
	criteria := make(map[string]string, len(contents))
	for index, content := range contents {
		criteria["m"+strconv.Itoa(index)] = content
	}
	probabilities, errorValue := decisions.choose(ctx, "rerank", query, "Which memory answers this question?", criteria)
	if errorValue != nil {
		return nil, errorValue
	}
	winner, bestProbability := 0, -1.0
	for index := range contents {
		if probability := probabilities["m"+strconv.Itoa(index)]; probability > bestProbability {
			winner, bestProbability = index, probability
		}
	}
	scores[winner] = 1
	return scores, nil
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

func isMonthsLater(first time.Time, last time.Time, monthCount int) bool {
	dayAfterLast := last.AddDate(0, 0, 1)
	boundary := time.Date(first.Year(), first.Month()+time.Month(monthCount), 1, 0, 0, 0, 0, first.Location())
	return dayAfterLast.Year() == boundary.Year() && dayAfterLast.YearDay() == boundary.YearDay()
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
A section headed "What was said" may follow, holding the conversation the memories were drawn from; read it for detail a memory left out.\nIf the memories support or imply an answer, give it briefly. If nothing in the memories bears on the question, say that the information is not available.`

const locomoCorrectnessInstruction = `You grade an answer against a gold answer for a question.
Mark it correct when the answer contains the same facts as the gold answer, however it is worded or how much extra it says. For a question about time, it is correct when it names the same date or period as the gold answer, even in another format.
An answer that says the information is not available is incorrect.
Do not add requirements the gold answer does not state. Wording, formatting and extra detail are not failures.`

const locomoDeclineInstruction = `You read a question and an answer to it. Decide whether the answer declines, meaning it says the information is not available, not mentioned or not known, or whether it asserts a specific fact in answer to the question.
Judge only from the question and the answer. Do not add requirements the answer does not state.`

const (
	locomoAnswerSchema      = `{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string"}}}`
	locomoCorrectnessSchema = `{"type":"object","additionalProperties":false,"required":["isCorrect"],"properties":{"isCorrect":{"type":"boolean"}}}`
	locomoDeclineSchema     = `{"type":"object","additionalProperties":false,"required":["declines"],"properties":{"declines":{"type":"boolean"}}}`
)

type locomoOutcome struct {
	Category      int      `json:"category"`
	Question      string   `json:"question"`
	Gold          string   `json:"gold"`
	Answer        string   `json:"answer"`
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
	store     *bluememo.Store
	clock     *locomoClock
	model     locomoModel
	transport locomoTransport
}

func openLocomoRig(t *testing.T, path string, conversationIndex int, credential string) *locomoRig {
	t.Helper()
	ledger := &locomoLedger{}
	transport := locomoTransport{credential: credential, client: &http.Client{Timeout: 3 * time.Minute}, ledger: ledger}
	clock := &locomoClock{current: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	decisions := locomoDecisions{transport: transport}
	model := locomoModel{transport: transport}
	store, errorValue := bluememo.Open(context.Background(), path, bluememo.Configuration{
		Embedder:           locomoEmbedder{transport: transport},
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
	return &locomoRig{store: store, clock: clock, model: model, transport: transport}
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

func (rig *locomoRig) answer(ctx context.Context, question locomoQuestion, result bluememo.RecallResult) (string, error) {
	subject := "Memories:\n" + numberedContext(result.Memories) + spokenContext(result) + "\nQuestion: " + question.Question
	response, errorValue := rig.model.structured(ctx, "answer", locomoAnswerSchema, locomoAnswerInstruction, subject)
	if errorValue != nil {
		return "", errorValue
	}
	var parsed struct {
		Answer string `json:"answer"`
	}
	if errorValue := json.Unmarshal([]byte(response), &parsed); errorValue != nil {
		return "", fmt.Errorf("answer is not the schema: %w", errorValue)
	}
	return parsed.Answer, nil
}

func (rig *locomoRig) judgeDecline(ctx context.Context, question locomoQuestion, answer string) (bool, error) {
	response, errorValue := rig.model.structured(ctx, "judge decline", locomoDeclineSchema, locomoDeclineInstruction, "Question: "+question.Question+"\nAnswer: "+answer)
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
	response, errorValue := rig.model.structured(ctx, "judge correctness", locomoCorrectnessSchema, locomoCorrectnessInstruction, subject)
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
	outcome.Answer, errorValue = rig.answer(ctx, question, result)
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
	rig := openLocomoRig(t, filepath.Join(outputDirectory, fmt.Sprintf("%s-%d.db", arm, conversationIndex)), conversationIndex, credential)
	ingestStarted := time.Now()
	settleFailures := 0
	if !reusesStore() {
		settleFailures = rig.ingest(ctx, t, ingested)
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
		Memories: rig.describeMemories(ctx, t), Cost: rig.transport.ledger.snapshot(), SettleFailures: settleFailures,
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
