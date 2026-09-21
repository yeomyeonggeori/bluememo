package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// reachCorpus is deliberately spread across unrelated domains so that a probe
// which lands on the right proposition landed for a reason.
var reachCorpus = []string{
	"워크스페이스 권한은 POSIX 소유권과 모드 비트가 결정한다.",
	"하나만 올리면 프로토콜 버전이 어긋난다.",
	"박예시는 아침에만 커피를 마신다.",
	"최견본은 채용 면접을 담당한다.",
	"이동하는 여명거리의 CTO이다.",
	"김여명은 회의를 길게 끄는 것을 싫어한다.",
	"여명거리와 고객사의 계약은 2026-01-01부터 효력이 생긴다.",
	"복합기는 2층 복도 끝에 있다.",
	"법인 카드 한도는 월 300만원이다.",
	"서버 백업은 매주 일요일 새벽에 돈다.",
	"신입은 첫 주에 보안 교육을 이수해야 한다.",
	"점심시간은 12시부터 13시까지다.",
}

// Each question is the situation in which the fact matters, written in
// vocabulary the fact does not use. The overlap assertion below is what keeps
// that claim honest rather than something eyeballed.
var reachProbes = []struct {
	question string
	wants    string
}{
	{"파일이 안 열린다는데 원인이 뭘까?", "POSIX"},
	{"배포하고 나서 기능이 동작을 안 해. 뭘 의심해?", "프로토콜"},
	{"오전 미팅에 음료 뭐 준비할까?", "커피"},
	{"지원자 만나는 일정은 누가 잡지?", "최견본"},
	{"대외 서명은 누구한테 받아야 하나?", "CTO"},
	{"이번 논의 짧게 끝내야 하는 이유가 있었나?", "김여명"},
	{"이 건으로 세금계산서 끊어도 되는 시점이 언제지?", "효력"},
	{"출력물 찾으러 어디로 가야 해?", "복도"},
	{"경비 결제 얼마까지 가능해?", "300만원"},
	{"주말에 디스크 작업 잡아도 괜찮나?", "백업"},
	{"입사자가 처음에 뭘 해야 하지?", "보안 교육"},
	{"외부 손님을 몇 시에 부르면 안 되나?", "점심"},
}

// sharedBigrams counts the two-character sequences a question and a proposition
// have in common. A descriptive probe shares many; an associative one shares
// none, and only the second kind tests whether a memory can be reached without
// echoing its wording.
func sharedBigrams(first string, second string) []string {
	bigrams := func(text string) map[string]bool {
		found := map[string]bool{}
		runes := []rune(strings.NewReplacer(" ", "", ".", "", "?", "", ",", "").Replace(text))
		for index := 0; index+1 < len(runes); index++ {
			found[string(runes[index:index+2])] = true
		}
		return found
	}
	left, right := bigrams(first), bigrams(second)
	shared := []string{}
	for gram := range left {
		if right[gram] {
			shared = append(shared, gram)
		}
	}
	sort.Strings(shared)
	return shared
}

func TestProbesAreSurfaceDisjoint(t *testing.T) {
	for _, probe := range reachProbes {
		target := ""
		for _, proposition := range reachCorpus {
			if strings.Contains(proposition, probe.wants) {
				target = proposition
				break
			}
		}
		if target == "" {
			t.Fatalf("프로브가 코퍼스에 없는 답을 원한다: %q", probe.wants)
		}
		shared := sharedBigrams(probe.question, target)
		if len(shared) > 1 {
			t.Errorf("표면이 겹쳐 연상 시험이 되지 않는다 (%d): %q ↔ %q\n  겹침: %v",
				len(shared), probe.question, target, shared)
			continue
		}
		t.Logf("  겹침 %d  %s", len(shared), probe.question)
	}
}

func TestReachWithAndWithoutRehearsals(t *testing.T) {
	client := newOllamaClient("qwen3.5:4b", "이동하", time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()

	run := func(withTriggers bool) (int, []string) {
		store, errorValue := openStore(
			filepath.Join(t.TempDir(), "reach.db"), client, client, registry, time.Now)
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		defer store.database.Close()

		identifiers := make([]string, len(reachCorpus))
		for index, proposition := range reachCorpus {
			identifiers[index], errorValue = store.insertMonadWithImportance(
				ctx, Monad{Content: proposition}, newIdentifier(), 4)
			if errorValue != nil {
				t.Fatal(errorValue)
			}
		}
		if withTriggers {
			store.triggerMaker = client
			for index, identifier := range identifiers {
				if errorValue := store.rehearse(ctx, identifier, reachCorpus[index]); errorValue != nil {
					t.Fatal(errorValue)
				}
			}
			var kept int
			store.database.QueryRowContext(ctx, `select count(*) from trigger_phrase`).Scan(&kept)
			t.Logf("  단서 %d개 유지, %d개 버림(과도한 일반화)", kept, len(store.droppedTriggers))
			for _, phrase := range store.droppedTriggers {
				t.Logf("      버림: %s", phrase)
			}
		}

		hits := 0
		lines := []string{}
		for _, probe := range reachProbes {
			ranked, errorValue := store.search(ctx, []Monad{{Content: probe.question}}, 3)
			if errorValue != nil {
				t.Fatal(errorValue)
			}
			mark, top := "✗", "(없음)"
			if len(ranked) > 0 {
				top = ranked[0].Memory.Content
				if strings.Contains(top, probe.wants) {
					hits++
					mark = "✓"
				}
			}
			lines = append(lines, fmt.Sprintf("  %s %-38s → %s", mark, probe.question, top))
		}
		return hits, lines
	}

	without, withoutLines := run(false)
	t.Log("=== 예행연습 없음 ===")
	for _, line := range withoutLines {
		t.Log(line)
	}
	with, withLines := run(true)
	t.Log("=== 예행연습 있음 ===")
	for _, line := range withLines {
		t.Log(line)
	}
	t.Log(strings.Repeat("=", 60))
	t.Logf("예행연습 없음 %d/%d · 있음 %d/%d", without, len(reachProbes), with, len(reachProbes))
}
