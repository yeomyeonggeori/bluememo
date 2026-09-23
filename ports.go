package bluememo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type Embedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

type StructuredRequest struct {
	SchemaName     string
	SchemaDocument string
	Instruction    string
	Subject        string
}

type LanguageModel interface {
	GenerateStructured(ctx context.Context, request StructuredRequest) (string, error)
}

type EntityResolver interface {
	Resolve(content string) (resolvedEntityIDs []string, unresolvedNames []string)
}

func NewIdentifier() string {
	identifierBytes := make([]byte, 16)
	if _, errorValue := rand.Read(identifierBytes); errorValue != nil {
		panic("crypto/rand failed: " + errorValue.Error())
	}
	return hex.EncodeToString(identifierBytes)
}
