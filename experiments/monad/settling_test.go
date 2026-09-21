package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// alwaysUnrelated stands in for the store as it is today: every proposition
// becomes a row, nothing is compared against what is already held.
type alwaysUnrelated struct{}

func (alwaysUnrelated) Judge(context.Context, string, []string) (Judgement, error) {
	return Judgement{Relation: RelationUnrelated, TargetIndex: -1, Importance: 3}, nil
}

func TestSettlingAgainstRepetition(t *testing.T) {
	groups := []string{
		"내 이름은 이동하고, 여명거리 CTO야.",
		"나 사과 좋아해.",
		"아까 말했듯이 나는 사과를 좋아해.",
		"사과 좋아한다고 전에 얘기했잖아.",
		"릴리스할 때 admind랑 capabilityd 같이 올려야 해.",
		"릴리스는 admind와 capabilityd를 함께 배포해야 한다니까.",
		"하나만 올리면 프로토콜 버전이 어긋나.",
		"ㅋㅋㅋ ㅇㅇ 알겠음",
		"아 근데 요즘은 사과보다 배가 더 좋더라.",
	}
	probes := []struct{ query, want string }{
		{"내가 뭐 좋아한다고 했지?", "배"},
		{"릴리스할 때 뭘 같이 올려야 해?", "capabilityd"},
		{"내 직업이 뭐랬지?", "CTO"},
	}

	client := newOllamaClient("qwen3.5:4b", "이동하", time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()

	type arm struct {
		name  string
		judge Judge
	}
	for _, one := range []arm{{"판정 없음 (지금)", alwaysUnrelated{}}, {"typed 판정", typedJudge{client: client, minMargin: 0.10, sameMinMargin: 0.25, trace: true}}} {
		store, errorValue := openStore(filepath.Join(t.TempDir(), "m.db"), client, client, registry, time.Now)
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		for index, body := range groups {
			if errorValue := store.Enqueue(ctx, fmt.Sprintf("g%02d", index), body); errorValue != nil {
				t.Fatal(errorValue)
			}
		}
		report, errorValue := store.Settle(ctx, one.judge)
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		live := 0
		store.database.QueryRow(`select count(*) from memory where superseded_by is null and forgotten_at is null`).Scan(&live)

		fmt.Printf("\n── %s ──\n", one.name)
		fmt.Printf("제안된 명제 %d · 살아있는 기억 %d\n", report.Proposed, live)
		fmt.Printf("신규 %d · 강화 %d · 갱신 %d · 확장 %d · 버림 %d\n",
			report.Inserted, report.Reinforced, report.Superseded, report.Extended, report.Dropped)
		memoryRows, _ := store.database.Query(`select content from memory where superseded_by is null and forgotten_at is null order by created_at`)
		fmt.Println("  살아있는 기억:")
		for memoryRows.Next() {
			line := ""
			memoryRows.Scan(&line)
			fmt.Printf("    · %s\n", line)
		}
		memoryRows.Close()
		for _, probe := range probes {
			ranked, errorValue := store.Recall(ctx, probe.query, 3)
			if errorValue != nil {
				t.Fatal(errorValue)
			}
			mark, top := "✗", "(없음)"
			if len(ranked) > 0 {
				top = ranked[0].Memory.Content
				if strings.Contains(strings.ToLower(top), strings.ToLower(probe.want)) {
					mark = "✓"
				}
			}
			fmt.Printf("  %s %-22s %s\n", mark, probe.query, truncate(top, 46))
		}
		store.database.Close()
	}
}
