package bluememo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DecompositionInstruction = `너는 기억 저장소의 분해기다. 받은 텍스트를 자립적인 명제들로 쪼갠다.

자립적이라는 것:
- 대명사와 생략을 모두 푼다. "나", "내", "그", "거기"는 실제 이름으로 바꾼다.
- 명제 하나만 따로 읽어도 완전한 문장이어야 한다.
- 조건, 시점, 출처, 정도를 버리지 않고 명제 안에 남긴다.
- 전해 들은 말은 출처를 명제 안에 포함한다. 출처를 별도 명제로 분리하지 않는다.
- 명제는 평서형 종결로 쓴다. "좋아해" 대신 "좋아한다".

isStatic: 그 사람에 대해 바뀌지 않는 특성이면 true. 이름, 직업, 소속, 오래 가는 선호가 그렇다.
사건, 한 번의 요청, 상황 설명은 false. 애매하면 false 다.

occurredOn: 명제가 특정 날에 일어난 일이면 그 날짜(YYYY-MM-DD). 아래 맥락의 오늘을 기준으로 센다. 아니면 빈 문자열.

expiry: 원문이 명제가 참인 기간을 끝맺으면 그 끝을 고른다.
  none            끝이 없다
  end_of_today    오늘까지
  end_of_week     이번 주까지
  end_of_month    이번 달까지
  end_of_quarter  이번 분기까지
  end_of_year     올해까지
  on_date         원문이 날짜를 말한다. 그 마지막 날을 expiryDate(YYYY-MM-DD)에 쓴다
on_date 가 아니면 expiryDate 는 빈 문자열이다. 날짜를 직접 계산하지 말고 기간을 골라라.

금지:
- 원문에 없는 사실을 추가하지 마라. 원문이 말하지 않은 것을 추론해 채우지 마라.
- 아래 맥락 자체를 명제로 만들지 마라. 맥락은 대명사와 날짜를 푸는 데만 쓴다.
- 이름의 철자를 바꾸거나 줄이지 마라. 조사를 이름에 붙이지 마라.
- 이름만 되풀이하는 명제("이동하의 이름은 이동하이다")는 아무것도 말하지 않는다.
- 잡담, 맞장구, 지금 한 번 하고 끝나는 요청은 명제가 아니다. 기억할 것이 없으면 빈 목록이 정답이다.

예시 맥락: 화자 이동하, 오늘 2026-09-21
예시 입력: 어제 최견본이랑 회의했어. 나는 회의록을 항상 마크다운으로 받고 싶어. 이번 분기만 한국어로 답해줘.
예시 출력의 명제 셋:
  isStatic false, occurredOn 2026-09-20: "이동하는 최견본과 회의했다."
  isStatic true:  "이동하는 회의록을 항상 마크다운으로 받고 싶어 한다."
  isStatic false, expiry end_of_quarter: "이동하는 한국어로 답을 받고 싶어 한다."

잘못된 출력의 예 (절대 이렇게 하지 마라):
  입력: "내 이름은 이동하고, 여명거리 CTO야."
  틀린 명제: "이동하는 이름은 이동한다."   ← "이동하고"를 "이동"+"하고"로 잘못 나눴다
  옳은 명제: "이동하는 여명거리의 CTO이다."
화자의 이름은 맥락에 주어진 문자열 그대로다. 그 문자열을 더 짧게 자르지 마라.`

var DecompositionSchemaDocument = mustMarshalSchema(map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"propositions"},
	"properties": map[string]any{
		"propositions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"content", "isStatic", "occurredOn", "expiry", "expiryDate"},
				"properties": map[string]any{
					"content":    map[string]any{"type": "string", "maxLength": ContentCharacterLimit},
					"isStatic":   map[string]any{"type": "boolean"},
					"occurredOn": map[string]any{"type": "string"},
					"expiry":     map[string]any{"type": "string", "enum": Expiries},
					"expiryDate": map[string]any{"type": "string"},
				},
			},
		},
	},
})

type decomposition struct {
	Propositions []Proposition `json:"propositions"`
}

func (store *Store) decompose(ctx context.Context, group pendingGroup) ([]Proposition, error) {
	response, errorValue := store.configuration.Model.GenerateStructured(ctx, StructuredRequest{
		SchemaName:     "memory_decomposition",
		SchemaDocument: DecompositionSchemaDocument,
		Instruction:    DecompositionInstruction,
		Subject:        decompositionSubject(group, store.configuration.Location),
	})
	if errorValue != nil {
		return nil, fmt.Errorf("decomposition model call failed: %w", errorValue)
	}
	var output decomposition
	if errorValue := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); errorValue != nil {
		return nil, fmt.Errorf("decomposition output is not the schema: %w", errorValue)
	}
	return output.Propositions, nil
}

func decompositionSubject(group pendingGroup, location *time.Location) string {
	arrivedAt := group.arrivedAt.In(location)
	var subject strings.Builder
	fmt.Fprintf(&subject, "맥락\n화자: %s\n오늘: %s (%s)\n\n텍스트\n", group.speakerName, arrivedAt.Format(time.DateOnly), arrivedAt.Weekday())
	for _, note := range group.notes {
		if note.speakerName != "" && note.speakerName != group.speakerName {
			fmt.Fprintf(&subject, "%s: ", note.speakerName)
		}
		subject.WriteString(note.body)
		subject.WriteString("\n")
	}
	return subject.String()
}

func mustMarshalSchema(schema map[string]any) string {
	document, errorValue := json.Marshal(schema)
	if errorValue != nil {
		panic("schema does not marshal: " + errorValue.Error())
	}
	return string(document)
}
