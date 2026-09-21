# Monad shape experiment

A throwaway store built to answer one question: is the shape proposed in
[#3](https://github.com/yeomyeonggeori/bluememo/issues/3) better than the one this
repository ships? It is a separate Go module, so `go build ./...` at the root ignores it.

The store keeps one table of self-contained sentences in SQLite, searches them with
brute-force cosine plus FTS5 trigram fused by RRF, and carries no access-control model at
all. The numbers below come from the tests in this directory.

## Shape

| | shipped | here |
|---|---|---|
| core lines | 1,756 | 541 |
| domain fields | `Fact`, 18 | `Monad`, 4 + 5 runtime |
| access control | `Reader` (5 fields), 3-way SQL gate, circle closure | none; the file is the boundary |

```
content              a sentence that stands on its own
is_static            permanent trait: exempt from eviction, member of the profile
occurred_at          when the event happened; range queries and eviction order
valid_until_phrase   the expiry wording, which the runtime turns into a date
origin_id            the decomposition batch, which gives sibling links for free
```

## Running

Needs [Ollama](https://ollama.com) on `:11434` with a generation model and
`embeddinggemma`. The tests name the models they use.

```
go test ./...                                   # every experiment
go test -run TestSiblingExpansion -v            # one of them
python3 benchmark/score.py                      # decomposition quality across models
python3 benchmark/score_slots.py                # the 5W1H slot variant
```

The benchmark scripts print a table of deterministic checks per model and output language.

## What the tests settled

| test | result |
|---|---|
| `decay_test.go` | a 5-value `kind` enum with per-value behaviour ties a single `is_static` boolean, 9/10 each |
| `decay_test.go` | removing decay from ranking scores 10/10; decay buries queries about the past |
| `sibling_test.go` | sibling expansion lifts coverage from 4/10 to 10/10 at the same result limit |
| `capacity_test.go` | accuracy is flat from 37 to 312 memories; near-duplicates break retrieval, volume does not |
| `recency_test.go` | every implementable recency scheme nets zero against plain similarity |

`recency_test.go` is worth reading before anyone proposes recency again. A hand-labelled
oracle reaches 10/10 and an embedding probe that replaces it reaches 9/10, the same as no
recency at all, so the gain belonged to the labels.

## What it does not have

The referent layer (`summary`, `full_text`, host path, hash, pin state), eviction, tombstones,
and write-time dedup. Dedup is the one the measurements point at: this store has a
`superseded_by` column and nothing that decides it, so the capacity numbers above are a
worst case.
