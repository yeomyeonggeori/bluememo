package bluememo

import (
	"encoding/binary"
	"errors"
	"math"
)

var ErrInvalidEmbedding = errors.New("an embedding must be non-empty and finite")

func ValidateEmbedding(embedding []float32) error {
	if len(embedding) == 0 {
		return ErrInvalidEmbedding
	}
	for _, value := range embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return ErrInvalidEmbedding
		}
	}
	return nil
}

func cosineSimilarity(first []float32, second []float32) float64 {
	if len(first) != len(second) {
		return 0
	}
	var dotProduct, firstNorm, secondNorm float64
	for index := range first {
		dotProduct += float64(first[index]) * float64(second[index])
		firstNorm += float64(first[index]) * float64(first[index])
		secondNorm += float64(second[index]) * float64(second[index])
	}
	if firstNorm == 0 || secondNorm == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(firstNorm) * math.Sqrt(secondNorm))
}

func meanSimilarity(embedding []float32, others [][]float32) float64 {
	if len(others) == 0 {
		return 0
	}
	total := 0.0
	for _, other := range others {
		total += cosineSimilarity(embedding, other)
	}
	return total / float64(len(others))
}

func encodeEmbedding(embedding []float32) []byte {
	encoded := make([]byte, 4*len(embedding))
	for index, value := range embedding {
		binary.LittleEndian.PutUint32(encoded[4*index:], math.Float32bits(value))
	}
	return encoded
}

func decodeEmbedding(encoded []byte) []float32 {
	embedding := make([]float32, len(encoded)/4)
	for index := range embedding {
		embedding[index] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*index:]))
	}
	return embedding
}
