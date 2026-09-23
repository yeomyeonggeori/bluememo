package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func main() {
	if errorValue := run(); errorValue != nil {
		fmt.Fprintln(os.Stderr, errorValue)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	directory, errorValue := os.MkdirTemp("", "bluememo-example")
	if errorValue != nil {
		return errorValue
	}
	defer os.RemoveAll(directory)

	model := bluememotest.NewScriptedModel()
	model.QueueDecomposition(
		bluememo.Proposition{Content: "이샘플은 여명거리의 CTO이다.", IsStatic: true},
		bluememo.Proposition{Content: "이샘플은 릴리스 때 admind와 capabilityd를 함께 올린다."},
	)
	store, errorValue := bluememo.Open(ctx, filepath.Join(directory, "memory.db"), bluememo.Configuration{
		Embedder:       bluememotest.HashEmbedder{},
		EmbeddingModel: "hash",
		Model:          model,
		Judge:          &bluememotest.ScriptedJudge{},
	})
	if errorValue != nil {
		return errorValue
	}
	defer store.Close()

	if errorValue := store.Memorize(ctx, bluememo.Note{Body: "나 여명거리 CTO고, 릴리스 땐 admind랑 capabilityd 같이 올려", SpeakerName: "이샘플", IsExplicit: true}); errorValue != nil {
		return errorValue
	}
	report, errorValue := store.Settle(ctx)
	if errorValue != nil {
		return errorValue
	}
	fmt.Printf("settled: %+v\n", report)

	profile, errorValue := store.Profile(ctx)
	if errorValue != nil {
		return errorValue
	}
	for _, memory := range profile {
		fmt.Println("profile:", memory.Content)
	}
	result, errorValue := store.Recall(ctx, "릴리스 때 뭘 같이 올려?", 3)
	if errorValue != nil {
		return errorValue
	}
	for _, entry := range result.Memories {
		fmt.Printf("recall (%s): %s\n", result.Mode, entry.Memory.Content)
	}
	return nil
}
