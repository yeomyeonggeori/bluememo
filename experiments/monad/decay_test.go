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

// rejectedKind is the five-value enum this test measured and found wanting.
// It lives here and nowhere else: the shipped Monad carries a single boolean,
// and a reader who found this type in the domain would take it for current.
type rejectedKind string

const (
	KindIdentity   rejectedKind = "identity"
	KindPreference rejectedKind = "preference"
	KindFact       rejectedKind = "fact"
	KindEpisode    rejectedKind = "episode"
	KindProcedure  rejectedKind = "procedure"
)

type sample struct {
	content    string
	kind       rejectedKind
	occurredAt time.Time
	isStatic   bool
	originID   string // 같은 memorize() 호출에서 나온 형제
}

var now = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

func daysAgo(days int) time.Time { return now.AddDate(0, 0, -days) }

var corpus = []sample{
	{"이동하는 사과를 좋아한다.", KindPreference, time.Time{}, true, "a"},
	{"이동하의 직업은 여명거리 CTO이다.", KindIdentity, time.Time{}, true, "a"},
	{"이동하는 회의록을 마크다운으로 받고 싶어 한다.", KindPreference, time.Time{}, true, "b"},
	{"릴리스는 admind와 capabilityd를 함께 올린다.", KindProcedure, time.Time{}, false, "c"},
	{"admind와 capabilityd 중 하나만 올리면 프로토콜이 어긋난다.", KindFact, time.Time{}, false, "c"},
	{"빌드 전에 현재 브랜치가 origin/main을 포함하는지 확인한다.", KindProcedure, time.Time{}, false, "c"},
	{"배포 후에는 실제로 도는 바이너리의 리비전을 확인한다.", KindProcedure, time.Time{}, false, "c"},
	{"이동하는 최견본과 신규 채용을 논의했다.", KindEpisode, daysAgo(400), false, "d"},
	{"이동하는 박예시와 배포 일정을 논의했다.", KindEpisode, daysAgo(7), false, "e"},
	{"이동하는 김여명과 릴리스 절차를 점검했다.", KindEpisode, daysAgo(300), false, "f"},
	{"여명거리와 고객사의 계약은 2026-01-01부터 유효하다.", KindFact, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true, "g"},
	{"박예시는 아침에만 커피를 마신다.", KindFact, time.Time{}, false, "h"},
}

type scheme struct {
	label string
	score func(sample, float64) float64 // (기억, 코사인) → 점수
}

func decayBy(halfLifeDays float64, when time.Time) float64 {
	if when.IsZero() {
		return 1.0
	}
	return math.Exp(-(now.Sub(when).Hours() / 24.0) / halfLifeDays)
}

var schemes = []scheme{
	// 감쇠를 랭킹에서 아예 빼고, 순수 유사도만
	{"안5 감쇠 없음", func(s sample, similarity float64) float64 { return similarity }},
	{"안3 isStatic만", func(s sample, similarity float64) float64 {
		if s.isStatic {
			return similarity
		}
		return similarity * decayBy(90, s.occurredAt)
	}},
	{"안4 isStatic+kind별", func(s sample, similarity float64) float64 {
		if s.isStatic {
			// 정체성·선호는 항상 떠올라야 하므로 가산점
			if s.kind == KindIdentity || s.kind == KindPreference {
				return similarity * 1.15
			}
			return similarity
		}
		switch s.kind {
		case KindEpisode:
			return similarity * decayBy(90, s.occurredAt)
		case KindProcedure:
			return similarity * decayBy(365, s.occurredAt)
		default:
			return similarity
		}
	}},
}

var probes = []struct {
	query string
	want  string
}{
	{"계약이 언제부터 유효하지?", "여명거리와 고객사의 계약은 2026-01-01부터 유효하다."},
	{"최근에 누구랑 배포 얘기했지?", "이동하는 박예시와 배포 일정을 논의했다."},
	{"내가 뭐 좋아한다고 했지?", "이동하는 사과를 좋아한다."},
	{"내 직업이 뭐라고 했지?", "이동하의 직업은 여명거리 CTO이다."},
	{"회의록 어떤 형식으로 달라고 했지?", "이동하는 회의록을 마크다운으로 받고 싶어 한다."},
	{"릴리스할 때 뭘 같이 올려야 해?", "릴리스는 admind와 capabilityd를 함께 올린다."},
	{"빌드 전에 확인할 게 뭐였지?", "빌드 전에 현재 브랜치가 origin/main을 포함하는지 확인한다."},
	{"하나만 올리면 어떻게 되지?", "admind와 capabilityd 중 하나만 올리면 프로토콜이 어긋난다."},
	{"채용 얘기 누구랑 했었지?", "이동하는 최견본과 신규 채용을 논의했다."},
	{"커피 언제 마시는 사람 있었지?", "박예시는 아침에만 커피를 마신다."},
}

func TestKindDifferentiatedDecay(t *testing.T) {
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

	fmt.Printf("\n%-26s", "질의")
	for _, s := range schemes {
		fmt.Printf("%-24s", s.label)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 76))

	hits := make([]int, len(schemes))
	for _, probe := range probes {
		queryVector, errorValue := client.Embed(ctx, []string{probe.query})
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		fmt.Printf("%-26s", truncate(probe.query, 24))
		for schemeIndex, s := range schemes {
			type scored struct {
				content string
				score   float64
			}
			ranked := make([]scored, len(corpus))
			for index, item := range corpus {
				ranked[index] = scored{item.content, s.score(item, cosineSimilarity(queryVector[0], documentVectors[index]))}
			}
			sort.Slice(ranked, func(a int, b int) bool { return ranked[a].score > ranked[b].score })
			mark := "✗"
			if ranked[0].content == probe.want {
				mark = "✓"
				hits[schemeIndex]++
			}
			fmt.Printf("%-2s %-21s", mark, truncate(ranked[0].content, 20))
		}
		fmt.Println()
	}
	fmt.Println(strings.Repeat("─", 76))
	fmt.Printf("%-26s", "1위 적중")
	for schemeIndex := range schemes {
		fmt.Printf("%-24s", fmt.Sprintf("%d/%d", hits[schemeIndex], len(probes)))
	}
	fmt.Println()
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text + strings.Repeat(" ", limit-len(runes))
	}
	return string(runes[:limit-1]) + "…"
}
