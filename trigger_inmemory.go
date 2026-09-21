package bluememo

import (
	"context"
	"sort"
	"sync"
)

type InMemoryTriggerRepository struct {
	mutex      sync.Mutex
	triggers   map[string]FactTrigger
	embeddings map[string][]float32
	order      []string
	Facts      *InMemoryRepository
}

func NewInMemoryTriggerRepository(facts *InMemoryRepository) *InMemoryTriggerRepository {
	return &InMemoryTriggerRepository{triggers: map[string]FactTrigger{}, embeddings: map[string][]float32{}, Facts: facts}
}

func (repository *InMemoryTriggerRepository) SaveFactTriggers(_ context.Context, triggers []FactTrigger, embeddings [][]float32) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for index, trigger := range triggers {
		if _, exists := repository.triggers[trigger.TriggerID]; !exists {
			repository.order = append(repository.order, trigger.TriggerID)
		}
		repository.triggers[trigger.TriggerID] = trigger
		if index < len(embeddings) {
			repository.embeddings[trigger.TriggerID] = embeddings[index]
		}
	}
	return nil
}

func (repository *InMemoryTriggerRepository) DeleteFactTriggers(_ context.Context, factIDs []string) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	removing := map[string]bool{}
	for _, factID := range factIDs {
		removing[factID] = true
	}
	kept := []string{}
	for _, triggerID := range repository.order {
		if removing[repository.triggers[triggerID].FactID] {
			delete(repository.triggers, triggerID)
			delete(repository.embeddings, triggerID)
			continue
		}
		kept = append(kept, triggerID)
	}
	repository.order = kept
	return nil
}

func (repository *InMemoryTriggerRepository) SearchTriggers(_ context.Context, query FactSearchQuery) ([]RankedFact, error) {
	if len(query.Embedding) == 0 || repository.Facts == nil {
		return nil, nil
	}
	repository.mutex.Lock()
	bestByFact := map[string]float64{}
	for _, triggerID := range repository.order {
		trigger := repository.triggers[triggerID]
		if trigger.EmbeddingModel != query.EmbeddingModel {
			continue
		}
		similarity := cosineSimilarity(query.Embedding, repository.embeddings[triggerID])
		if best, seen := bestByFact[trigger.FactID]; !seen || similarity > best {
			bestByFact[trigger.FactID] = similarity
		}
	}
	repository.mutex.Unlock()

	factIDs := make([]string, 0, len(bestByFact))
	for factID := range bestByFact {
		factIDs = append(factIDs, factID)
	}
	sort.SliceStable(factIDs, func(left int, right int) bool { return bestByFact[factIDs[left]] > bestByFact[factIDs[right]] })

	facts, errorValue := repository.Facts.ListFactsByID(context.Background(), query.Reader, factIDs, query.ReferenceTime)
	if errorValue != nil {
		return nil, errorValue
	}
	byID := map[string]Fact{}
	for _, fact := range facts {
		byID[fact.FactID] = fact
	}
	ranked := []RankedFact{}
	for index, factID := range factIDs {
		fact, readable := byID[factID]
		if !readable || (query.CandidateLimit > 0 && index >= query.CandidateLimit) {
			continue
		}
		ranked = append(ranked, RankedFact{Fact: fact, VectorRank: len(ranked) + 1})
	}
	return ranked, nil
}

func (repository *InMemoryTriggerRepository) TriggersForFact(factID string) []FactTrigger {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	triggers := []FactTrigger{}
	for _, triggerID := range repository.order {
		if repository.triggers[triggerID].FactID == factID {
			triggers = append(triggers, repository.triggers[triggerID])
		}
	}
	return triggers
}

func (repository *InMemoryTriggerRepository) ListTriggerPhrases(ctx context.Context, factIDs []string) (map[string][]string, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	wanted := map[string]bool{}
	for _, factID := range factIDs {
		wanted[factID] = true
	}
	phrases := map[string][]string{}
	for _, trigger := range repository.triggers {
		if !wanted[trigger.FactID] {
			continue
		}
		phrases[trigger.FactID] = append(phrases[trigger.FactID], trigger.Phrase)
	}
	for factID := range phrases {
		sort.Strings(phrases[factID])
	}
	return phrases, nil
}
