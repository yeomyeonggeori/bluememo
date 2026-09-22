package bluememo

import (
	"context"
	"errors"
	"strings"
)

const (
	TriggerPhraseCharacterLimit = 80
	TriggerPhrasesPerFact       = 4
	TriggerSpecificityMargin    = 0.05
)

type FactTrigger struct {
	TriggerID      string `json:"triggerID"`
	FactID         string `json:"factID"`
	Phrase         string `json:"phrase"`
	EmbeddingModel string `json:"embeddingModel"`
}

const TriggerSchemaDocument = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["phrases"],
  "properties": {
    "phrases": {
      "type": "array",
      "maxItems": 4,
      "items": {"type": "string", "maxLength": 80}
    }
  }
}`

const TriggerInstruction = `A memory is only found when a question resembles it. Your job is to write the short phrases a person would use in the situations where this memory matters, so that it can be found even when their words and its words have nothing in common.

Write at most four noun phrases of two to six words each, in the language the memory uses. Two kinds are useful:
- the name of the category the memory belongs to, one or two steps more general than the memory itself;
- a concrete situation, decision, or object that, when it comes up, makes this memory relevant.

A phrase is wasted when it restates the memory in other words, when it is so general that it would fit any memory, or when the situation it names would call up many unrelated memories instead of this one.

If you would have to invent something to reach four, return fewer. An empty list is a correct answer for a memory that only ever matters when asked about directly.

For 박예시는 아침에만 커피를 마신다, good phrases are 음료 취향, 아침 회의 준비, 카페 주문. 커피를 마시는 습관 is a restatement, and 사무실 생활 would fit anything.`

type TriggerRepository interface {
	SaveFactTriggers(ctx context.Context, triggers []FactTrigger, embeddings [][]float32) error
	SearchTriggers(ctx context.Context, query FactSearchQuery) ([]RankedFact, error)
	DeleteFactTriggers(ctx context.Context, factIDs []string) error
	ListTriggerPhrases(ctx context.Context, factIDs []string) (map[string][]string, error)
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
		if len(normalized) == TriggerPhrasesPerFact {
			break
		}
	}
	return normalized
}

func SelectSpecificTriggers(phrases []string, phraseEmbeddings [][]float32, ownEmbedding []float32, otherEmbeddings [][]float32) ([]string, [][]float32) {
	keptPhrases := []string{}
	keptEmbeddings := [][]float32{}
	for index, phrase := range phrases {
		if index >= len(phraseEmbeddings) {
			break
		}
		ownSimilarity := cosineSimilarity(phraseEmbeddings[index], ownEmbedding)
		if ownSimilarity <= meanSimilarity(phraseEmbeddings[index], otherEmbeddings)+TriggerSpecificityMargin {
			continue
		}
		keptPhrases = append(keptPhrases, phrase)
		keptEmbeddings = append(keptEmbeddings, phraseEmbeddings[index])
	}
	return keptPhrases, keptEmbeddings
}

func meanSimilarity(embedding []float32, others [][]float32) float64 {
	if len(others) == 0 {
		return 0
	}
	total := 0.0
	for _, other := range others {
		total += cosineSimilarity(embedding, other)
	}
	return total / float64(len(others))
}

var ErrTriggersUnavailable = errors.New("memory trigger repository is not configured")
