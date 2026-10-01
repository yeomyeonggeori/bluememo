package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yeomyeonggeori/bluememo"
)

const (
	DefaultDecisionsModel = "~typesafe/jev-latest"
	noTargetAnswer        = "9"
)

var (
	instructionAnswerLinePattern = regexp.MustCompile(`(?m)^(\d) (\S.*)$`)
	instructionColumnPattern     = regexp.MustCompile(`\s{2,}`)
	subjectCandidateLinePattern  = regexp.MustCompile(`(?m)^(\d+)\. (.+)$`)
)

type Criterion struct {
	Key   string
	Gloss string
}

func InstructionGlosses(instruction string) map[string]string {
	glosses := map[string]string{}
	for _, match := range instructionAnswerLinePattern.FindAllStringSubmatch(instruction, -1) {
		columns := instructionColumnPattern.Split(strings.TrimSpace(match[2]), 2)
		glosses[match[1]] = strings.Join(columns, ": ")
	}
	return glosses
}

func subjectGlosses(subject string) map[string]string {
	glosses := map[string]string{noTargetAnswer: "none of the candidates"}
	for _, match := range subjectCandidateLinePattern.FindAllStringSubmatch(subject, -1) {
		glosses[match[1]] = "candidate " + match[1] + ": " + match[2]
	}
	return glosses
}

func CriteriaFor(request bluememo.ChoiceRequest) ([]Criterion, error) {
	glosses := InstructionGlosses(request.Instruction)
	if request.Instruction == bluememo.TargetInstruction {
		glosses = subjectGlosses(request.Subject)
	}
	criteria := make([]Criterion, 0, len(request.Answers))
	for _, answer := range request.Answers {
		gloss, isPresent := glosses[answer]
		if !isPresent {
			return nil, fmt.Errorf("no gloss for answer %q", answer)
		}
		criteria = append(criteria, Criterion{Key: answer, Gloss: gloss})
	}
	return criteria, nil
}

type Decisions struct {
	Client *Client
	Model  string
}

func NewDecisions(client *Client) Decisions {
	return Decisions{Client: client, Model: DefaultDecisionsModel}
}

func (decisions Decisions) Distribute(ctx context.Context, component string, state string, instruction string, criteria map[string]string) (map[string]float64, error) {
	model := decisions.Model
	if model == "" {
		model = DefaultDecisionsModel
	}
	responseBody, errorValue := decisions.Client.post(ctx, component, DecisionsURL, map[string]any{
		"model": model,
		"state": state,
		"questions": map[string]any{"answer": map[string]any{
			"type":         "choice",
			"instructions": instruction,
			"criteria":     criteria,
		}},
	})
	if errorValue != nil {
		return nil, errorValue
	}
	var parsed struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if errorValue := json.Unmarshal(responseBody, &parsed); errorValue != nil {
		return nil, fmt.Errorf("decisions response is not the expected shape: %w", errorValue)
	}
	answer, isPresent := parsed.Answers["answer"]
	if !isPresent || len(answer.Probabilities) == 0 {
		return nil, fmt.Errorf("decisions response carries no probabilities: %s", responseBody)
	}
	return answer.Probabilities, nil
}

func (decisions Decisions) Choose(ctx context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	criteria, errorValue := CriteriaFor(request)
	if errorValue != nil {
		return nil, errorValue
	}
	glosses := make(map[string]string, len(criteria))
	for _, criterion := range criteria {
		glosses[criterion.Key] = criterion.Gloss
	}
	return decisions.Distribute(ctx, "judge", request.Subject, request.Instruction, glosses)
}

// RerankBatch is how many candidates one decision call scores. A call that
// spreads its belief over every candidate at once discriminates no better at
// two hundred than at twenty, so candidates are scored in groups small enough
// for the answer to stand out, and the groups' winners are then scored against
// each other.
const RerankBatch = 24

func (decisions Decisions) Rerank(ctx context.Context, query string, contents []string) ([]float64, error) {
	scores := make([]float64, len(contents))
	if len(contents) < 2 {
		return scores, nil
	}
	if len(contents) <= RerankBatch {
		return decisions.scoreGroup(ctx, query, contents, indexRange(len(contents)), scores)
	}
	finalists := []int{}
	for start := 0; start < len(contents); start += RerankBatch {
		group := indexRange(min(start+RerankBatch, len(contents)))[start:]
		if _, errorValue := decisions.scoreGroup(ctx, query, contents, group, scores); errorValue != nil {
			return nil, errorValue
		}
		finalists = append(finalists, bestOf(group, scores, 3)...)
	}
	if len(finalists) < 2 {
		return scores, nil
	}
	runoff := make([]float64, len(contents))
	if _, errorValue := decisions.scoreGroup(ctx, query, contents, finalists, runoff); errorValue != nil {
		return nil, errorValue
	}
	// A finalist is ordered by the runoff and still ranks above every candidate
	// its own group beat, so the group scores stay as the tie-break below.
	for _, index := range finalists {
		scores[index] = 1 + runoff[index]
	}
	return scores, nil
}

func (decisions Decisions) scoreGroup(ctx context.Context, query string, contents []string, group []int, scores []float64) ([]float64, error) {
	criteria := make(map[string]string, len(group))
	for _, index := range group {
		criteria["m"+strconv.Itoa(index)] = contents[index]
	}
	probabilities, errorValue := decisions.Distribute(ctx, "rerank", query, "Which memory answers this question?", criteria)
	if errorValue != nil {
		return nil, errorValue
	}
	for _, index := range group {
		scores[index] = probabilities["m"+strconv.Itoa(index)]
	}
	return scores, nil
}

func indexRange(count int) []int {
	indexes := make([]int, count)
	for index := range indexes {
		indexes[index] = index
	}
	return indexes
}

func bestOf(group []int, scores []float64, keep int) []int {
	ordered := append([]int{}, group...)
	sort.SliceStable(ordered, func(first, second int) bool {
		return scores[ordered[first]] > scores[ordered[second]]
	})
	return ordered[:min(keep, len(ordered))]
}

func SubjectCandidates(subject string) []Criterion {
	matches := subjectCandidateLinePattern.FindAllStringSubmatch(subject, -1)
	candidates := make([]Criterion, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, Criterion{Key: match[1], Gloss: match[2]})
	}
	return candidates
}

func IsInstructionAnswerLine(line string) bool {
	return instructionAnswerLinePattern.MatchString(line)
}

const NoCandidateAnswer = noTargetAnswer
