# Running bluememo through AMB

AMB owns the datasets, the answer prompt, the judge and the scoring for every
provider it reports, which is why a number measured here can be compared with
the ones it publishes and a number measured by this repository's own harness
cannot.

Clone [agent-memory-benchmark](https://github.com/vectorize-io/agent-memory-benchmark),
put the provider where its registry can see it, and register it:

```bash
cp benchmarks/amb/bluememo.py <amb>/src/memory_bench/memory/bluememo.py
```

Then add two lines to `src/memory_bench/memory/__init__.py`:

```python
from .bluememo import BluememoMemoryProvider
REGISTRY["bluememo"] = BluememoMemoryProvider
```

Start the server, which holds one store per `user_id` under the directory it is
given and needs `OPENROUTER_API_KEY`:

```bash
go run ./cmd/bench-server -directory /tmp/bluememo-banks
```

Then run a dataset. `BLUEMEMO_SERVER` points the provider elsewhere,
`BLUEMEMO_SOURCES=1` appends the notes the memories came from, and
`BLUEMEMO_RECALL=N` answers with N memories whatever the benchmark asked for:

```bash
cd <amb> && uv run memory-bench --dataset locomo --split locomo10 --memory bluememo
```

One LoCoMo conversation is a `--unit`, so a single one is a cheap first pass
before a full split.

## What a comparable number needs

AMB records the models each published run used, and a run with different ones
answers a different question. The LoCoMo rows it publishes were all produced
with the same two:

| | |
| --- | --- |
| answer | `gemini:gemini-3.1-pro-preview` |
| judge | `gemini:gemini-2.5-flash-lite` |

```bash
OMB_ANSWER_LLM=gemini OMB_ANSWER_MODEL=gemini-3.1-pro-preview \
OMB_JUDGE_LLM=gemini OMB_JUDGE_MODEL=gemini-2.5-flash-lite \
GEMINI_API_KEY=... uv run memory-bench --dataset locomo --split locomo10 --memory bluememo
```

The answering and the judging are charged to that key. The ingestion and the
recall are charged to `OPENROUTER_API_KEY`, through the server.

AMB's own published numbers for this split, for what the run is being compared
with: hindsight 0.9201 over 1540 questions at 36235 context tokens per query,
hybrid-search 0.7909 at 22157, cognee 0.8026 over 152 questions at 14724.
`hybrid-search` is chunk retrieval over the raw text with no memory system,
which is the comparison AMB's own README draws when it says that on most
instances dumping everything into context scores competitively because
retrieval has become the easy part.

## On asking for a different number than the benchmark did

AMB asks a provider for ten results, and a result means something different to
each one. Hindsight spends those ten slots on about 3600 tokens each; a bluememo
memory is around 29. So ten is the same count and a different budget, and the
axis this library is actually compared on is the context it costs.

`BLUEMEMO_RECALL` exists to answer that honestly: a run that supplies fifty
memories instead of ten says so, and the result records the context tokens it
used, which is what makes it comparable. A run that leaves the variable unset
answers with exactly what was asked.
