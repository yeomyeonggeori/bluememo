package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// 한 입력에서 분해된 명제들은 형제다. supermemory의 extends 엣지에 해당하고,
// 추출이 필요 없다 — 같은 memorize() 호출에서 나왔다는 사실만으로 생긴다.
var coverageProbes = []struct {
	query  string
	origin string // 이 질의가 온전히 답해지려면 필요한 형제 묶음
}{
	{"릴리스 절차 알려줘", "c"},
	{"배포할 때 주의할 거 다 알려줘", "c"},
	{"나에 대해 아는 거 말해봐", "a"},
}

func TestSiblingExpansion(t *testing.T) {
	client := newOllamaClient("qwen3.5:4b", "이동하", now)
	ctx := context.Background()

	contents := make([]string, len(corpus))
	for index, item := range corpus {
		contents[index] = item.content
	}
	documentVectors, errorValue := client.Embed(ctx, contents)
	if errorValue != nil {
		t.Fatal(errorValue)
	}

	const limit = 3
	fmt.Printf("\n%-30s %-22s %-22s\n", "질의", "형제 링크 없음", "형제 링크 있음")
	fmt.Println(strings.Repeat("─", 76))

	plainTotal, siblingTotal, wantTotal := 0, 0, 0
	for _, probe := range coverageProbes {
		queryVector, errorValue := client.Embed(ctx, []string{probe.query})
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		type scored struct {
			index int
			score float64
		}
		ranked := make([]scored, len(corpus))
		for index := range corpus {
			ranked[index] = scored{index, cosineSimilarity(queryVector[0], documentVectors[index])}
		}
		sort.Slice(ranked, func(a int, b int) bool { return ranked[a].score > ranked[b].score })

		wanted := map[int]bool{}
		for index, item := range corpus {
			if item.originID == probe.origin {
				wanted[index] = true
			}
		}
		wantTotal += len(wanted)

		plain := map[int]bool{}
		for _, candidate := range ranked[:limit] {
			plain[candidate.index] = true
		}
		// 형제 확장: 1위의 형제를 전부 끌어온 뒤 같은 상한으로 자른다
		expanded := map[int]bool{}
		topOrigin := corpus[ranked[0].index].originID
		for index, item := range corpus {
			if item.originID == topOrigin {
				expanded[index] = true
			}
		}
		for _, candidate := range ranked {
			if len(expanded) >= limit {
				break
			}
			expanded[candidate.index] = true
		}

		plainHits, siblingHits := 0, 0
		for index := range wanted {
			if plain[index] {
				plainHits++
			}
			if expanded[index] {
				siblingHits++
			}
		}
		plainTotal += plainHits
		siblingTotal += siblingHits
		fmt.Printf("%-30s %-22s %-22s\n", truncate(probe.query, 28),
			fmt.Sprintf("%d/%d 회수", plainHits, len(wanted)),
			fmt.Sprintf("%d/%d 회수", siblingHits, len(wanted)))
	}
	fmt.Println(strings.Repeat("─", 76))
	fmt.Printf("%-30s %-22s %-22s\n", "합계",
		fmt.Sprintf("%d/%d", plainTotal, wantTotal),
		fmt.Sprintf("%d/%d", siblingTotal, wantTotal))
}
