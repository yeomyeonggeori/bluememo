package main

import (
	"context"
	"fmt"
	"log"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/ollama"
)

func main() {
	ctx := context.Background()
	model := ollama.New("qwen3.5:4b", "embeddinggemma")

	store, errorValue := bluememo.Open(ctx, "alex.db", bluememo.Configuration{
		Embedder:       model,
		EmbeddingModel: model.EmbeddingModel,
		Model:          model,
		Judge:          bluememo.DistributionJudge{Chooser: model},
	})
	if errorValue != nil {
		log.Fatal(errorValue)
	}
	defer store.Close()

	store.Memorize(ctx, bluememo.Note{
		SpeakerName: "Alex",
		Body:        "I lead the payments team. Send me meeting notes in Markdown, and I'm off every Friday this month.",
	})
	if _, errorValue := store.Settle(ctx); errorValue != nil {
		log.Fatal(errorValue)
	}

	result, errorValue := store.Recall(ctx, "How should I send Alex the notes from today's meeting?", 3)
	if errorValue != nil {
		log.Fatal(errorValue)
	}
	for _, recalled := range result.Memories {
		fmt.Println(recalled.Memory.Content)
	}
}
