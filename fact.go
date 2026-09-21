package bluememo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	FactKindIdentity   = "identity"
	FactKindPreference = "preference"
	FactKindFact       = "fact"
	FactKindEpisode    = "episode"
	FactKindTemporary  = "temporary"
)

const (
	EpisodeSourceKindTaskRun  = "task_run"
	EpisodeSourceKindExplicit = "explicit"
	EpisodeSourceKindImport   = "import"
)

const FactContentCharacterLimit = 240

const EmbeddingDimensionCount = 1024

var FactKinds = []string{FactKindIdentity, FactKindPreference, FactKindFact, FactKindEpisode, FactKindTemporary}

var EpisodeSourceKinds = []string{EpisodeSourceKindTaskRun, EpisodeSourceKindExplicit, EpisodeSourceKindImport}

type Episode struct {
	EpisodeID         string    `json:"episodeID"`
	SourceKind        string    `json:"sourceKind"`
	SourceID          string    `json:"sourceID"`
	RequesterPersonID string    `json:"requesterPersonID"`
	ConversationID    string    `json:"conversationID,omitempty"`
	Content           string    `json:"content"`
	OccurredAt        time.Time `json:"occurredAt"`
	CreatedAt         time.Time `json:"createdAt"`
}

type Fact struct {
	FactID             string    `json:"factID"`
	EpisodeID          string    `json:"episodeID"`
	OwnerPersonID      string    `json:"ownerPersonID"`
	CircleIDs          []string  `json:"circleIDs,omitempty"`
	SubjectPersonID    string    `json:"subjectPersonID,omitempty"`
	Kind               string    `json:"kind"`
	Content            string    `json:"content"`
	EmbeddingModel     string    `json:"embeddingModel,omitempty"`
	SecurityLevelRank  int       `json:"securityLevelRank"`
	RequiredClasses    []string  `json:"requiredClasses"`
	ValidFrom          time.Time `json:"validFrom"`
	ValidUntil         time.Time `json:"validUntil,omitzero"`
	SupersededBy       string    `json:"supersededBy,omitempty"`
	ReinforcementCount int       `json:"reinforcementCount"`
	LastRecalledAt     time.Time `json:"lastRecalledAt,omitzero"`
	ForgottenAt        time.Time `json:"forgottenAt,omitzero"`
	ForgetReason       string    `json:"forgetReason,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
}

type FactWrite struct {
	Fact             Fact
	Embedding        []float32
	SupersedesFactID string
	ReinforcesFactID string
}

type EpisodeWrite struct {
	Episode        Episode
	Facts          []FactWrite
	CandidateCount int
}

type EpisodeReceipt struct {
	EpisodeID         string   `json:"episodeID"`
	FactIDs           []string `json:"factIDs"`
	SupersededFactIDs []string `json:"supersededFactIDs"`
	ReinforcedFactIDs []string `json:"reinforcedFactIDs"`
	CandidateCount    int      `json:"candidateCount"`
}

func ReceiptForWrite(write EpisodeWrite) EpisodeReceipt {
	receipt := EpisodeReceipt{EpisodeID: write.Episode.EpisodeID, CandidateCount: write.CandidateCount, FactIDs: []string{}, SupersededFactIDs: []string{}, ReinforcedFactIDs: []string{}}
	for _, factWrite := range write.Facts {
		if factWrite.ReinforcesFactID != "" {
			receipt.ReinforcedFactIDs = append(receipt.ReinforcedFactIDs, factWrite.ReinforcesFactID)
			continue
		}
		receipt.FactIDs = append(receipt.FactIDs, factWrite.Fact.FactID)
		if factWrite.SupersedesFactID != "" {
			receipt.SupersededFactIDs = append(receipt.SupersededFactIDs, factWrite.SupersedesFactID)
		}
	}
	return receipt
}

func ValidateEpisodeReplay(stored Episode, requested Episode) error {
	if stored.RequesterPersonID != requested.RequesterPersonID || stored.Content != requested.Content || stored.ConversationID != requested.ConversationID {
		return errors.New("memory episode source is already recorded with a different requester or payload")
	}
	return nil
}

type Profile struct {
	SourceFactIDs      []string  `json:"sourceFactIDs"`
	PersonID           string    `json:"personID"`
	IdentityLines      []string  `json:"identityLines"`
	CurrentLines       []string  `json:"currentLines"`
	BuiltFromFactCount int       `json:"builtFromFactCount"`
	BuiltAt            time.Time `json:"builtAt"`
}

type SecurityLabel struct {
	SecurityLevelRank int
	RequiredClasses   []string
}

func (fact Fact) IsShared() bool {
	return len(fact.CircleIDs) > 0
}

func (fact Fact) IsStatic() bool {
	return fact.Kind == FactKindIdentity || fact.Kind == FactKindPreference
}

func (fact Fact) IsLive(referenceTime time.Time) bool {
	if fact.SupersededBy != "" || !fact.ForgottenAt.IsZero() {
		return false
	}
	return fact.ValidUntil.IsZero() || fact.ValidUntil.After(referenceTime)
}

func ValidateEpisode(episode Episode) error {
	if strings.TrimSpace(episode.EpisodeID) == "" {
		return errors.New("episode id is required")
	}
	if !containsString(EpisodeSourceKinds, episode.SourceKind) {
		return fmt.Errorf("episode source kind %q is not one of %s", episode.SourceKind, strings.Join(EpisodeSourceKinds, ", "))
	}
	if strings.TrimSpace(episode.SourceID) == "" {
		return errors.New("episode source id is required")
	}
	if strings.TrimSpace(episode.RequesterPersonID) == "" {
		return errors.New("episode requester person id is required")
	}
	if strings.TrimSpace(episode.Content) == "" {
		return errors.New("episode content is required")
	}
	if episode.OccurredAt.IsZero() {
		return errors.New("episode occurred_at is required")
	}
	return nil
}

func ValidateFact(fact Fact) error {
	if strings.TrimSpace(fact.FactID) == "" {
		return errors.New("fact id is required")
	}
	if strings.TrimSpace(fact.EpisodeID) == "" {
		return errors.New("fact episode id is required")
	}
	if strings.TrimSpace(fact.OwnerPersonID) == "" {
		return errors.New("a fact requires an owner")
	}
	if len(fact.CircleIDs) != len(NormalizeCircleIDs(fact.CircleIDs)) {
		return errors.New("fact circles must be lowercase, trimmed, and unique")
	}
	if !containsString(FactKinds, fact.Kind) {
		return fmt.Errorf("fact kind %q is not one of %s", fact.Kind, strings.Join(FactKinds, ", "))
	}
	if errorValue := validateFactContent(fact.Content); errorValue != nil {
		return errorValue
	}
	if fact.ValidFrom.IsZero() {
		return errors.New("fact valid_from is required")
	}
	if fact.Kind == FactKindTemporary && fact.ValidUntil.IsZero() {
		return errors.New("a temporary fact requires valid_until")
	}
	if !fact.ValidUntil.IsZero() && !fact.ValidUntil.After(fact.ValidFrom) {
		return errors.New("fact valid_until must come after valid_from")
	}
	return nil
}

func validateFactContent(content string) error {
	trimmedContent := strings.TrimSpace(content)
	if trimmedContent == "" {
		return errors.New("fact content is required")
	}
	if utf8.RuneCountInString(trimmedContent) > FactContentCharacterLimit {
		return fmt.Errorf("fact content exceeds %d characters", FactContentCharacterLimit)
	}
	return nil
}

func ValidateEmbedding(embedding []float32) error {
	if len(embedding) != EmbeddingDimensionCount {
		return fmt.Errorf("embedding has %d dimensions, memory stores %d", len(embedding), EmbeddingDimensionCount)
	}
	return nil
}

func NormalizeCircleIDs(circleIDs []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(circleIDs))
	for _, circleID := range circleIDs {
		trimmed := strings.ToLower(strings.TrimSpace(circleID))
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
