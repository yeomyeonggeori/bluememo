// bench-retrieval measures what a recall puts in front of a model, without
// asking a model anything. It answers a read-path question in the time the
// embeddings take, so a change to ranking is measured in minutes rather than
// in the hours a full benchmark spends settling stores and judging answers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/openrouter"
)

type conversation struct {
	SampleID  string `json:"sample_id"`
	Questions []struct {
		Question          string `json:"question"`
		Answer            any    `json:"answer"`
		AdversarialAnswer string `json:"adversarial_answer"`
		Category          int    `json:"category"`
	} `json:"qa"`
}

type outcome struct {
	category     int
	rankOfGold   int
	found        bool
	question     string
	goldText     string
	nearest      string
	nearestShare float64
}

var wordPattern = regexp.MustCompile(`[a-z0-9]+`)

var stopWords = map[string]bool{}

func init() {
	for _, word := range strings.Fields(`the a an of and to in on is was were be been at for with that this it he she they i you my his her their our as by from or not no yes do does did has have had will would can could what when where who how why there here them us me we so if then than too very just more most some any all each`) {
		stopWords[word] = true
	}
}

// monthNumbers lets a gold answer written "7 May 2023" share words with a
// memory that carries the same day as 2023-05-07. Without it every temporal
// question scores as a miss on a store that answers it correctly.
var monthNumbers = map[string]string{
	"january": "1", "february": "2", "march": "3", "april": "4",
	"may": "5", "june": "6", "july": "7", "august": "8",
	"september": "9", "october": "10", "november": "11", "december": "12",
	"jan": "1", "feb": "2", "mar": "3", "apr": "4", "jun": "6",
	"jul": "7", "aug": "8", "sep": "9", "sept": "9", "oct": "10", "nov": "11", "dec": "12",
}

func contentWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range wordPattern.FindAllString(strings.ToLower(text), -1) {
		if stopWords[word] {
			continue
		}
		if number, isMonth := monthNumbers[word]; isMonth {
			words[number] = true
			continue
		}
		words[strings.TrimLeft(word, "0")] = true
	}
	delete(words, "")
	return words
}

func shareOf(gold map[string]bool, present map[string]bool) float64 {
	if len(gold) == 0 {
		return 0
	}
	shared := 0
	for word := range gold {
		if present[word] {
			shared++
		}
	}
	return float64(shared) / float64(len(gold))
}

func sharedShare(gold map[string]bool, text string) float64 {
	return shareOf(gold, contentWords(text))
}

func main() {
	storePath := flag.String("store", "", "store to read")
	datasetPath := flag.String("dataset", "", "locomo10.json")
	unit := flag.String("unit", "", "sample_id to score")
	limit := flag.Int("limit", 50, "memories a recall returns")
	rerankDepth := flag.Int("rerank-depth", 0, "candidates the reranker sees (0 leaves the library default)")
	siblings := flag.Bool("siblings", true, "let the leading memory bring its note's other statements")
	workers := flag.Int("workers", 8, "questions in flight")
	showFailures := flag.Int("show-failures", 0, "print this many questions whose gold never appeared")
	onlyCategory := flag.Int("category", 0, "score only this LoCoMo category (0 scores all)")
	flag.Parse()
	if *storePath == "" || *datasetPath == "" || *unit == "" {
		log.Fatal("-store, -dataset and -unit are required")
	}
	credential := os.Getenv("OPENROUTER_API_KEY")
	if credential == "" {
		log.Fatal("OPENROUTER_API_KEY is not set")
	}
	client := openrouter.New(credential)
	store, errorValue := bluememo.Open(context.Background(), *storePath, bluememo.Configuration{
		Embedder:           client,
		Model:              client,
		Reranker:           openrouter.NewDecisions(client),
		Judge:              bluememo.DistributionJudge{Chooser: openrouter.NewDecisions(client)},
		EmbeddingModel:     openrouter.DefaultEmbedModel,
		RerankDepth:        *rerankDepth,
		Location:           time.UTC,
		EmbedTimeReference: true,
	})
	if errorValue != nil {
		log.Fatalf("open %s: %v", *storePath, errorValue)
	}
	defer store.Close()
	if !*siblings {
		log.Print("note: -siblings=false is not wired; the library always expands the leading memory")
	}

	raw, errorValue := os.ReadFile(*datasetPath)
	if errorValue != nil {
		log.Fatal(errorValue)
	}
	var conversations []conversation
	if errorValue := json.Unmarshal(raw, &conversations); errorValue != nil {
		log.Fatal(errorValue)
	}

	type task struct {
		question string
		gold     map[string]bool
		category int
		goldText string
	}
	tasks := []task{}
	for _, each := range conversations {
		if each.SampleID != *unit {
			continue
		}
		for _, question := range each.Questions {
			goldText := fmt.Sprint(question.Answer)
			if question.Answer == nil {
				goldText = question.AdversarialAnswer
			}
			gold := contentWords(goldText)
			if len(gold) == 0 {
				continue
			}
			if *onlyCategory != 0 && question.Category != *onlyCategory {
				continue
			}
			tasks = append(tasks, task{question.Question, gold, question.Category, goldText})
		}
	}
	log.Printf("%d questions, limit %d, rerank depth %d", len(tasks), *limit, *rerankDepth)

	outcomes := make([]outcome, len(tasks))
	queue := make(chan int)
	group := sync.WaitGroup{}
	for range *workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range queue {
				current := tasks[index]
				result, errorValue := store.Recall(context.Background(), current.question, *limit)
				if errorValue != nil {
					log.Printf("recall failed: %v", errorValue)
					outcomes[index] = outcome{category: current.category, rankOfGold: -1}
					continue
				}
				// An answer is read from everything a recall returns, and a
				// multi-hop answer is a union across memories by definition, so
				// coverage accumulates down the ranking rather than being asked
				// of one memory at a time.
				record := outcome{category: current.category, rankOfGold: -1}
				gathered, best, bestText := map[string]bool{}, 0.0, ""
				for rank, memory := range result.Memories {
					for word := range contentWords(memory.Memory.Content) {
						gathered[word] = true
					}
					share := shareOf(current.gold, gathered)
					if share > best {
						best, bestText = share, memory.Memory.Content
					}
					if share >= 0.7 {
						record.found, record.rankOfGold = true, rank+1
						break
					}
				}
				record.question, record.goldText, record.nearest, record.nearestShare = current.question, current.goldText, bestText, best
				outcomes[index] = record
			}
		}()
	}
	for index := range tasks {
		queue <- index
	}
	close(queue)
	group.Wait()

	found, ranks := 0, []int{}
	byCategory := map[int][2]int{}
	for _, record := range outcomes {
		counts := byCategory[record.category]
		counts[1]++
		if record.found {
			found++
			ranks = append(ranks, record.rankOfGold)
			counts[0]++
		}
		byCategory[record.category] = counts
	}
	sort.Ints(ranks)
	fmt.Printf("hit@%d = %.4f  (%d/%d)\n", *limit, float64(found)/float64(len(outcomes)), found, len(outcomes))
	if len(ranks) > 0 {
		fmt.Printf("rank of gold: median %d  p90 %d  max %d\n",
			ranks[len(ranks)/2], ranks[min(len(ranks)*9/10, len(ranks)-1)], ranks[len(ranks)-1])
		for _, cut := range []int{5, 10, 20, 50} {
			within := 0
			for _, rank := range ranks {
				if rank <= cut {
					within++
				}
			}
			fmt.Printf("  hit@%-3d = %.4f\n", cut, float64(within)/float64(len(outcomes)))
		}
	}
	if *showFailures > 0 {
		shown := 0
		for _, record := range outcomes {
			if record.found || shown >= *showFailures {
				continue
			}
			shown++
			fmt.Printf("\n  [%d] Q: %s\n      gold   : %s\n      nearest: %s (%.2f shared)\n",
				record.category, record.question, truncate(record.goldText, 110), truncate(record.nearest, 110), record.nearestShare)
		}
	}
	categories := []int{}
	for category := range byCategory {
		categories = append(categories, category)
	}
	sort.Ints(categories)
	for _, category := range categories {
		counts := byCategory[category]
		fmt.Printf("  category %d: %.4f (%d/%d)\n", category, float64(counts[0])/float64(counts[1]), counts[0], counts[1])
	}
}

func truncate(text string, width int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) <= width {
		return collapsed
	}
	return collapsed[:width] + "..."
}
