package bluememo_test

import (
	"context"
	"os"
	"testing"

	bluememo "github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestAStoreFromAnEarlierSchemaUpgradesAndKeepsItsMemories(t *testing.T) {
	path := os.Getenv("BLUEMEMO_OLD_STORE")
	if path == "" {
		t.Skip("set BLUEMEMO_OLD_STORE to a store written by an earlier schema")
	}
	store, errorValue := bluememo.Open(context.Background(), path, bluememo.Configuration{
		Embedder: bluememotest.HashEmbedder{}, EmbeddingModel: "hash", Model: &bluememotest.ScriptedModel{}, Judge: &bluememotest.ScriptedJudge{},
	})
	if errorValue != nil {
		t.Fatalf("upgrade failed: %v", errorValue)
	}
	defer store.Close()
	memories, errorValue := store.Memories(context.Background())
	if errorValue != nil || len(memories) == 0 {
		t.Fatalf("memories after upgrade: %d (%v)", len(memories), errorValue)
	}
	t.Logf("upgraded and read %d memories", len(memories))
}
