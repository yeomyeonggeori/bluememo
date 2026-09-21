package bluememo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Rehearser struct {
	Store Store
	Model LanguageModel
}

type RehearseResult struct {
	EpisodeID    string `json:"episodeID"`
	FactCount    int    `json:"factCount"`
	PhraseCount  int    `json:"phraseCount"`
	DroppedCount int    `json:"droppedCount"`
}

type rehearseOutput struct {
	Phrases []string `json:"phrases"`
}

func (rehearser Rehearser) Rehearse(ctx context.Context, episodeID string) (RehearseResult, error) {
	if rehearser.Model == nil {
		return RehearseResult{}, errors.New("memory rehearsal has no language model")
	}
	if rehearser.Store.Facts == nil || rehearser.Store.Embedder == nil {
		return RehearseResult{}, errors.New("memory rehearsal has no fact repository or embedder")
	}
	if rehearser.Store.Triggers == nil {
		return RehearseResult{}, TerminalJobError{Cause: ErrTriggersUnavailable}
	}
	trimmedEpisodeID := strings.TrimSpace(episodeID)
	if trimmedEpisodeID == "" {
		return RehearseResult{}, TerminalJobError{Cause: errors.New("memory rehearsal needs an episode identifier")}
	}
	now := rehearser.Store.now()
	facts, errorValue := rehearser.Store.Facts.ListLiveFactsToRehearse(ctx, trimmedEpisodeID, now)
	if errorValue != nil {
		return RehearseResult{}, errorValue
	}
	result := RehearseResult{EpisodeID: trimmedEpisodeID, FactCount: len(facts)}
	for _, fact := range facts {
		phraseCount, droppedCount, errorValue := rehearser.rehearseFact(ctx, fact, now)
		if errorValue != nil {
			return result, errorValue
		}
		result.PhraseCount += phraseCount
		result.DroppedCount += droppedCount
	}
	return result, nil
}

func (rehearser Rehearser) rehearseFact(ctx context.Context, fact Fact, now time.Time) (int, int, error) {
	phrases, errorValue := rehearser.askModel(ctx, fact)
	if errorValue != nil {
		return 0, 0, errorValue
	}
	if len(phrases) == 0 {
		return 0, 0, rehearser.Store.Triggers.DeleteFactTriggers(ctx, []string{fact.FactID})
	}
	phraseEmbeddings, errorValue := rehearser.Store.Embedder.EmbedDocuments(ctx, phrases)
	if errorValue != nil {
		return 0, 0, fmt.Errorf("trigger phrase embedding failed: %w", errorValue)
	}
	ownEmbedding, errorValue := rehearser.Store.Embedder.EmbedQuery(ctx, fact.Content)
	if errorValue != nil {
		return 0, 0, fmt.Errorf("fact embedding failed: %w", errorValue)
	}
	keptPhrases, keptEmbeddings := []string{}, [][]float32{}
	for index, phrase := range phrases {
		if index >= len(phraseEmbeddings) {
			break
		}
		neighbourEmbeddings, errorValue := rehearser.neighbourEmbeddings(ctx, phrase, phraseEmbeddings[index], fact, now)
		if errorValue != nil {
			return 0, 0, errorValue
		}
		selected, selectedEmbeddings := SelectSpecificTriggers([]string{phrase}, [][]float32{phraseEmbeddings[index]}, ownEmbedding, neighbourEmbeddings)
		keptPhrases = append(keptPhrases, selected...)
		keptEmbeddings = append(keptEmbeddings, selectedEmbeddings...)
	}
	if errorValue := rehearser.Store.Triggers.DeleteFactTriggers(ctx, []string{fact.FactID}); errorValue != nil {
		return 0, 0, errorValue
	}
	if len(keptPhrases) == 0 {
		return 0, len(phrases), nil
	}
	triggers := make([]FactTrigger, 0, len(keptPhrases))
	for _, phrase := range keptPhrases {
		triggers = append(triggers, FactTrigger{
			TriggerID:      NewIdentifier(),
			FactID:         fact.FactID,
			Phrase:         phrase,
			EmbeddingModel: rehearser.Store.EmbeddingModel,
		})
	}
	if errorValue := rehearser.Store.Triggers.SaveFactTriggers(ctx, triggers, keptEmbeddings); errorValue != nil {
		return 0, 0, errorValue
	}
	return len(keptPhrases), len(phrases) - len(keptPhrases), nil
}

func (rehearser Rehearser) neighbourEmbeddings(ctx context.Context, phrase string, phraseEmbedding []float32, fact Fact, now time.Time) ([][]float32, error) {
	hits, errorValue := rehearser.Store.Facts.SearchFacts(ctx, FactSearchQuery{
		Reader:         Reader{PersonID: fact.OwnerPersonID},
		Text:           phrase,
		Embedding:      phraseEmbedding,
		EmbeddingModel: rehearser.Store.EmbeddingModel,
		CandidateLimit: rehearser.Store.candidateLimit(),
		ReferenceTime:  now,
	})
	if errorValue != nil {
		return nil, errorValue
	}
	neighbourContents := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit.Fact.FactID == fact.FactID {
			continue
		}
		neighbourContents = append(neighbourContents, hit.Fact.Content)
	}
	if len(neighbourContents) == 0 {
		return nil, nil
	}
	return rehearser.Store.Embedder.EmbedDocuments(ctx, neighbourContents)
}

func (rehearser Rehearser) askModel(ctx context.Context, fact Fact) ([]string, error) {
	response, errorValue := rehearser.Model.GenerateStructured(ctx, StructuredRequest{
		SchemaName:     "memory_trigger",
		SchemaDocument: TriggerSchemaDocument,
		Instruction:    TriggerInstruction,
		Subject:        "Memory: " + fact.Content,
	})
	if errorValue != nil {
		return nil, fmt.Errorf("memory rehearsal model call failed: %w", errorValue)
	}
	var output rehearseOutput
	if errorValue := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); errorValue != nil {
		return nil, TerminalJobError{Cause: fmt.Errorf("memory rehearsal output is not the schema: %w", errorValue)}
	}
	return NormalizeTriggerPhrases(output.Phrases), nil
}
