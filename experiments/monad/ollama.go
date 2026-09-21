package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type ollamaClient struct {
	baseURL         string
	generationModel string
	embeddingModel  string
	speakerName     string
	today           time.Time
	httpClient      *http.Client
}

func newOllamaClient(generationModel string, speakerName string, today time.Time) *ollamaClient {
	return &ollamaClient{
		baseURL:         "http://127.0.0.1:11434",
		generationModel: generationModel,
		embeddingModel:  "embeddinggemma",
		speakerName:     speakerName,
		today:           today,
		httpClient:      &http.Client{Timeout: 10 * time.Minute},
	}
}

const decomposerInstruction = `너는 기억 저장소의 분해기다. 사용자가 보낸 텍스트를 자립적인 명제들로 쪼갠다.

자립적이라는 것:
- 대명사와 생략을 모두 푼다. "나", "내", "그", "거기"는 실제 이름으로 바꾼다.
- 명제 하나만 따로 읽어도 완전한 문장이어야 한다.
- 조건, 시점, 출처, 정도를 버리지 않고 명제 안에 남긴다.
- 전해 들은 말은 출처를 명제 안에 포함한다. 출처를 별도 명제로 분리하지 않는다.
- 명제는 평서형 종결로 쓴다. "좋아해" 대신 "좋아한다".

kind:
- identity: 누가 누구인가. 이름, 역할, 소속.
- preference: 원하는 것, 선호하는 방식, 요청받은 응대 방식.
- fact: 참으로 유지되는 단언.
- episode: 언제 무슨 일이 있었나.
- procedure: 어떤 일을 하는 방법이나 절차, 단계.
- unknown: 위 어디에도 명확히 속하지 않을 때.

validUntil: 그 명제가 특정 날짜 이후 더는 참이 아니면 ISO 날짜(YYYY-MM-DD). 아니면 빈 문자열.

금지:
- 원문에 없는 사실을 추가하지 마라.
- 원문이 말하지 않은 것을 추론해 채우지 마라. 애매하면 unknown을 골라라.
- 아래 맥락 자체를 명제로 만들지 마라. 맥락은 대명사를 푸는 데만 쓴다.
- 이름의 철자를 바꾸거나 줄이지 마라. 조사를 이름에 붙이지 마라.

예시 입력: 어제 최견본이랑 회의했어. 나는 회의록을 항상 마크다운으로 받고 싶어.
예시 출력의 명제 둘:
  episode: "이동하는 2026-09-20에 최견본과 회의했다."
  preference: "이동하는 회의록을 항상 마크다운으로 받고 싶어 한다."

잘못된 출력의 예 (절대 이렇게 하지 마라):
  입력: "내 이름은 이동하고, 여명거리 CTO야."
  틀린 명제: "이동하는 이름은 이동한다."   ← "이동하고"를 "이동"+"하고"로 잘못 나눴다
  옳은 명제: "이동하는 여명거리의 CTO이다."
화자의 이름은 맥락에 주어진 문자열 그대로다. 그 문자열을 더 짧게 자르지 마라.`

var decompositionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"propositions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content":    map[string]any{"type": "string"},
					"kind":       map[string]any{"type": "string", "enum": []string{"identity", "preference", "fact", "episode", "procedure", "unknown"}},
					"validUntil": map[string]any{"type": "string"},
				},
				"required":             []string{"content", "kind", "validUntil"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"propositions"},
	"additionalProperties": false,
}

func (client *ollamaClient) Decompose(ctx context.Context, text string) ([]Monad, error) {
	systemPrompt := fmt.Sprintf("%s\n\n맥락\n화자: %s\n오늘: %s",
		decomposerInstruction, client.speakerName, client.today.Format("2006-01-02"))

	requestBody := map[string]any{
		"model":   client.generationModel,
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": text},
		},
		"format": decompositionSchema,
	}

	var response struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if errorValue := client.postJSON(ctx, "/api/chat", requestBody, &response); errorValue != nil {
		return nil, errorValue
	}

	var decoded struct {
		Propositions []Monad `json:"propositions"`
	}
	if errorValue := json.Unmarshal([]byte(response.Message.Content), &decoded); errorValue != nil {
		return nil, fmt.Errorf("decomposer returned unparseable structured output: %w", errorValue)
	}

	monads := make([]Monad, 0, len(decoded.Propositions))
	for _, monad := range decoded.Propositions {
		if validateMonad(monad) != nil {
			continue
		}
		monads = append(monads, monad)
	}
	return monads, nil
}

func (client *ollamaClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	requestBody := map[string]any{"model": client.embeddingModel, "input": texts}
	if errorValue := client.postJSON(ctx, "/api/embed", requestBody, &response); errorValue != nil {
		return nil, errorValue
	}
	if len(response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(response.Embeddings), len(texts))
	}
	return response.Embeddings, nil
}

func (client *ollamaClient) postJSON(ctx context.Context, path string, body any, target any) error {
	encoded, errorValue := json.Marshal(body)
	if errorValue != nil {
		return errorValue
	}
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(encoded))
	if errorValue != nil {
		return errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	response, errorValue := client.httpClient.Do(request)
	if errorValue != nil {
		return errorValue
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s returned %d", path, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

const judgeInstruction = `너는 기억 저장소의 통합 판정자다. 새 명제 하나와, 저장소가 이미 갖고 있는 후보 명제 목록을 받는다.
새 명제가 후보들 중 하나에 대해 어떤 관계인지 딱 하나만 고른다.

same      후보와 같은 사실을 말한다. 표현만 다르고 새로 알게 된 것이 없다.
updates   후보가 말하던 것이 더는 참이 아니게 만든다. 같은 대상의 값이 바뀌었다.
extends   후보를 무효화하지 않으면서 세부를 더한다. 둘 다 참으로 남는다.
unrelated 후보들과 관계가 없다.
noise     기억할 가치가 없는 잡담이거나 내용이 없다.

targetIndex 는 관계를 맺는 후보의 번호다. unrelated 와 noise 는 -1 을 쓴다.

same 은 같은 대상에 대한 같은 종류의 말이라는 뜻이 아니다. 같은 것을 서술해야 same 이다.
확신이 없으면 unrelated 를 골라라. 잘못 묶으면 사실이 영영 사라지고, 잘못 나누면 중복이 남을 뿐이다.

잘못된 판정의 예 (절대 이렇게 하지 마라):
  새 명제: "이동하는 여명거리의 CTO이다."
  후보 0: "이동하의 이름은 이동하이다."
  틀린 판정: same   ← 둘 다 이동하에 대한 신원 서술이지만 서술하는 것이 다르다
  옳은 판정: unrelated

  새 명제: "이동하는 사과보다 배를 더 좋아한다."
  후보 0: "이동하는 사과를 좋아한다."
  틀린 판정: updates   ← 후보가 거짓이 되지 않았다
  옳은 판정: extends`

var judgeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"relation":    map[string]any{"type": "string", "enum": []string{"same", "updates", "extends", "unrelated", "noise"}},
		"targetIndex": map[string]any{"type": "integer"},
	},
	"required":             []string{"relation", "targetIndex"},
	"additionalProperties": false,
}

func (client *ollamaClient) Judge(ctx context.Context, proposition string, candidates []string) (Relation, int, error) {
	listing := ""
	for index, candidate := range candidates {
		listing += fmt.Sprintf("%d. %s\n", index, candidate)
	}
	userMessage := fmt.Sprintf("새 명제:\n%s\n\n이미 갖고 있는 후보:\n%s", proposition, listing)

	requestBody := map[string]any{
		"model":   client.generationModel,
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0},
		"messages": []map[string]string{
			{"role": "system", "content": judgeInstruction},
			{"role": "user", "content": userMessage},
		},
		"format": judgeSchema,
	}
	var response struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if errorValue := client.postJSON(ctx, "/api/chat", requestBody, &response); errorValue != nil {
		return "", -1, errorValue
	}
	var decoded struct {
		Relation    Relation `json:"relation"`
		TargetIndex int      `json:"targetIndex"`
	}
	if errorValue := json.Unmarshal([]byte(response.Message.Content), &decoded); errorValue != nil {
		return "", -1, fmt.Errorf("judge returned unparseable structured output: %w", errorValue)
	}
	return decoded.Relation, decoded.TargetIndex, nil
}
