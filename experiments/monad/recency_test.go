package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

// 근접 중복이 존재할 때 시간이 결정적인가. 방해물에는 날짜가 없고
// 정답만 7일 전이므로, "최근에"를 묻는 질의에서 시간이 가르는지 본다.
func TestRecencyAgainstNearDuplicates(t *testing.T) {
	client := newOllamaClient("qwen3.5:4b", "이동하", now)
	ctx := context.Background()

	distractors := makeDistractors(300, 42)
	texts := []string{}
	ages := []time.Time{}
	for _, item := range corpus {
		texts = append(texts, item.content)
		ages = append(ages, item.occurredAt)
	}
	for _, sentence := range distractors {
		texts = append(texts, sentence)
		ages = append(ages, daysAgo(180)) // 방해물은 오래된 기록으로 둔다
	}

	vectors := [][]float32{}
	for start := 0; start < len(texts); start += 32 {
		end := start + 32
		if end > len(texts) {
			end = len(texts)
		}
		batch, errorValue := client.Embed(ctx, texts[start:end])
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		vectors = append(vectors, batch...)
	}

	temporalQueries := map[string]bool{
		"최근에 누구랑 배포 얘기했지?": true,
		"채용 얘기 누구랑 했었지?":   false,
	}

	fmt.Printf("\n%-26s %-24s %-24s\n", "질의", "감쇠 없음", "임베딩 프로브 + 최근성")
	fmt.Println(strings.Repeat("─", 78))
	recentReferences := []string{"최근에 있었던 일", "요즘 무슨 일이 있었지", "며칠 전에 뭐 했지"}
	pastReferences := []string{"예전에 있었던 일", "작년에 뭐 했지", "오래전 일"}
	referenceVectors, errorValue := client.Embed(ctx, append(append([]string{}, recentReferences...), pastReferences...))
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	recentVectors := referenceVectors[:len(recentReferences)]
	pastVectors := referenceVectors[len(recentReferences):]
	maximumSimilarity := func(query []float32, references [][]float32) float64 {
		best := -1.0
		for _, reference := range references {
			if score := cosineSimilarity(query, reference); score > best {
				best = score
			}
		}
		return best
	}

	for _, probe := range probes {
		queryVector, errorValue2 := client.Embed(ctx, []string{probe.query})
		if errorValue2 != nil {
			t.Fatal(errorValue2)
		}
		recentScore := maximumSimilarity(queryVector[0], recentVectors)
		pastScore := maximumSimilarity(queryVector[0], pastVectors)
		isTemporal := recentScore > pastScore
		_ = temporalQueries

		type scored struct {
			content string
			plain   float64
			boosted float64
		}
		ranked := make([]scored, len(texts))
		for index := range texts {
			similarity := cosineSimilarity(queryVector[0], vectors[index])
			boost := 1.0
			if isTemporal && !ages[index].IsZero() {
				boost = 1.0 + 0.30*math.Exp(-(now.Sub(ages[index]).Hours()/24.0)/30.0)
			}
			ranked[index] = scored{texts[index], similarity, similarity * boost}
		}
		plain := append([]scored{}, ranked...)
		sort.Slice(plain, func(a int, b int) bool { return plain[a].plain > plain[b].plain })
		boosted := append([]scored{}, ranked...)
		sort.Slice(boosted, func(a int, b int) bool { return boosted[a].boosted > boosted[b].boosted })

		mark := func(content string) string {
			if content == probe.want {
				return "✓"
			}
			return "✗"
		}
		fmt.Printf("%-26s %-2s %-21s %-2s %-21s\n", truncate(probe.query, 24),
			mark(plain[0].content), truncate(plain[0].content, 20),
			mark(boosted[0].content), truncate(boosted[0].content, 20))
	}
}
