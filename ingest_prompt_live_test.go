package bluememo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type ollamaStructuredModel struct{ instruction string }

func (model ollamaStructuredModel) GenerateStructured(ctx context.Context, request StructuredRequest) (string, error) {
	var schema any
	json.Unmarshal([]byte(request.SchemaDocument), &schema)
	body, _ := json.Marshal(map[string]any{
		"model":  "qwen3.5:4b",
		"stream": false, "think": false,
		"format": schema,
		"messages": []map[string]string{
			{"role": "system", "content": model.instruction},
			{"role": "user", "content": request.Subject},
		},
		"options": map[string]any{"temperature": 0, "num_predict": 2000},
	})
	httpRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost:11434/api/chat", bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	response, errorValue := http.DefaultClient.Do(httpRequest)
	if errorValue != nil {
		return "", errorValue
	}
	defer response.Body.Close()
	var decoded struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	json.NewDecoder(response.Body).Decode(&decoded)
	return decoded.Message.Content, nil
}

func TestIngestInstructionPreservesKoreanNamesLive(t *testing.T) {
	if os.Getenv("BLUEMEMO_LIVE_MODEL") == "" {
		t.Skip("set BLUEMEMO_LIVE_MODEL to run the live ingest prompt check")
	}
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	request := IngestRequest{
		Episode:       Episode{EpisodeID: "e-live", SourceKind: EpisodeSourceKindExplicit, Content: "내 이름은 이동하고, 여명거리 CTO야. 김여명은 회의 길게 끄는 걸 싫어해.", OccurredAt: now},
		Reader:        NewReader("p-dongha", nil, nil, 0, nil),
		RequesterName: "이동하",
		KnownPeople:   map[string]string{"이동하": "p-dongha", "김여명": "p-yeomyeong"},
	}
	subject := IngestSubject(request, nil, now)

	previousInstruction := IngestInstruction[:strings.Index(IngestInstruction, "\n\nA source saying")]
	for instructionName, instruction := range map[string]string{"이전": previousInstruction, "현행": IngestInstruction} {
		raw, errorValue := ollamaStructuredModel{instruction: instruction}.GenerateStructured(context.Background(), StructuredRequest{
			SchemaDocument: IngestSchemaDocument, Subject: subject,
		})
		if errorValue != nil {
			t.Fatalf("%s: %v", instructionName, errorValue)
		}
		var output struct {
			Facts []struct {
				Content string `json:"content"`
				Kind    string `json:"kind"`
			} `json:"facts"`
		}
		if errorValue := json.Unmarshal([]byte(strings.TrimSpace(raw)), &output); errorValue != nil {
			t.Fatalf("%s: output is not the schema: %v (raw %d bytes: %.400q)", instructionName, errorValue, len(raw), raw)
		}
		namePreserved, nameCorrupted, tautology := false, "", ""
		for _, fact := range output.Facts {
			t.Logf("  [%s] %s (%s)", instructionName, fact.Content, fact.Kind)
			if strings.Contains(fact.Content, "이동하") {
				namePreserved = true
			}
			for _, corruption := range []string{"이동하고", "이동은", "이동이", "이동한다"} {
				if strings.Contains(fact.Content, corruption) {
					nameCorrupted = corruption
				}
			}
			if strings.Contains(fact.Content, "이름은 이동하") {
				tautology = fact.Content
			}
		}
		t.Logf("%s → 이름 보존 %v · 훼손 %q · 동어반복 %q", instructionName, namePreserved, nameCorrupted, tautology)
		if instructionName != "현행" {
			continue
		}
		if !namePreserved {
			t.Errorf("current instruction dropped the requester name entirely")
		}
		if nameCorrupted != "" {
			t.Errorf("current instruction corrupted the name into %q; a Korean particle is not part of the name", nameCorrupted)
		}
		if tautology != "" {
			t.Errorf("current instruction produced a fact that only restates the name: %q", tautology)
		}
	}
}
