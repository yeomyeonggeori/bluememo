package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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

func (decisions Decisions) Rerank(ctx context.Context, query string, contents []string) ([]float64, error) {
	scores := make([]float64, len(contents))
	if len(contents) < 2 {
		return scores, nil
	}
	criteria := make(map[string]string, len(contents))
	for index, content := range contents {
		criteria["m"+strconv.Itoa(index)] = content
	}
	probabilities, errorValue := decisions.Distribute(ctx, "rerank", query, "Which memory answers this question?", criteria)
	if errorValue != nil {
		return nil, errorValue
	}
	winner, bestProbability := 0, -1.0
	for index := range contents {
		if probability := probabilities["m"+strconv.Itoa(index)]; probability > bestProbability {
			winner, bestProbability = index, probability
		}
	}
	scores[winner] = 1
	return scores, nil
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
