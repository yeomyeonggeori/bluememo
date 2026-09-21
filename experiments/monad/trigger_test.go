package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTriggersReachWhatSurfaceCannot asks questions whose wording shares nothing
// with the stored proposition. Similarity search can only reach a memory whose
// surface the question happens to echo; a written-down rehearsal of when the
// memory will be wanted is the other way in.
func TestTriggersReachWhatSurfaceCannot(t *testing.T) {
	client := newOllamaClient("qwen3.5:4b", "이동하", time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()

	propositions := []string{
		"릴리스는 admind 와 capabilityd 를 함께 올린다.",
		"하나만 올리면 프로토콜 버전이 어긋난다.",
		"박예시는 아침에만 커피를 마신다.",
		"최견본은 채용 면접을 담당한다.",
		"이동하는 여명거리의 CTO 이다.",
		"워크스페이스 권한은 POSIX 가 결정한다.",
		"계약은 2026-01-01 부터 유효하다.",
		"김여명은 회의를 길게 끄는 것을 싫어한다.",
	}

	// None of these repeats the wording of the proposition it wants.
	probes := []struct {
		question string
		wants    string
	}{
		{"배포하고 나서 기능이 안 먹으면 뭘 의심해야 해?", "프로토콜"},
		{"면접 일정 누구한테 물어봐야 하지?", "최견본"},
		{"아침 회의에 뭐 사다 놓을까?", "커피"},
		{"파일 못 읽는다고 하면 어디를 봐야 해?", "POSIX"},
		{"회의 짧게 끝내야 하는 사람 있었나?", "김여명"},
	}

	run := func(name string, withTriggers bool) int {
		store, errorValue := openStore(
			filepath.Join(t.TempDir(), "trigger.db"),
			client, client, registry, time.Now)
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		defer store.database.Close()
		if withTriggers {
			store.triggerMaker = client
		}

		for _, proposition := range propositions {
			monad := Monad{Content: proposition, Kind: KindFact}
			if _, errorValue := store.insertMonadWithImportance(ctx, monad, newIdentifier(), 4); errorValue != nil {
				t.Fatal(errorValue)
			}
		}
		if withTriggers {
			rows, errorValue := store.database.QueryContext(ctx, `select memory_id, content from memory`)
			if errorValue != nil {
				t.Fatal(errorValue)
			}
			type row struct{ id, content string }
			pending := []row{}
			for rows.Next() {
				one := row{}
				if errorValue := rows.Scan(&one.id, &one.content); errorValue != nil {
					t.Fatal(errorValue)
				}
				pending = append(pending, one)
			}
			rows.Close()
			for _, one := range pending {
				if errorValue := store.rehearse(ctx, one.id, one.content); errorValue != nil {
					t.Fatal(errorValue)
				}
			}
			var phraseCount int
			store.database.QueryRowContext(ctx, `select count(*) from trigger_phrase`).Scan(&phraseCount)
			t.Logf("  단서 %d개 생성", phraseCount)
		}

		hits := 0
		for _, probe := range probes {
			ranked, errorValue := store.search(ctx, []Monad{{Content: probe.question}}, 3)
			if errorValue != nil {
				t.Fatal(errorValue)
			}
			mark := "✗"
			top := "(없음)"
			if len(ranked) > 0 {
				top = ranked[0].Memory.Content
				if strings.Contains(top, probe.wants) {
					hits++
					mark = "✓"
				}
			}
			t.Logf("  %s %s\n      → %s", mark, probe.question, top)
		}
		t.Logf("%s: %d/%d", name, hits, len(probes))
		return hits
	}

	t.Log("=== 단서 없음 (표면 유사도만) ===")
	without := run("단서 없음", false)
	t.Log("=== 단서 있음 (Entity/Bridge) ===")
	with := run("단서 있음", true)

	t.Log(strings.Repeat("=", 50))
	t.Log(fmt.Sprintf("단서 없음 %d/%d · 단서 있음 %d/%d", without, len(probes), with, len(probes)))
}
