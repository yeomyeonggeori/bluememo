package bluememo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TriggerPhraseCharacterLimit = 80
	TriggerPhrasesPerMemory     = 4
	TriggerSpecificityMargin    = 0.05
)

var TriggerSchemaDocument = mustMarshalSchema(map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"phrases"},
	"properties": map[string]any{
		"phrases": map[string]any{
			"type":     "array",
			"maxItems": TriggerPhrasesPerMemory,
			"items":    map[string]any{"type": "string", "maxLength": TriggerPhraseCharacterLimit},
		},
	},
})

const TriggerInstruction = `A memory is only found when a question resembles it. Your job is to write the short phrases a person would use in the situations where this memory matters, so that it can be found even when their words and its words have nothing in common.

Write at most four noun phrases of two to six words each, in the language the memory uses. Two kinds are useful:
- the name of the category the memory belongs to, one or two steps more general than the memory itself;
- a concrete situation, decision, or object that, when it comes up, makes this memory relevant.

A phrase is wasted when it restates the memory in other words, when it is so general that it would fit any memory, or when the situation it names would call up many unrelated memories instead of this one.

If you would have to invent something to reach four, return fewer. An empty list is a correct answer for a memory that only ever matters when asked about directly.

For "Jordan drinks coffee only in the morning", good phrases are drink preferences, preparing a morning meeting, a cafe order. "coffee drinking habit" is a restatement, and "office life" would fit anything.`

type rehearsalOutput struct {
	Phrases []string `json:"phrases"`
}

func (store *Store) rehearse(ctx context.Context, memory Memory, ownEmbedding []float32) (int, error) {
	phrases, errorValue := store.askForTriggers(ctx, memory.Content)
	if errorValue != nil || len(phrases) == 0 {
		return 0, errorValue
	}
	phraseEmbeddings, errorValue := store.configuration.Embedder.EmbedDocuments(ctx, phrases)
	if errorValue != nil {
		return 0, fmt.Errorf("trigger phrase embedding failed: %w", errorValue)
	}
	if len(phraseEmbeddings) != len(phrases) {
		return 0, fmt.Errorf("embedder returned %d embeddings for %d trigger phrases", len(phraseEmbeddings), len(phrases))
	}
	otherEmbeddings, errorValue := store.liveEmbeddingsExcept(ctx, memory.MemoryID)
	if errorValue != nil {
		return 0, errorValue
	}
	keptPhrases, keptEmbeddings := SelectSpecificTriggers(phrases, phraseEmbeddings, ownEmbedding, otherEmbeddings)
	return len(phrases) - len(keptPhrases), store.saveTriggers(ctx, memory.MemoryID, keptPhrases, keptEmbeddings)
}

func (store *Store) askForTriggers(ctx context.Context, content string) ([]string, error) {
	response, errorValue := store.configuration.Model.GenerateStructured(ctx, StructuredRequest{
		SchemaName:     "memory_trigger",
		SchemaDocument: TriggerSchemaDocument,
		Instruction:    TriggerInstruction,
		Subject:        "Memory: " + content,
	})
	if errorValue != nil {
		return nil, fmt.Errorf("rehearsal model call failed: %w", errorValue)
	}
	var output rehearsalOutput
	if errorValue := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); errorValue != nil {
		return nil, fmt.Errorf("rehearsal output is not the schema: %w", errorValue)
	}
	return NormalizeTriggerPhrases(output.Phrases), nil
}

func (store *Store) saveTriggers(ctx context.Context, memoryID string, phrases []string, embeddings [][]float32) error {
	transaction, errorValue := store.database.BeginTx(ctx, nil)
	if errorValue != nil {
		return errorValue
	}
	defer transaction.Rollback()
	if _, errorValue := transaction.ExecContext(ctx, `delete from memory_trigger where memory_id = ?`, memoryID); errorValue != nil {
		return errorValue
	}
	for index, phrase := range phrases {
		if _, errorValue := transaction.ExecContext(ctx, `
			insert into memory_trigger (trigger_id, memory_id, phrase, embedding_model, embedding) values (?, ?, ?, ?, ?)`,
			NewIdentifier(), memoryID, phrase, store.configuration.EmbeddingModel, encodeEmbedding(embeddings[index])); errorValue != nil {
			return errorValue
		}
	}
	return transaction.Commit()
}

func (store *Store) liveEmbeddingsExcept(ctx context.Context, memoryID string) ([][]float32, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select embedding from memory
		where memory_id <> ? and cold_since is null and embedding_model = ? and embedding is not null`,
		memoryID, store.configuration.EmbeddingModel)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	embeddings := [][]float32{}
	for rows.Next() {
		var encoded []byte
		if errorValue := rows.Scan(&encoded); errorValue != nil {
			return nil, errorValue
		}
		embeddings = append(embeddings, decodeEmbedding(encoded))
	}
	return embeddings, rows.Err()
}

func (store *Store) TriggerPhrases(ctx context.Context, memoryID string) ([]string, error) {
	rows, errorValue := store.database.QueryContext(ctx, `select phrase from memory_trigger where memory_id = ? order by phrase`, memoryID)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()
	phrases := []string{}
	for rows.Next() {
		var phrase string
		if errorValue := rows.Scan(&phrase); errorValue != nil {
			return nil, errorValue
		}
		phrases = append(phrases, phrase)
	}
	return phrases, rows.Err()
}

func NormalizeTriggerPhrases(phrases []string) []string {
	normalized := []string{}
	seen := map[string]bool{}
	for _, phrase := range phrases {
		trimmed := strings.TrimSpace(phrase)
		if trimmed == "" || len([]rune(trimmed)) > TriggerPhraseCharacterLimit || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
		if len(normalized) == TriggerPhrasesPerMemory {
			break
		}
	}
	return normalized
}

func SelectSpecificTriggers(phrases []string, phraseEmbeddings [][]float32, ownEmbedding []float32, otherEmbeddings [][]float32) ([]string, [][]float32) {
	keptPhrases := []string{}
	keptEmbeddings := [][]float32{}
	for index, phrase := range phrases {
		ownSimilarity := cosineSimilarity(phraseEmbeddings[index], ownEmbedding)
		if ownSimilarity <= meanSimilarity(phraseEmbeddings[index], otherEmbeddings)+TriggerSpecificityMargin {
			continue
		}
		keptPhrases = append(keptPhrases, phrase)
		keptEmbeddings = append(keptEmbeddings, phraseEmbeddings[index])
	}
	return keptPhrases, keptEmbeddings
}
