package bluememo

import (
	"sort"
	"time"
)

const reciprocalRankOffset = 60.0

type ScoredFact struct {
	Fact        Fact    `json:"fact"`
	Score       float64 `json:"score"`
	VectorRank  int     `json:"vectorRank,omitempty"`
	LexicalRank int     `json:"lexicalRank,omitempty"`
}

func RankFacts(hits []RankedFact, referenceTime time.Time) []ScoredFact {
	scoredFacts := make([]ScoredFact, 0, len(hits))
	for _, hit := range hits {
		scoredFacts = append(scoredFacts, ScoredFact{
			Fact:        hit.Fact,
			Score:       reciprocalRankScore(hit.VectorRank) + reciprocalRankScore(hit.LexicalRank),
			VectorRank:  hit.VectorRank,
			LexicalRank: hit.LexicalRank,
		})
	}
	sort.SliceStable(scoredFacts, func(left int, right int) bool {
		if scoredFacts[left].Score != scoredFacts[right].Score {
			return scoredFacts[left].Score > scoredFacts[right].Score
		}
		if scoredFacts[left].Fact.ReinforcementCount != scoredFacts[right].Fact.ReinforcementCount {
			return scoredFacts[left].Fact.ReinforcementCount > scoredFacts[right].Fact.ReinforcementCount
		}
		return scoredFacts[left].Fact.ValidFrom.After(scoredFacts[right].Fact.ValidFrom)
	})
	return scoredFacts
}

func reciprocalRankScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1 / (reciprocalRankOffset + float64(rank))
}

func ExpandWithSiblings(scoredFacts []ScoredFact, siblings []Fact, limit int) []ScoredFact {
	if len(scoredFacts) == 0 || len(siblings) == 0 {
		return limitScoredFacts(scoredFacts, limit)
	}
	leadingEpisodeID := scoredFacts[0].Fact.EpisodeID
	if leadingEpisodeID == "" {
		return limitScoredFacts(scoredFacts, limit)
	}
	alreadyPresent := make(map[string]bool, len(scoredFacts))
	for _, scoredFact := range scoredFacts {
		alreadyPresent[scoredFact.Fact.FactID] = true
	}
	expanded := []ScoredFact{scoredFacts[0]}
	for _, sibling := range siblings {
		if sibling.EpisodeID != leadingEpisodeID || alreadyPresent[sibling.FactID] {
			continue
		}
		alreadyPresent[sibling.FactID] = true
		expanded = append(expanded, ScoredFact{Fact: sibling, Score: scoredFacts[0].Score})
	}
	return limitScoredFacts(append(expanded, scoredFacts[1:]...), limit)
}

func MergeRankedFacts(primary []RankedFact, secondary []RankedFact) []RankedFact {
	position := make(map[string]int, len(primary))
	merged := append([]RankedFact{}, primary...)
	for index, hit := range merged {
		position[hit.Fact.FactID] = index
	}
	for _, hit := range secondary {
		index, alreadyRanked := position[hit.Fact.FactID]
		if !alreadyRanked {
			position[hit.Fact.FactID] = len(merged)
			merged = append(merged, hit)
			continue
		}
		if merged[index].VectorRank == 0 || (hit.VectorRank > 0 && hit.VectorRank < merged[index].VectorRank) {
			merged[index].VectorRank = hit.VectorRank
		}
	}
	return merged
}
