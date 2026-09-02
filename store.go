package bluememo

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

const DefaultEmbeddingModelName = "baai/bge-m3"

const (
	SearchModeHybrid  = "hybrid"
	SearchModeLexical = "lexical"

	DefaultSearchCandidateLimit = 40
	DefaultSearchResultLimit    = 12
	DefaultProfileBudget        = 1200
	DefaultRecalledBudget       = 2400
	DefaultAdminFactListLimit   = 200
)

type Store struct {
	Facts          FactRepository
	Profiles       ProfileRepository
	Jobs           JobRepository
	Embedder       Embedder
	EmbeddingModel string
	CandidateLimit int
	Logger         *slog.Logger
	Now            func() time.Time
}

type SearchResult struct {
	Facts          []ScoredFact `json:"facts"`
	Mode           string       `json:"mode"`
	DegradedReason string       `json:"degradedReason,omitempty"`
}

type RecallRequest struct {
	Reader         Reader
	PersonID       string
	Query          string
	Limit          int
	ProfileBudget  int
	RecalledBudget int
}

type Recall struct {
	Profile        Profile      `json:"profile"`
	Facts          []ScoredFact `json:"facts"`
	Mode           string       `json:"mode"`
	DegradedReason string       `json:"degradedReason,omitempty"`
}

func (recall Recall) ProfileLines() []string {
	return append(append([]string{}, recall.Profile.IdentityLines...), recall.Profile.CurrentLines...)
}

func (store Store) Search(ctx context.Context, reader Reader, text string, limit int) (SearchResult, error) {
	if store.Facts == nil {
		return SearchResult{}, errors.New("memory fact repository is not configured")
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return SearchResult{}, errors.New("memory search text is required")
	}
	referenceTime := store.now()
	query := FactSearchQuery{Reader: reader, Text: trimmedText, CandidateLimit: store.candidateLimit(), ReferenceTime: referenceTime}
	mode, degradedReason := store.resolveSearchMode(ctx, &query)
	hits, errorValue := store.Facts.SearchFacts(ctx, query)
	if errorValue != nil {
		return SearchResult{}, errorValue
	}
	scoredFacts := limitScoredFacts(RankFacts(hits, referenceTime), limit)
	store.markRecalled(ctx, scoredFacts, referenceTime)
	return SearchResult{Facts: scoredFacts, Mode: mode, DegradedReason: degradedReason}, nil
}

func (store Store) resolveSearchMode(ctx context.Context, query *FactSearchQuery) (string, string) {
	if store.Embedder == nil {
		return SearchModeLexical, ""
	}
	hasVectorSearch, errorValue := store.Facts.HasVectorSearch(ctx)
	if errorValue != nil {
		return SearchModeLexical, "vector search availability check failed: " + errorValue.Error()
	}
	if !hasVectorSearch {
		return SearchModeLexical, "the database has no vector extension"
	}
	embedding, errorValue := store.Embedder.EmbedQuery(ctx, query.Text)
	if errorValue != nil {
		return SearchModeLexical, "query embedding failed: " + errorValue.Error()
	}
	if errorValue := ValidateEmbedding(embedding); errorValue != nil {
		return SearchModeLexical, "query embedding rejected: " + errorValue.Error()
	}
	query.Embedding = embedding
	return SearchModeHybrid, ""
}

func (store Store) markRecalled(ctx context.Context, scoredFacts []ScoredFact, recalledAt time.Time) {
	if len(scoredFacts) == 0 {
		return
	}
	factIDs := make([]string, 0, len(scoredFacts))
	for _, scoredFact := range scoredFacts {
		factIDs = append(factIDs, scoredFact.Fact.FactID)
	}
	if errorValue := store.Facts.MarkFactsRecalled(ctx, factIDs, recalledAt); errorValue != nil {
		store.logger().Warn("memory.search.mark_recalled_failed", "error", errorValue.Error(), "factCount", len(factIDs))
	}
}

func (store Store) Recall(ctx context.Context, request RecallRequest) (Recall, error) {
	recall := Recall{Mode: SearchModeLexical}
	if store.Profiles != nil && strings.TrimSpace(request.PersonID) != "" {
		profile, isFound, errorValue := store.Profiles.FindProfile(ctx, request.PersonID)
		if errorValue != nil {
			return Recall{}, errorValue
		}
		if isFound {
			recall.Profile = profile
		}
	}
	if strings.TrimSpace(request.Query) != "" {
		searchResult, errorValue := store.Search(ctx, request.Reader, request.Query, request.Limit)
		if errorValue != nil {
			return Recall{}, errorValue
		}
		recall.Facts = searchResult.Facts
		recall.Mode = searchResult.Mode
		recall.DegradedReason = searchResult.DegradedReason
	}
	return BudgetRecall(recall, request.ProfileBudget, request.RecalledBudget), nil
}

func (store Store) ListReadable(ctx context.Context, reader Reader, limit int) (Profile, []Fact, error) {
	if store.Facts == nil {
		return Profile{}, nil, errors.New("memory fact repository is not configured")
	}
	now := store.now()
	profile := Profile{PersonID: reader.PersonID, IdentityLines: []string{}, CurrentLines: []string{}}
	if store.Profiles != nil && strings.TrimSpace(reader.PersonID) != "" {
		storedProfile, isFound, errorValue := store.Profiles.FindProfile(ctx, reader.PersonID)
		if errorValue != nil {
			return Profile{}, nil, errorValue
		}
		if isFound {
			profile = storedProfile
		}
	}
	facts, errorValue := store.Facts.ListReadableFacts(ctx, reader, limit, now)
	if errorValue != nil {
		return Profile{}, nil, errorValue
	}
	return profile, facts, nil
}

func (store Store) Forget(ctx context.Context, reader Reader, factIDs []string, reason string) ([]string, error) {
	if store.Facts == nil {
		return nil, errors.New("memory fact repository is not configured")
	}
	forgottenFactIDs, errorValue := store.Facts.ForgetFacts(ctx, reader, factIDs, reason, store.now())
	if errorValue != nil {
		return nil, errorValue
	}
	if len(forgottenFactIDs) > 0 {
		store.EnqueueProfileRebuild(ctx, reader.PersonID)
	}
	return forgottenFactIDs, nil
}

func (store Store) EnqueueExtraction(ctx context.Context, subjectID string) (Job, bool, error) {
	if store.Jobs == nil {
		return Job{}, false, errors.New("memory job repository is not configured")
	}
	return store.Jobs.EnqueueJob(ctx, JobKindExtract, subjectID, store.now())
}

func (store Store) EnqueueProfileRebuild(ctx context.Context, personID string) {
	if store.Jobs == nil || strings.TrimSpace(personID) == "" {
		return
	}
	if _, _, errorValue := store.Jobs.EnqueueJob(ctx, JobKindProfile, personID, store.now()); errorValue != nil {
		store.logger().Warn("memory.profile.enqueue_failed", "personID", personID, "error", errorValue.Error())
	}
}

func BudgetRecall(recall Recall, profileBudget int, recalledBudget int) Recall {
	if profileBudget <= 0 {
		profileBudget = DefaultProfileBudget
	}
	if recalledBudget <= 0 {
		recalledBudget = DefaultRecalledBudget
	}
	recall.Profile.IdentityLines, profileBudget = takeLinesWithinBudget(recall.Profile.IdentityLines, profileBudget)
	recall.Profile.CurrentLines, _ = takeLinesWithinBudget(recall.Profile.CurrentLines, profileBudget)
	budgetedFacts := make([]ScoredFact, 0, len(recall.Facts))
	for _, scoredFact := range recall.Facts {
		cost := len([]rune(scoredFact.Fact.Content))
		if cost > recalledBudget {
			break
		}
		recalledBudget -= cost
		budgetedFacts = append(budgetedFacts, scoredFact)
	}
	recall.Facts = budgetedFacts
	return recall
}

func takeLinesWithinBudget(lines []string, budget int) ([]string, int) {
	taken := make([]string, 0, len(lines))
	for _, line := range lines {
		cost := len([]rune(line))
		if cost > budget {
			break
		}
		budget -= cost
		taken = append(taken, line)
	}
	return taken, budget
}

func limitScoredFacts(scoredFacts []ScoredFact, limit int) []ScoredFact {
	if limit <= 0 {
		limit = DefaultSearchResultLimit
	}
	if len(scoredFacts) <= limit {
		return scoredFacts
	}
	return scoredFacts[:limit]
}

func (store Store) candidateLimit() int {
	if store.CandidateLimit > 0 {
		return store.CandidateLimit
	}
	return DefaultSearchCandidateLimit
}

func (store Store) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func (store Store) logger() *slog.Logger {
	if store.Logger != nil {
		return store.Logger
	}
	return slog.Default()
}
