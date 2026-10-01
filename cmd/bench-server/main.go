package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/openrouter"
)

const identifierSeed = 20260301

type document struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	UserID    string `json:"user_id"`
	Timestamp string `json:"timestamp"`
}

type ingestRequest struct {
	Documents []document `json:"documents"`
}

type retrieveRequest struct {
	Query   string `json:"query"`
	K       int    `json:"k"`
	UserID  string `json:"user_id"`
	Sources bool   `json:"sources"`
}

type retrievedMemory struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	SourceIDs []string `json:"source_ids"`
}

type retrieveResponse struct {
	Memories []retrievedMemory `json:"memories"`
}

type clock struct {
	mutex   sync.Mutex
	current time.Time
}

func (dial *clock) now() time.Time {
	dial.mutex.Lock()
	defer dial.mutex.Unlock()
	return dial.current
}

func (dial *clock) set(instant time.Time) {
	dial.mutex.Lock()
	defer dial.mutex.Unlock()
	dial.current = instant
}

type bank struct {
	store *bluememo.Store
	clock *clock
}

type service struct {
	mutex       sync.Mutex
	banks       map[string]*bank
	directory   string
	client      *openrouter.Client
	recall      int
	sourceLimit int
}

var unsafeInName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func (running *service) bankFor(ctx context.Context, userID string) (*bank, error) {
	running.mutex.Lock()
	defer running.mutex.Unlock()
	if existing, isPresent := running.banks[userID]; isPresent {
		return existing, nil
	}
	name := unsafeInName.ReplaceAllString(userID, "-")
	if name == "" {
		name = "shared"
	}
	dial := &clock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store, errorValue := bluememo.Open(ctx, filepath.Join(running.directory, name+".db"), bluememo.Configuration{
		Embedder:           running.client,
		EmbeddingModel:     running.client.EmbeddingModel,
		Model:              running.client,
		Judge:              bluememo.DistributionJudge{Chooser: openrouter.NewDecisions(running.client)},
		Reranker:           openrouter.NewDecisions(running.client),
		RerankDepth:        20,
		EmbedTimeReference: true,
		RecallSources:      true,
		ClaimDuration:      time.Minute,
		Now:                dial.now,
		NewIdentifier:      identifiers(identifierSeed),
	})
	if errorValue != nil {
		return nil, errorValue
	}
	created := &bank{store: store, clock: dial}
	running.banks[userID] = created
	return created, nil
}

func identifiers(seed int64) func() string {
	var mutex sync.Mutex
	source := rand.New(rand.NewSource(seed))
	return func() string {
		mutex.Lock()
		defer mutex.Unlock()
		return fmt.Sprintf("%016x", source.Uint64())
	}
}

func (running *service) ingest(writer http.ResponseWriter, request *http.Request) {
	var parsed ingestRequest
	if errorValue := json.NewDecoder(request.Body).Decode(&parsed); errorValue != nil {
		http.Error(writer, errorValue.Error(), http.StatusBadRequest)
		return
	}
	for _, each := range parsed.Documents {
		held, errorValue := running.bankFor(request.Context(), each.UserID)
		if errorValue != nil {
			refuse(writer, request.URL.Path, errorValue)
			return
		}
		if instant, errorValue := time.Parse(time.RFC3339, each.Timestamp); errorValue == nil {
			held.clock.set(instant)
		}
		if errorValue := held.store.Memorize(request.Context(), bluememo.Note{Body: each.Content, GroupID: each.ID}); errorValue != nil {
			refuse(writer, request.URL.Path, errorValue)
			return
		}
		if _, errorValue := held.store.Settle(request.Context()); errorValue != nil {
			refuse(writer, request.URL.Path, errorValue)
			return
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (running *service) retrieve(writer http.ResponseWriter, request *http.Request) {
	var parsed retrieveRequest
	if errorValue := json.NewDecoder(request.Body).Decode(&parsed); errorValue != nil {
		http.Error(writer, errorValue.Error(), http.StatusBadRequest)
		return
	}
	held, errorValue := running.bankFor(request.Context(), parsed.UserID)
	if errorValue != nil {
		refuse(writer, request.URL.Path, errorValue)
		return
	}
	limit := parsed.K
	if limit <= 0 {
		limit = running.recall
	}
	result, errorValue := held.store.Recall(request.Context(), parsed.Query, limit)
	if errorValue != nil {
		refuse(writer, request.URL.Path, errorValue)
		return
	}
	bodies := map[string]string{}
	for _, source := range result.Sources {
		bodies[source.OriginID] = source.Body
	}
	answer := retrieveResponse{Memories: make([]retrievedMemory, 0, len(result.Memories))}
	for _, recalled := range result.Memories {
		content := recalled.Memory.Content
		if occurrence := recalled.Memory.TimeReference(time.UTC); occurrence != "" {
			content = "[" + occurrence + "] " + content
		}
		answer.Memories = append(answer.Memories, retrievedMemory{
			ID:        recalled.Memory.MemoryID,
			Content:   content,
			SourceIDs: []string{recalled.Memory.OriginID},
		})
	}
	if parsed.Sources {
		answer.Memories = append(answer.Memories, spokenEntries(result.Memories, bodies, running.sourceLimit)...)
	}
	writer.Header().Set("Content-Type", "application/json")
	if errorValue := json.NewEncoder(writer).Encode(answer); errorValue != nil {
		log.Println("encode retrieve response:", errorValue)
	}
}

func spokenEntries(memories []bluememo.RecalledMemory, bodies map[string]string, limit int) []retrievedMemory {
	var spoken []retrievedMemory
	taken := map[string]bool{}
	for _, recalled := range memories {
		origin := recalled.Memory.OriginID
		body, isKnown := bodies[origin]
		if !isKnown || taken[origin] {
			continue
		}
		taken[origin] = true
		spoken = append(spoken, retrievedMemory{
			ID:        "said-" + origin,
			Content:   "What was said:\n" + body,
			SourceIDs: []string{origin},
		})
		if len(spoken) == limit {
			break
		}
	}
	return spoken
}

func (running *service) reset(writer http.ResponseWriter, request *http.Request) {
	running.mutex.Lock()
	defer running.mutex.Unlock()
	for _, held := range running.banks {
		held.store.Close()
	}
	running.banks = map[string]*bank{}
	entries, errorValue := os.ReadDir(running.directory)
	if errorValue != nil {
		refuse(writer, request.URL.Path, errorValue)
		return
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".db" {
			os.Remove(filepath.Join(running.directory, entry.Name()))
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func refuse(writer http.ResponseWriter, route string, errorValue error) {
	log.Printf("%s failed: %v", route, errorValue)
	http.Error(writer, errorValue.Error(), http.StatusInternalServerError)
}

func main() {
	address := flag.String("address", ":8713", "address to listen on")
	directory := flag.String("directory", "", "directory holding one store per user")
	recall := flag.Int("recall", 50, "memories returned when a request names no limit")
	sourceLimit := flag.Int("sources", 4, "distinct notes appended after the memories when a request asks for them")
	flag.Parse()

	credential := os.Getenv("OPENROUTER_API_KEY")
	if credential == "" {
		log.Fatal("OPENROUTER_API_KEY is not set")
	}
	if *directory == "" {
		log.Fatal("-directory is required")
	}
	if errorValue := os.MkdirAll(*directory, 0o755); errorValue != nil {
		log.Fatal(errorValue)
	}
	running := &service{
		banks:       map[string]*bank{},
		directory:   *directory,
		client:      openrouter.New(credential),
		recall:      *recall,
		sourceLimit: *sourceLimit,
	}
	handler := http.NewServeMux()
	handler.HandleFunc("POST /ingest", running.ingest)
	handler.HandleFunc("POST /retrieve", running.retrieve)
	handler.HandleFunc("POST /reset", running.reset)
	handler.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write([]byte("ok"))
	})
	log.Println("bluememo bench server on", *address, "storing in", *directory)
	log.Fatal(http.ListenAndServe(*address, handler))
}
