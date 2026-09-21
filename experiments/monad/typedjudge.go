package main

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// typedJudge reads the probability the model puts on each answer token in one
// forward pass, the way SemIf reads a decision out of native logits, instead of
// letting it generate JSON. Nothing about the answer is inferred from embedding
// distance: the model decides, and the runtime only holds the one irreversible
// branch to a confidence margin.
type typedJudge struct {
	client *ollamaClient
	// minMargin is how far ahead of the runner-up the chosen answer must be
	// before the runtime acts on it. Below it the judgement falls back to
	// unrelated, which inserts and keeps both, because every other branch
	// either merges or drops and neither can be undone by inserting later.
	minMargin float64
	// sameMinMargin holds the one branch that destroys a fact to a higher bar.
	sameMinMargin float64
	trace         bool
}

const relationInstruction = `너는 기억 저장소의 통합 판정자다. 새 명제 하나와, 저장소가 이미 갖고 있는 후보 명제 목록을 받는다.
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

const importanceInstruction = `너는 기억 저장소의 중요도 평가자다. 명제 하나를 받아 기억으로 얼마나 값어치 있는지 매긴다.

1 거의 없음   잡담, 내용 없는 맞장구, 동어반복.
2 낮음        지나가는 말, 곧 무의미해질 세부.
3 보통        보통의 사실.
4 높음        그 사람에 대해 오래 쓸 사실, 절차, 선호.
5 매우 높음   신원, 역할, 반복해서 쓰일 규칙.

같은 것을 말하는 두 명제 중에서는 더 완전하고 더 자립적인 쪽이 높다.
"이동하는 사과를 좋아한다" 는 3, "이동하는 사과를 좋아한다는 것을 전제했다" 는 2 다.

숫자 하나만 출력한다.`

const targetInstruction = `너는 기억 저장소의 후보 선택자다. 새 명제 하나와 후보 명제 목록을 받는다.
새 명제와 관계를 맺는 후보의 번호 하나만 출력한다. 관계 있는 후보가 없으면 9 를 출력한다.

숫자 하나만 출력한다.`

var relationByToken = map[string]Relation{
	"1": RelationSame,
	"2": RelationUpdates,
	"3": RelationExtends,
	"4": RelationUnrelated,
	"5": RelationNoise,
}

func (judge typedJudge) Judge(ctx context.Context, proposition string, candidates []string) (Judgement, error) {
	listing := ""
	for index, candidate := range candidates {
		listing += fmt.Sprintf("%d. %s\n", index, candidate)
	}
	question := fmt.Sprintf("새 명제:\n%s\n\n이미 갖고 있는 후보:\n%s", proposition, listing)

	distribution, errorValue := judge.client.firstTokenDistribution(ctx, relationInstruction, question)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	chosenToken, margin := leadingToken(distribution, []string{"1", "2", "3", "4", "5"})
	relation, known := relationByToken[chosenToken]
	if !known {
		return Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: 3}, nil
	}
	gated := ""
	switch {
	case relation == RelationSame && margin < judge.sameMinMargin:
		relation, gated = RelationUnrelated, " (same 문턱 미달)"
	case relation != RelationUnrelated && margin < judge.minMargin:
		relation, gated = RelationUnrelated, " (확신 부족)"
	}
	if judge.trace {
		fmt.Printf("    판정 %-9s 마진 %.3f%s  ← %s\n", relation, margin, gated, truncate(proposition, 34))
	}

	importance, errorValue := judge.rate(ctx, proposition)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	if relation == RelationUnrelated || relation == RelationNoise {
		return Judgement{Relation: relation, TargetIndex: -1, Importance: importance}, nil
	}

	distribution, errorValue = judge.client.firstTokenDistribution(ctx, targetInstruction, question)
	if errorValue != nil {
		return Judgement{}, errorValue
	}
	allowed := make([]string, 0, len(candidates)+1)
	for index := range candidates {
		allowed = append(allowed, fmt.Sprint(index))
	}
	allowed = append(allowed, "9")
	targetToken, _ := leadingToken(distribution, allowed)
	if targetToken == "9" || targetToken == "" {
		return Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: importance}, nil
	}
	targetIndex := 0
	fmt.Sscan(targetToken, &targetIndex)
	return Judgement{Relation: relation, TargetIndex: targetIndex, Importance: importance}, nil
}

func (judge typedJudge) rate(ctx context.Context, proposition string) (int, error) {
	distribution, errorValue := judge.client.firstTokenDistribution(ctx, importanceInstruction, proposition)
	if errorValue != nil {
		return 0, errorValue
	}
	token, _ := leadingToken(distribution, []string{"1", "2", "3", "4", "5"})
	if token == "" {
		return 3, nil
	}
	rating := 3
	fmt.Sscan(token, &rating)
	return rating, nil
}

// leadingToken returns the allowed answer the model put most weight on, and how
// far ahead of the runner-up it is, as a probability difference.
func leadingToken(distribution map[string]float64, allowed []string) (string, float64) {
	best, second, chosen := -1.0, -1.0, ""
	for _, token := range allowed {
		probability := distribution[token]
		switch {
		case probability > best:
			best, second, chosen = probability, best, token
		case probability > second:
			second = probability
		}
	}
	if best < 0 {
		return "", 0
	}
	if second < 0 {
		second = 0
	}
	return chosen, best - second
}

func (client *ollamaClient) firstTokenDistribution(ctx context.Context, instruction string, question string) (map[string]float64, error) {
	requestBody := map[string]any{
		"model":  client.generationModel,
		"stream": false,
		"think":  false,
		"messages": []map[string]string{
			{"role": "system", "content": instruction},
			{"role": "user", "content": question},
		},
		"options":      map[string]any{"temperature": 0, "num_predict": 1},
		"logprobs":     true,
		"top_logprobs": 20,
	}
	var response struct {
		Logprobs []struct {
			TopLogprobs []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"logprobs"`
	}
	if errorValue := client.postJSON(ctx, "/api/chat", requestBody, &response); errorValue != nil {
		return nil, errorValue
	}
	if len(response.Logprobs) == 0 {
		return nil, fmt.Errorf("ollama returned no logprobs; the runtime must expose them for a typed decision")
	}
	distribution := map[string]float64{}
	for _, entry := range response.Logprobs[0].TopLogprobs {
		distribution[strings.TrimSpace(entry.Token)] += math.Exp(entry.Logprob)
	}
	return distribution, nil
}
