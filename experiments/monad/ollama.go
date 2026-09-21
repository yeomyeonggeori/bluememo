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

isStatic: 그 사람에 대해 바뀌지 않는 특성이면 true. 이름, 직업, 소속, 오래 가는 선호가 그렇다.
사건, 한 번의 요청, 상황 설명은 false.

validUntil: 그 명제가 특정 날짜 이후 더는 참이 아니면 ISO 날짜(YYYY-MM-DD). 아니면 빈 문자열.

금지:
- 원문에 없는 사실을 추가하지 마라.
- 원문이 말하지 않은 것을 추론해 채우지 마라. 애매하면 isStatic은 false 다.
- 아래 맥락 자체를 명제로 만들지 마라. 맥락은 대명사를 푸는 데만 쓴다.
- 이름의 철자를 바꾸거나 줄이지 마라. 조사를 이름에 붙이지 마라.

예시 입력: 어제 최견본이랑 회의했어. 나는 회의록을 항상 마크다운으로 받고 싶어.
예시 출력의 명제 둘:
  isStatic false: "이동하는 2026-09-20에 최견본과 회의했다."
  isStatic true:  "이동하는 회의록을 항상 마크다운으로 받고 싶어 한다."

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
					"isStatic":   map[string]any{"type": "boolean"},
					"validUntil": map[string]any{"type": "string"},
				},
				"required":             []string{"content", "isStatic", "validUntil"},
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
