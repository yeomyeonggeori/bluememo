package main

import (
	"context"
	"encoding/json"
	"fmt"
)

const triggerSchema = `
create table if not exists trigger_phrase (
  trigger_id text primary key,
  memory_id  text not null references memory(memory_id),
  phrase     text not null,
  route      text not null check (route in ('entity','bridge'))
);

create table if not exists trigger_vector (
  trigger_id text primary key references trigger_phrase(trigger_id),
  vector     blob not null
);

create index if not exists trigger_by_memory on trigger_phrase(memory_id);
`

// TriggerMaker writes, at settle time, the short phrases a memory should answer
// to later. A memory is otherwise only reachable through its own wording, which
// leaves it unreachable from a question that shares no surface with it.
type TriggerMaker interface {
	Triggers(ctx context.Context, proposition string) ([]Trigger, error)
}

type Trigger struct {
	Phrase string `json:"phrase"`
	Route  string `json:"route"`
}

const triggerInstruction = `너는 기억을 나중에 불러낼 짧은 단서를 쓰는 사람이다.
명제 하나를 받아, 그 명제를 안정적으로 떠올리게 할 단서 구절 네 개를 쓴다.
각 구절은 2~6 단어의 명사구다.

두 갈래만 허용한다.

entity  명제의 의미를 한두 칸 위로 일반화한 이름.
bridge  그 장면이나 소품이 언급되면 이 명제가 관련될 확률이 높은 구체적 상황.

실격:
- 재진술. 명제를 짧게 줄여 쓴 것은 단서가 아니다.
- 지나친 일반화. 아무 명제에나 붙는 말은 이 명제를 못 집어낸다.
- 약한 연상. 그 장면이 무관한 명제 여럿에도 붙으면 안 된다.

네 구절은 서로 다른 각도여야 한다. 같은 개념의 변형을 나열하지 마라.
지어내야 채울 수 있으면 적게 써라. 환각은 빈칸보다 회상을 더 해친다.

예: "정우성은 매운 음식을 먹으면 딸꾹질을 한다."
  entity: 음식 자극 반응
  entity: 체질적 불편
  bridge: 점심 메뉴 고를 때
  bridge: 회식 장소 정할 때`

var triggerOutputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"triggers": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"phrase": map[string]any{"type": "string"},
					"route":  map[string]any{"type": "string", "enum": []string{"entity", "bridge"}},
				},
				"required":             []string{"phrase", "route"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"triggers"},
	"additionalProperties": false,
}

func (client *ollamaClient) Triggers(ctx context.Context, proposition string) ([]Trigger, error) {
	requestBody := map[string]any{
		"model":   client.generationModel,
		"stream":  false,
		"think":   false,
		"options": map[string]any{"temperature": 0},
		"messages": []map[string]string{
			{"role": "system", "content": triggerInstruction},
			{"role": "user", "content": proposition},
		},
		"format": triggerOutputSchema,
	}
	var response struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if errorValue := client.postJSON(ctx, "/api/chat", requestBody, &response); errorValue != nil {
		return nil, errorValue
	}
	var decoded struct {
		Triggers []Trigger `json:"triggers"`
	}
	if errorValue := json.Unmarshal([]byte(response.Message.Content), &decoded); errorValue != nil {
		return nil, fmt.Errorf("trigger maker returned unparseable structured output: %w", errorValue)
	}
	return decoded.Triggers, nil
}

// rehearse stores the phrases a memory should answer to. They are indexed and
// searched, and never returned as evidence: how a memory is reached stays
// separate from what the memory says.
func (store *Store) rehearse(ctx context.Context, memoryID string, proposition string) error {
	if store.triggerMaker == nil {
		return nil
	}
	triggers, errorValue := store.triggerMaker.Triggers(ctx, proposition)
	if errorValue != nil {
		return errorValue
	}
	if len(triggers) == 0 {
		return nil
	}
	phrases := make([]string, len(triggers))
	for index, trigger := range triggers {
		phrases[index] = trigger.Phrase
	}
	vectors, errorValue := store.embedder.Embed(ctx, phrases)
	if errorValue != nil {
		return errorValue
	}
	for index, trigger := range triggers {
		triggerID := newIdentifier()
		if _, errorValue := store.database.ExecContext(ctx,
			`insert into trigger_phrase(trigger_id, memory_id, phrase, route) values(?,?,?,?)`,
			triggerID, memoryID, trigger.Phrase, trigger.Route); errorValue != nil {
			return errorValue
		}
		if _, errorValue := store.database.ExecContext(ctx,
			`insert into trigger_vector(trigger_id, vector) values(?,?)`,
			triggerID, encodeVector(vectors[index])); errorValue != nil {
			return errorValue
		}
	}
	return nil
}

type triggerHit struct {
	memoryID string
	rank     int
}

// rankByTrigger scores the phrases and reports the memories behind them. A
// memory reached this way is ranked, never quoted.
func (store *Store) rankByTrigger(ctx context.Context, queryVector []float32, limit int) ([]triggerHit, error) {
	rows, errorValue := store.database.QueryContext(ctx, `
		select t.memory_id, v.vector
		from trigger_phrase t
		join trigger_vector v using (trigger_id)
		join memory m using (memory_id)
		where m.superseded_by is null and m.forgotten_at is null`)
	if errorValue != nil {
		return nil, errorValue
	}
	defer rows.Close()

	type scored struct {
		memoryID string
		score    float64
	}
	all := []scored{}
	for rows.Next() {
		one := scored{}
		blob := []byte{}
		if errorValue := rows.Scan(&one.memoryID, &blob); errorValue != nil {
			return nil, errorValue
		}
		one.score = cosineSimilarity(queryVector, decodeVector(blob))
		all = append(all, one)
	}
	for outer := 0; outer < len(all); outer++ {
		for inner := outer + 1; inner < len(all); inner++ {
			if all[inner].score > all[outer].score {
				all[outer], all[inner] = all[inner], all[outer]
			}
		}
	}

	hits := []triggerHit{}
	seen := map[string]bool{}
	for _, one := range all {
		if seen[one.memoryID] {
			continue
		}
		seen[one.memoryID] = true
		hits = append(hits, triggerHit{memoryID: one.memoryID, rank: len(hits)})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
