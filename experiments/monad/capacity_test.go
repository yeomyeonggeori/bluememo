package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// 방해물은 우리 코퍼스와 같은 세계에서 나온 그럴듯한 명제여야 한다.
// 무관한 문장을 넣으면 시험이 쉬워져서 상한을 과대평가한다.
var (
	people  = []string{"박예시", "최견본", "김여명", "이샘플", "정본보기", "한표본"}
	verbs   = []string{"회의했다", "일정을 조율했다", "리뷰를 남겼다", "보고서를 보냈다", "질문을 남겼다", "승인을 요청했다"}
	topics  = []string{"배포", "채용", "예산", "근태", "보안 점검", "고객 응대", "인프라 이전", "문서 정리"}
	systems = []string{"admind", "capabilityd", "chatd", "relay", "blueclaw", "supabase"}
	actions = []string{"재시작한다", "로그를 확인한다", "설정을 다시 읽는다", "헬스체크를 통과해야 한다", "버전을 맞춘다"}
)

func makeDistractors(count int, seed int64) []string {
	random := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	out := []string{}
	for len(out) < count {
		var sentence string
		switch random.Intn(3) {
		case 0:
			sentence = fmt.Sprintf("이동하는 %s와 %s에 대해 %s.", people[random.Intn(len(people))], topics[random.Intn(len(topics))], verbs[random.Intn(len(verbs))])
		case 1:
			sentence = fmt.Sprintf("%s를 배포한 뒤에는 %s.", systems[random.Intn(len(systems))], actions[random.Intn(len(actions))])
		default:
			sentence = fmt.Sprintf("%s는 %s 업무를 맡고 있다.", people[random.Intn(len(people))], topics[random.Intn(len(topics))])
		}
		if seen[sentence] {
			continue
		}
		seen[sentence] = true
		out = append(out, sentence)
	}
	return out
}

func TestAccuracyAgainstCorpusSize(t *testing.T) {
	client := newOllamaClient("qwen3.5:4b", "이동하", now)
	ctx := context.Background()

	baseContents := make([]string, len(corpus))
	for index, item := range corpus {
		baseContents[index] = item.content
	}
	distractors := makeDistractors(300, 42)

	allTexts := append(append([]string{}, baseContents...), distractors...)
	vectors := make([][]float32, 0, len(allTexts))
	for start := 0; start < len(allTexts); start += 32 {
		end := start + 32
		if end > len(allTexts) {
			end = len(allTexts)
		}
		batch, errorValue := client.Embed(ctx, allTexts[start:end])
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		vectors = append(vectors, batch...)
		fmt.Printf("\r  임베딩 %d/%d", len(vectors), len(allTexts))
	}

	queryVectors := map[string][]float32{}
	for _, probe := range probes {
		embedded, errorValue := client.Embed(ctx, []string{probe.query})
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		queryVectors[probe.query] = embedded[0]
	}

	fmt.Printf("\n%-14s %-12s %s\n", "코퍼스 크기", "1위 적중", "놓친 질의")
	fmt.Println(strings.Repeat("─", 76))
	for _, extra := range []int{0, 25, 50, 100, 200, 300} {
		size := len(baseContents) + extra
		hits, missed := 0, []string{}
		for _, probe := range probes {
			type scored struct {
				content string
				score   float64
			}
			ranked := make([]scored, size)
			for index := 0; index < size; index++ {
				ranked[index] = scored{allTexts[index], cosineSimilarity(queryVectors[probe.query], vectors[index])}
			}
			sort.Slice(ranked, func(a int, b int) bool { return ranked[a].score > ranked[b].score })
			if ranked[0].content == probe.want {
				hits++
			} else {
				missed = append(missed, truncate(probe.query, 16))
			}
		}
		fmt.Printf("%-14d %-12s %s\n", size, fmt.Sprintf("%d/%d", hits, len(probes)), strings.Join(missed, " · "))
	}
}
