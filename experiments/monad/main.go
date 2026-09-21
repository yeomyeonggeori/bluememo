package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var registry = registryResolver{people: []person{
	{personID: "p-dongha", names: []string{"이동하", "동하"}},
	{personID: "p-yeomyeong-a", names: []string{"김여명"}},
	{personID: "p-yeomyeong-b", names: []string{"김여명"}},
	{personID: "p-yesi", names: []string{"박예시"}},
	{personID: "p-gyeonbon", names: []string{"최견본"}},
}}

func main() {
	generationModel := "gemma4:latest"
	if len(os.Args) > 1 {
		generationModel = os.Args[1]
	}
	today := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return today }

	client := newOllamaClient(generationModel, "이동하 (여명거리 CTO)", today)
	databasePath := "/tmp/bluememo-poc.db"
	os.Remove(databasePath)

	store, errorValue := openStore(databasePath, client, client, registry, clock)
	exitOn(errorValue)
	ctx := context.Background()

	fmt.Printf("모델: %s\n\n", generationModel)

	section("1. memorize — 한 문장이 여러 모나드가 된다")
	show(store.Memorize(ctx, "내 이름은 이동하고, 나이는 14살이야. 사과를 좋아해."))

	section("2. memorize — 출처·조건·만료가 명제 안에 남는가")
	show(store.Memorize(ctx, "김여명이 그러는데 박예시는 아침에만 커피를 마신대. 이번 분기만 나한테 한국어로 답해줘."))

	section("3. memorize — 문서 전문도 같은 연산이다")
	show(store.Memorize(ctx, strings.TrimSpace(`
배포 절차

릴리스는 admind와 capabilityd를 함께 올린다. 하나만 올리면 프로토콜이 어긋난다.
빌드 전에 현재 브랜치가 origin/main을 포함하는지 확인한다.
배포 후에는 실제로 도는 바이너리의 리비전을 확인한다. 종료 코드만 보면 안 된다.`)))

	section("4. recall — 패러프레이즈로 찾는다")
	recall(ctx, store, "내가 뭐 좋아한다고 했지?")
	recall(ctx, store, "릴리스할 때 뭘 같이 올려야 해?")

	section("5. forget — 사다리를 탄다")
	forget(ctx, store, "사과 좋아한다는 거 지워줘")
	forget(ctx, store, "배포 얘기 다 지워줘")

	section("6. 원장")
	dump(store)
}

func section(title string) { fmt.Printf("\n\033[1m── %s ──\033[0m\n", title) }

func show(memories []Memory, errorValue error) {
	exitOn(errorValue)
	for _, memory := range memories {
		annotation := ""
		if memory.ValidUntil != "" {
			annotation += "  ~" + memory.ValidUntil
		}
		if len(memory.ResolvedEntityIDs) > 0 {
			annotation += "  →" + strings.Join(memory.ResolvedEntityIDs, ",")
		}
		if len(memory.UnresolvedNames) > 0 {
			annotation += "  ?" + strings.Join(memory.UnresolvedNames, ",")
		}
		fmt.Printf("  [%-10s] %s%s\n", memory.Kind, memory.Content, annotation)
	}
}

func recall(ctx context.Context, store *Store, query string) {
	ranked, errorValue := store.Recall(ctx, query, 3)
	exitOn(errorValue)
	fmt.Printf("  ? %s\n", query)
	for _, candidate := range ranked {
		fmt.Printf("      %.4f  [%-10s] %s\n", candidate.Score, candidate.Memory.Kind, candidate.Memory.Content)
	}
}

func forget(ctx context.Context, store *Store, request string) {
	outcome, errorValue := store.Forget(ctx, request)
	exitOn(errorValue)
	fmt.Printf("  ! %s\n", request)
	for _, memory := range outcome.Forgotten {
		fmt.Printf("      지움: %s\n", memory.Content)
	}
	if len(outcome.NeedsApproval) > 0 {
		fmt.Printf("      여럿이 맞아서 확인이 필요함:\n")
		for _, candidate := range outcome.NeedsApproval {
			fmt.Printf("        - %s\n", candidate.Memory.Content)
		}
	}
	if len(outcome.Forgotten) == 0 && len(outcome.NeedsApproval) == 0 {
		fmt.Printf("      맞는 기억 없음\n")
	}
}

func dump(store *Store) {
	rows, errorValue := store.database.Query(`
		select kind, content, storage_strength, retrieval_strength,
		       case when forgotten_at is null then '' else 'forgotten' end
		from memory order by kind, created_at`)
	exitOn(errorValue)
	defer rows.Close()
	for rows.Next() {
		var kind, content, state string
		var storage, retrieval float64
		exitOn(rows.Scan(&kind, &content, &storage, &retrieval, &state))
		fmt.Printf("  [%-10s] S=%.2f R=%.2f %-10s %s\n", kind, storage, retrieval, state, content)
	}
	var tombstones int
	store.database.QueryRow(`select count(*) from tombstone`).Scan(&tombstones)
	fmt.Printf("  묘비 %d개\n", tombstones)
}

func exitOn(errorValue error) {
	if errorValue != nil {
		fmt.Fprintln(os.Stderr, "실패:", errorValue)
		os.Exit(1)
	}
}
