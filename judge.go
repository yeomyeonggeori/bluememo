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

const RelationInstruction = `너는 기억 저장소의 통합 판정자다. 새 명제 하나와, 저장소가 이미 갖고 있는 후보 명제 목록을 받는다.
새 명제가 후보 중 하나에 대해 어떤 관계인지 판정한다.

1 same      후보와 같은 것을 서술한다. 표현만 다르고 새로 알게 된 것이 없다.
2 updates   후보가 서술하던 것이 더는 참이 아니게 만든다.
3 extends   후보를 무효화하지 않으면서 세부를 더한다. 둘 다 참으로 남는다.
4 unrelated 후보들과 관계가 없다.
5 noise     기억할 가치가 없는 잡담이거나 내용이 없다.

same 은 같은 대상에 대한 같은 종류의 말이라는 뜻이 아니다. 같은 것을 서술해야 same 이다.
잘못 묶으면 사실이 영영 사라지고, 잘못 나누면 중복이 남을 뿐이다.

잘못된 판정의 예 (절대 이렇게 하지 마라):
  새 명제 "이동하는 여명거리의 CTO이다." 와 후보 "이동하의 이름은 이동하이다."
  틀린 판정 1 · 옳은 판정 4. 둘 다 신원 서술이지만 서술하는 것이 다르다.
  새 명제 "이동하는 사과보다 배를 더 좋아한다." 와 후보 "이동하는 사과를 좋아한다."
  틀린 판정 2 · 옳은 판정 3. 후보가 거짓이 되지 않았다.

숫자 하나만 출력한다.`

const ImportanceInstruction = `너는 기억 저장소의 중요도 평가자다. 명제 하나를 받아 기억으로 얼마나 값어치 있는지 매긴다.

1 거의 없음   잡담, 내용 없는 맞장구, 동어반복.
2 낮음        지나가는 말, 곧 무의미해질 세부.
3 보통        보통의 사실.
4 높음        그 사람에 대해 오래 쓸 사실, 절차, 선호.
5 매우 높음   신원, 역할, 반복해서 쓰일 규칙.

같은 것을 말하는 두 명제 중에서는 더 완전하고 더 자립적인 쪽이 높다.
"이동하는 사과를 좋아한다" 는 3, "이동하는 사과를 좋아한다는 것을 전제했다" 는 2 다.

숫자 하나만 출력한다.`

const TargetInstruction = `너는 기억 저장소의 후보 선택자다. 새 명제 하나와 후보 명제 목록을 받는다.
새 명제와 관계를 맺는 후보의 번호 하나만 출력한다. 관계 있는 후보가 없으면 9 를 출력한다.

숫자 하나만 출력한다.`

var relationByAnswer = map[string]Relation{
	"1": RelationSame,
	"2": RelationUpdates,
	"3": RelationExtends,
	"4": RelationUnrelated,
	"5": RelationNoise,
}

var ratingAnswers = []string{"1", "2", "3", "4", "5"}

func (judge DistributionJudge) Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error) {
	subject := judgementSubject(proposition, candidates)
	relation, errorValue := judge.relation(ctx, subject)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	importance, errorValue := judge.importance(ctx, proposition)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	if relation == RelationUnrelated || relation == RelationNoise {
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
	distribution, errorValue := judge.Chooser.Choose(ctx, ChoiceRequest{Instruction: RelationInstruction, Subject: subject, Answers: ratingAnswers})
	if errorValue != nil {
		return "", fmt.Errorf("relation judgement failed: %w", errorValue)
	}
	answer, margin := leadingAnswer(distribution, ratingAnswers)
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

func (judge DistributionJudge) importance(ctx context.Context, proposition string) (int, error) {
	distribution, errorValue := judge.Chooser.Choose(ctx, ChoiceRequest{Instruction: ImportanceInstruction, Subject: proposition, Answers: ratingAnswers})
	if errorValue != nil {
		return 0, fmt.Errorf("importance rating failed: %w", errorValue)
	}
	answer, _ := leadingAnswer(distribution, ratingAnswers)
	rating, errorValue := strconv.Atoi(answer)
	if errorValue != nil {
		return DefaultImportance, nil
	}
	return rating, nil
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
		listing.WriteString("(없음)\n")
	}
	return fmt.Sprintf("새 명제:\n%s\n\n이미 갖고 있는 후보:\n%s", proposition, listing.String())
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
