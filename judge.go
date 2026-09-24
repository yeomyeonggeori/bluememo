package bluememo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type Relation string

const (
	RelationSame      Relation = "same"
	RelationUpdates   Relation = "updates"
	RelationExtends   Relation = "extends"
	RelationUnrelated Relation = "unrelated"
	RelationNoise     Relation = "noise"
)

const (
	DefaultJudgeMargin     = 0.10
	DefaultSameJudgeMargin = 0.25
	DefaultImportance      = 3
	noTargetAnswer         = "9"
)

type Judgement struct {
	Relation    Relation
	TargetIndex int
	Importance  int
}

type Judge interface {
	Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error)
}

type ChoiceRequest struct {
	Instruction string
	Subject     string
	Answers     []string
}

type Chooser interface {
	Choose(ctx context.Context, request ChoiceRequest) (map[string]float64, error)
}

type DistributionJudge struct {
	Chooser    Chooser
	Margin     float64
	SameMargin float64
}

const RelationInstruction = `You decide how a new statement relates to statements a memory store already holds. You get one new statement and a numbered list of held candidates.

1 same      it says what a candidate says. Only the wording differs and nothing new is learned.
2 updates   it makes a candidate no longer true.
3 extends   it adds detail to a candidate without making it false. Both stay true.
4 unrelated it has nothing to do with the candidates.

same means the two statements say the same thing, not merely that they are about the same subject.
Merging by mistake loses a fact for good; keeping apart by mistake only leaves a duplicate.

Wrong answers (never do this):
  New "Alex leads the payments team." against candidate "Alex's name is Alex."
  Wrong 1, right 4. Both describe who Alex is, but they say different things.
  New "Alex likes pears more than apples." against candidate "Alex likes apples."
  Wrong 2, right 3. The candidate is still true.

Answer with one digit.`

const ImportanceInstruction = `You rate how much a statement is worth keeping in a memory store.

1 almost nothing  small talk, an empty acknowledgement, a tautology.
2 low             a passing remark, a detail that will soon stop mattering.
3 ordinary        an ordinary fact.
4 high            a lasting fact, procedure or preference about the person.
5 very high       identity, role, or a rule that will be used again and again.

Of two statements that say the same thing, the more complete and self-contained one rates higher.
"Alex likes apples" is 3; "Alex assumed that liking apples was given" is 2.

Answer with one digit.`

const TargetInstruction = `You pick which held candidate a new statement relates to. You get one new statement and a numbered list of candidates.
Answer with the number of that candidate. When none relates, answer 9.

Answer with one digit.`

var relationByAnswer = map[string]Relation{
	"1": RelationSame,
	"2": RelationUpdates,
	"3": RelationExtends,
	"4": RelationUnrelated,
}

var (
	relationAnswers = []string{"1", "2", "3", "4"}
	ratingAnswers   = []string{"1", "2", "3", "4", "5"}
)

const noiseRating = 1

func (judge DistributionJudge) Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error) {
	importance, isNoise, errorValue := judge.importance(ctx, proposition)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	if isNoise {
		return Judgement{Relation: RelationNoise, TargetIndex: -1, Importance: importance}, nil
	}
	if len(candidates) == 0 {
		return Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: importance}, nil
	}
	subject := judgementSubject(proposition, candidates)
	relation, errorValue := judge.relation(ctx, subject)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	if relation == RelationUnrelated {
		return Judgement{Relation: relation, TargetIndex: -1, Importance: importance}, nil
	}
	targetIndex, errorValue := judge.target(ctx, subject, len(candidates))
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	if targetIndex < 0 {
		return Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: importance}, nil
	}
	return Judgement{Relation: relation, TargetIndex: targetIndex, Importance: importance}, nil
}

func (judge DistributionJudge) relation(ctx context.Context, subject string) (Relation, error) {
	distribution, errorValue := judge.Chooser.Choose(ctx, ChoiceRequest{Instruction: RelationInstruction, Subject: subject, Answers: relationAnswers})
	if errorValue != nil {
		return "", fmt.Errorf("relation judgement failed: %w", errorValue)
	}
	answer, margin := leadingAnswer(distribution, relationAnswers)
	relation, isKnown := relationByAnswer[answer]
	if !isKnown {
		return RelationUnrelated, nil
	}
	if relation == RelationSame && margin < judge.sameMargin() {
		return RelationUnrelated, nil
	}
	if relation != RelationUnrelated && margin < judge.margin() {
		return RelationUnrelated, nil
	}
	return relation, nil
}

func (judge DistributionJudge) importance(ctx context.Context, proposition string) (int, bool, error) {
	distribution, errorValue := judge.Chooser.Choose(ctx, ChoiceRequest{Instruction: ImportanceInstruction, Subject: proposition, Answers: ratingAnswers})
	if errorValue != nil {
		return 0, false, fmt.Errorf("importance rating failed: %w", errorValue)
	}
	answer, margin := leadingAnswer(distribution, ratingAnswers)
	rating, errorValue := strconv.Atoi(answer)
	if errorValue != nil {
		return DefaultImportance, false, nil
	}
	return rating, rating == noiseRating && margin >= judge.margin(), nil
}

func (judge DistributionJudge) target(ctx context.Context, subject string, candidateCount int) (int, error) {
	answers := make([]string, 0, candidateCount+1)
	for index := range candidateCount {
		answers = append(answers, strconv.Itoa(index))
	}
	answers = append(answers, noTargetAnswer)
	distribution, errorValue := judge.Chooser.Choose(ctx, ChoiceRequest{Instruction: TargetInstruction, Subject: subject, Answers: answers})
	if errorValue != nil {
		return 0, fmt.Errorf("target choice failed: %w", errorValue)
	}
	answer, _ := leadingAnswer(distribution, answers)
	if answer == "" || answer == noTargetAnswer {
		return -1, nil
	}
	return strconv.Atoi(answer)
}

func (judge DistributionJudge) margin() float64 {
	if judge.Margin > 0 {
		return judge.Margin
	}
	return DefaultJudgeMargin
}

func (judge DistributionJudge) sameMargin() float64 {
	if judge.SameMargin > 0 {
		return judge.SameMargin
	}
	return DefaultSameJudgeMargin
}

func judgementSubject(proposition string, candidates []string) string {
	var listing strings.Builder
	for index, candidate := range candidates {
		fmt.Fprintf(&listing, "%d. %s\n", index, candidate)
	}
	if len(candidates) == 0 {
		listing.WriteString("(none)\n")
	}
	return fmt.Sprintf("New statement:\n%s\n\nHeld candidates:\n%s", proposition, listing.String())
}

func leadingAnswer(distribution map[string]float64, answers []string) (string, float64) {
	best, runnerUp, chosen := 0.0, 0.0, ""
	for _, answer := range answers {
		probability := distribution[answer]
		switch {
		case probability > best:
			best, runnerUp, chosen = probability, best, answer
		case probability > runnerUp:
			runnerUp = probability
		}
	}
	return chosen, best - runnerUp
}
