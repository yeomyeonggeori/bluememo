package bluememotest

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"sync"

	"github.com/yeomyeonggeori/bluememo"
)

const EmbeddingDimensionCount = 256

type HashEmbedder struct {
	Failure error
}

func (embedder HashEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	if embedder.Failure != nil {
		return nil, embedder.Failure
	}
	return Embed(text), nil
}

func (embedder HashEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if embedder.Failure != nil {
		return nil, embedder.Failure
	}
	embeddings := make([][]float32, 0, len(texts))
	for _, text := range texts {
		embeddings = append(embeddings, Embed(text))
	}
	return embeddings, nil
}

func Embed(text string) []float32 {
	embedding := make([]float32, EmbeddingDimensionCount)
	for _, term := range strings.Fields(strings.ToLower(text)) {
		hasher := fnv.New32a()
		_, _ = hasher.Write([]byte(strings.Trim(term, ".,?!")))
		embedding[int(hasher.Sum32()%uint32(EmbeddingDimensionCount))] += 1
	}
	var norm float64
	for _, value := range embedding {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		embedding[0] = 1
		return embedding
	}
	scale := float32(1 / math.Sqrt(norm))
	for index := range embedding {
		embedding[index] *= scale
	}
	return embedding
}

type ScriptedModel struct {
	mutex     sync.Mutex
	responses map[string][]string
	Requests  []bluememo.StructuredRequest
}

func NewScriptedModel() *ScriptedModel {
	return &ScriptedModel{responses: map[string][]string{}}
}

func (scripted *ScriptedModel) QueueDecomposition(propositions ...bluememo.Proposition) {
	for index := range propositions {
		if propositions[index].Expiry == "" {
			propositions[index].Expiry = bluememo.ExpiryNone
		}
	}
	if propositions == nil {
		propositions = []bluememo.Proposition{}
	}
	scripted.queue("memory_decomposition", map[string]any{"propositions": propositions})
}

func (scripted *ScriptedModel) QueueTriggers(phrases ...string) {
	if phrases == nil {
		phrases = []string{}
	}
	scripted.queue("memory_trigger", map[string]any{"phrases": phrases})
}

func (scripted *ScriptedModel) queue(schemaName string, response any) {
	scripted.mutex.Lock()
	defer scripted.mutex.Unlock()
	document, _ := json.Marshal(response)
	scripted.responses[schemaName] = append(scripted.responses[schemaName], string(document))
}

func (scripted *ScriptedModel) GenerateStructured(_ context.Context, request bluememo.StructuredRequest) (string, error) {
	scripted.mutex.Lock()
	defer scripted.mutex.Unlock()
	scripted.Requests = append(scripted.Requests, request)
	queued := scripted.responses[request.SchemaName]
	if len(queued) == 0 {
		if request.SchemaName == "memory_trigger" {
			return `{"phrases":[]}`, nil
		}
		return "", errors.New("scripted model has no " + request.SchemaName + " response left")
	}
	scripted.responses[request.SchemaName] = queued[1:]
	return queued[0], nil
}

type ScriptedJudge struct {
	mutex      sync.Mutex
	judgements []bluememo.Judgement
	Seen       [][]string
}

func (judge *ScriptedJudge) Queue(judgements ...bluememo.Judgement) {
	judge.mutex.Lock()
	defer judge.mutex.Unlock()
	judge.judgements = append(judge.judgements, judgements...)
}

func (judge *ScriptedJudge) Judge(_ context.Context, _ string, candidates []string) (bluememo.Judgement, error) {
	judge.mutex.Lock()
	defer judge.mutex.Unlock()
	judge.Seen = append(judge.Seen, candidates)
	if len(judge.judgements) == 0 {
		return bluememo.Judgement{Relation: bluememo.RelationUnrelated, TargetIndex: -1, Importance: bluememo.DefaultImportance}, nil
	}
	next := judge.judgements[0]
	judge.judgements = judge.judgements[1:]
	return next, nil
}

type ScriptedChooser struct {
	Distributions map[string]map[string]float64
	Failure       error
}

func (chooser ScriptedChooser) Choose(_ context.Context, request bluememo.ChoiceRequest) (map[string]float64, error) {
	if chooser.Failure != nil {
		return nil, chooser.Failure
	}
	return chooser.Distributions[request.Instruction], nil
}

type TableEmbedder struct {
	Vectors map[string][]float32
}

func (embedder TableEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return embedder.embed(text), nil
}

func (embedder TableEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, 0, len(texts))
	for _, text := range texts {
		embeddings = append(embeddings, embedder.embed(text))
	}
	return embeddings, nil
}

func (embedder TableEmbedder) embed(text string) []float32 {
	if vector, isListed := embedder.Vectors[text]; isListed {
		return vector
	}
	return Embed(text)
}

func Axes(weights map[int]float32) []float32 {
	vector := make([]float32, EmbeddingDimensionCount)
	for axis, weight := range weights {
		vector[axis] = weight
	}
	return vector
}
