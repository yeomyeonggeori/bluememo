package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

const (
	EmbeddingURL      = "https://openrouter.ai/api/v1/embeddings"
	ChatURL           = "https://openrouter.ai/api/v1/chat/completions"
	DecisionsURL      = "https://openrouter.ai/api/alpha/decisions"
	DefaultEmbedModel = "perplexity/pplx-embed-v1-4b"
	DefaultChatModel  = "openai/gpt-6-luna"

	attemptLimit    = 5
	backoffBase     = 2 * time.Second
	backoffCeiling  = 32 * time.Second
	requestDeadline = 5 * time.Minute
)

// ErrOutputTruncated is the model stopping because it reached its output
// limit. The answer it gave is a prefix of the one it meant, so a caller
// parsing a schema sees malformed JSON and would otherwise blame the schema.
// DefaultOutputLimit is generous because a decomposition enumerates a text
// before it writes statements, so its output grows with the text twice over.
const DefaultOutputLimit = 16000

var ErrOutputTruncated = errors.New("the model reached its output limit")

type Client struct {
	Credential     string
	EmbeddingModel string
	ChatModel      string
	OutputLimit    int
	HTTPClient     *http.Client
	RecordCost     func(component string, cost float64)
}

func New(credential string) *Client {
	return &Client{
		Credential:     credential,
		EmbeddingModel: DefaultEmbedModel,
		ChatModel:      DefaultChatModel,
		OutputLimit:    DefaultOutputLimit,
		HTTPClient:     &http.Client{Timeout: requestDeadline},
	}
}

func (client *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	embeddings, errorValue := client.embed(ctx, "embed query", []string{text})
	if errorValue != nil {
		return nil, errorValue
	}
	return embeddings[0], nil
}

func (client *Client) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return client.embed(ctx, "embed document", texts)
}

func (client *Client) embed(ctx context.Context, component string, texts []string) ([][]float32, error) {
	responseBody, errorValue := client.post(ctx, component, EmbeddingURL,
		map[string]any{"model": client.EmbeddingModel, "input": texts})
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

func (client *Client) GenerateStructured(ctx context.Context, request bluememo.StructuredRequest) (string, error) {
	return client.Structured(ctx, request.SchemaName, request.SchemaDocument, request.Instruction, request.Subject)
}

func (client *Client) Structured(ctx context.Context, name string, schemaDocument string, instruction string, subject string) (string, error) {
	var schema any
	if errorValue := json.Unmarshal([]byte(schemaDocument), &schema); errorValue != nil {
		return "", fmt.Errorf("schema %s is not JSON: %w", name, errorValue)
	}
	payload := map[string]any{
		"model": client.ChatModel,
		"messages": []map[string]string{
			{"role": "system", "content": instruction},
			{"role": "user", "content": subject},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": strings.ReplaceAll(name, " ", "_"), "strict": true, "schema": schema,
			},
		},
	}
	if client.OutputLimit > 0 {
		payload["max_tokens"] = client.OutputLimit
	}
	responseBody, errorValue := client.post(ctx, name, ChatURL, payload)
	if errorValue != nil {
		return "", errorValue
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat response for %s is not the expected shape: %s", name, responseBody)
	}
	choice := parsed.Choices[0]
	if choice.FinishReason == "length" {
		return "", fmt.Errorf("%s ran out of output before it finished: %w", name, ErrOutputTruncated)
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", fmt.Errorf("%s returned no content (finish reason %q)", name, choice.FinishReason)
	}
	return choice.Message.Content, nil
}

func (client *Client) post(ctx context.Context, component string, url string, payload any) ([]byte, error) {
	requestBody, errorValue := json.Marshal(payload)
	if errorValue != nil {
		return nil, errorValue
	}
	var lastError error
	for attempt := range attemptLimit {
		if attempt > 0 {
			if errorValue := sleepWithJitter(ctx, attempt); errorValue != nil {
				return nil, errorValue
			}
		}
		responseBody, isRetryable, errorValue := client.postOnce(ctx, url, requestBody)
		if errorValue == nil {
			if client.RecordCost != nil {
				client.RecordCost(component, CostOf(responseBody))
			}
			return responseBody, nil
		}
		lastError = errorValue
		if !isRetryable {
			return nil, errorValue
		}
	}
	return nil, fmt.Errorf("%s gave up after %d attempts: %w", component, attemptLimit, lastError)
}

func sleepWithJitter(ctx context.Context, attempt int) error {
	delay := min(backoffBase<<(attempt-1), backoffCeiling)
	delay += time.Duration(rand.Int63n(int64(delay)/2 + 1))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (client *Client) postOnce(ctx context.Context, url string, requestBody []byte) ([]byte, bool, error) {
	request, errorValue := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
	if errorValue != nil {
		return nil, false, errorValue
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.Credential)
	response, errorValue := client.HTTPClient.Do(request)
	if errorValue != nil {
		return nil, true, errorValue
	}
	defer response.Body.Close()
	responseBody, errorValue := io.ReadAll(response.Body)
	if errorValue != nil {
		return nil, true, errorValue
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusBadRequest || response.StatusCode >= http.StatusInternalServerError {
		return nil, true, fmt.Errorf("endpoint returned %d: %s", response.StatusCode, responseBody)
	}
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("endpoint returned %d: %s", response.StatusCode, responseBody)
	}
	return responseBody, false, nil
}

func CostOf(responseBody []byte) float64 {
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
