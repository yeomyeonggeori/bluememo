package bluememo

import (
	"sort"
	"strings"
)

func rankByLexeme(retrievable map[string]candidate, text string, depth int) []string {
	identifiers := make([]string, 0, len(retrievable))
	contents := make([]string, 0, len(retrievable))
	for memoryID, held := range retrievable {
		identifiers = append(identifiers, memoryID)
		contents = append(contents, held.memory.Content)
	}
	ranked := []string{}
	for _, index := range rankByOverlap(text, contents, depth) {
		ranked = append(ranked, identifiers[index])
	}
	return ranked
}

func rankByOverlap(query string, texts []string, depth int) []int {
	queryBigrams := characterBigrams(query)
	if len(queryBigrams) == 0 {
		return nil
	}
	type scored struct {
		index   int
		overlap int
	}
	matching := []scored{}
	for index, text := range texts {
		textBigrams := characterBigrams(text)
		overlap := 0
		for bigram := range queryBigrams {
			if textBigrams[bigram] {
				overlap++
			}
		}
		if overlap > 0 {
			matching = append(matching, scored{index: index, overlap: overlap})
		}
	}
	sort.SliceStable(matching, func(left int, right int) bool {
		if matching[left].overlap != matching[right].overlap {
			return matching[left].overlap > matching[right].overlap
		}
		return texts[matching[left].index] < texts[matching[right].index]
	})
	indexes := make([]int, 0, min(depth, len(matching)))
	for _, entry := range matching[:min(depth, len(matching))] {
		indexes = append(indexes, entry.index)
	}
	return indexes
}

func characterBigrams(text string) map[string]bool {
	bigrams := map[string]bool{}
	for _, field := range strings.Fields(strings.ToLower(text)) {
		characters := []rune(strings.Trim(field, ".,?!\"'()[]{}:;"))
		for index := 0; index+1 < len(characters); index++ {
			bigrams[string(characters[index:index+2])] = true
		}
	}
	return bigrams
}
