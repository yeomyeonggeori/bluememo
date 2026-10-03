package bluememo_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func TestAReaderWhoMayOnlyReadTheFileRecallsWithoutChangingIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes every file, so nothing here would be read-only")
	}
	keeper := newFixture(t)
	keeper.settle(t, "박예시 keeps the quarterly ledger.", statement("박예시 keeps the quarterly ledger."))
	withoutWriting(t, keeper.path, keeper.path+"-wal", keeper.path+"-shm")

	reader, errorValue := bluememo.OpenToRead(context.Background(), keeper.path, bluememo.Configuration{Embedder: bluememotest.HashEmbedder{}, EmbeddingModel: "hash"})
	if errorValue != nil {
		t.Fatalf("a reader who may only read the file could not open it: %v", errorValue)
	}
	defer reader.Close()
	result, errorValue := reader.Recall(context.Background(), "who keeps the quarterly ledger", 5)
	if errorValue != nil {
		t.Fatalf("a read-only recall failed: %v", errorValue)
	}

	if len(result.Memories) == 0 || result.Memories[0].Memory.Content != "박예시 keeps the quarterly ledger." {
		t.Fatalf("a read-only recall answered %v", recalledContents(result))
	}
	if held := keeper.memoryWithContent(t, "박예시 keeps the quarterly ledger."); !held.LastRecalledAt.IsZero() {
		t.Fatalf("a read-only recall reinforced the memory at %v", held.LastRecalledAt)
	}
}

func TestTheKeeperReinforcesWhatAReaderRecalled(t *testing.T) {
	keeper := newFixture(t)
	keeper.settle(t, "박예시 keeps the quarterly ledger.", statement("박예시 keeps the quarterly ledger."))
	held := keeper.memoryWithContent(t, "박예시 keeps the quarterly ledger.")
	keeper.clock.advance(24 * time.Hour)

	if errorValue := keeper.store.Reinforce(context.Background(), []string{held.MemoryID, "no-such-memory"}); errorValue != nil {
		t.Fatalf("reinforce: %v", errorValue)
	}

	reinforced := keeper.memoryWithContent(t, "박예시 keeps the quarterly ledger.")
	if !reinforced.LastRecalledAt.Equal(keeper.clock.now()) || reinforced.StorageStrength <= held.StorageStrength {
		t.Fatalf("reinforcing left the memory at strength %v, recalled %v", reinforced.StorageStrength, reinforced.LastRecalledAt)
	}
}

func TestAReaderCreatesNoStore(t *testing.T) {
	path := t.TempDir() + "/memory.db"

	_, errorValue := bluememo.OpenToRead(context.Background(), path, bluememo.Configuration{})

	if !errors.Is(errorValue, os.ErrNotExist) {
		t.Fatalf("reading a store nobody wrote answered %v", errorValue)
	}
	if _, statFailure := os.Stat(path); !errors.Is(statFailure, os.ErrNotExist) {
		t.Fatal("reading a store nobody wrote created one")
	}
}

func withoutWriting(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if errorValue := os.Chmod(path, 0o444); errorValue != nil {
			t.Fatalf("chmod %s: %v", path, errorValue)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	}
}
