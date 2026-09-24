package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

const DefaultBaseURL = "http://127.0.0.1:11434"

type Client struct {
	BaseURL         string
	GenerationModel string
	EmbeddingModel  string
	HTTPClient      *http.Client
}

func New(generationModel string, embeddingModel string) Client {
	return Client{
		BaseURL:         DefaultBaseURL,
		GenerationModel: generationModel,
		EmbeddingModel:  embeddingModel,
		HTTPClient:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (client Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	embeddings, errorValue := client.EmbedDocuments(ctx, []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	return embeddings[0], nil
}

func (client Client) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	request := map[string]any{"model": client.EmbeddingModel, "input": texts}
	if errorValue := client.post(ctx, "/api/embed", request, &response); errorValue != nil {
		return nil, errorValue
	}
	if len(response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d texts", len(response.Embeddings), len(texts))
	}
	return response.Embeddings, nil
}

func (client Client) GenerateStructured(ctx context.Context, request bluememo.StructuredRequest) (string, error) {
	var response chatResponse
	body := client.chatRequest(request.Instruction, request.Subject)
	body["format"] = json.RawMessage(request.SchemaDocument)
	if errorValue := client.post(ctx, "/api/chat", body, &response); errorValue != nil {
		return "", errorValue
	}
	return response.Message.Content, nil
}

func (client Client) Choose(ctx context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	var response chatResponse
	body := client.chatRequest(request.Instruction, request.Subject)
	body["options"] = map[string]any{"temperature": 0, "num_predict": 1}
	body["logprobs"] = true
	body["top_logprobs"] = 20
	if errorValue := client.post(ctx, "/api/chat", body, &response); errorValue != nil {
		return nil, errorValue
	}
	if len(response.Logprobs) == 0 {
		return nil, fmt.Errorf("ollama returned no logprobs for %s; it needs version 0.12 or later", client.GenerationModel)
	}
	distribution := map[string]float64{}
	for _, candidate := range response.Logprobs[0].TopLogprobs {
		distribution[strings.TrimSpace(candidate.Token)] += math.Exp(candidate.Logprob)
	}
	return distribution, nil
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Logprobs []struct {
		TopLogprobs []struct {
			Token   string  `json:"token"`
			Logprob float64 `json:"logprob"`
		} `json:"top_logprobs"`
	} `json:"logprobs"`
}

func (client Client) chatRequest(instruction string, subject string) map[string]any {
	return map[string]any{
		"model":   client.GenerationModel,
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0},
		"messages": []map[string]string{
			{"role": "system", "content": instruction},
			{"role": "user", "content": subject},
		},
	}
}

func (client Client) post(ctx context.Context, path string, body any, target any) error {
	encoded, errorValue := json.Marshal(body)
	if errorValue != nil {
		return errorValue
	}
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+path, bytes.NewReader(encoded))
	if errorValue != nil {
		return errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	response, errorValue := client.HTTPClient.Do(request)
	if errorValue != nil {
		return fmt.Errorf("ollama %s: %w", path, errorValue)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s returned %d", path, response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}
