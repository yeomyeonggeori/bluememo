# bluememo

*Long-term memory for an agent serving one person: a single SQLite file of self-contained sentences that settles what it hears, recalls without a model call, and forgets under pressure.*

[![check](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml/badge.svg)](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

> **Status: pre-alpha.** The exported API, the schema and the prompts change without notice.

```bash
ollama pull qwen3.5:4b && ollama pull embeddinggemma
go run ./examples/quickstart
```

## From an agent

This repository is an [Agent Plugins](https://agent-plugins.org) package, so any client that speaks the standard can keep its own memory here:

```bash
go install github.com/yeomyeonggeori/bluememo/cmd/bluememo@latest
```

`mcp.json` declares `bluememo mcp`, a Model Context Protocol server over stdio with three tools: `remember`, `search` and `forget`. It opens `~/.bluememo/me.db` and asks for nothing else. With no embedder a recall ranks by wording and says so, so there is no key, no model and no setup; configuring one adds the vector lane.

The documentation is [DOCS.md](DOCS.md), published at [bluememo.intern.kim](https://bluememo.intern.kim). The design and the measurements behind it are in [issue #3](https://github.com/yeomyeonggeori/bluememo/issues/3).

## Where it stands

On LoCoMo, scored by [agent-memory-benchmark](https://github.com/vectorize-io/agent-memory-benchmark) so the prompts, the answering model and the judge are the same for every row:

| | correct of 885 | context tokens |
|---|---|---|
| hindsight | 811 | 36,235 |
| bluememo | 806 | 6,816 |
| cognee | 710 | 14,724 |
| hybrid search, no memory system | 700 | 22,157 |

The top two rows are the same 885 questions answered one at a time, so they can be compared question by question: 775 both answer, 31 only bluememo, 36 only hindsight. McNemar gives χ² 0.24, p 0.62. The two systems answer different questions at the same rate, and bluememo spends a sixth of the context doing it.

The split by category says where the difference lives. Of the questions only one system answers, bluememo takes 6 multi-hop against hindsight's 2, which is what atomic facts are for. Hindsight takes 11 temporal against bluememo's 4, and a large part of that is phrasing: those golds are written against another date ("the week before 9 June 2023"), and hindsight carries the raw turn, so "last week" survives into its answer in the gold's own words.

[TRIED.md](TRIED.md) has what was measured and dropped, with the numbers.

| path | holds |
|---|---|
| `.` | the store, settling, judging, recall, forgetting, sweeping |
| `migrations/` | the schema, embedded and applied by `Open` through `user_version` |
| `cmd/bluememo/` | `bluememo mcp`, the protocol server, and the plugin's own guards |
| `plugin.json`, `mcp.json`, `skills/` | the Agent Plugins package, with the plugin root at the repository root |
| `ollama/` | an adapter that runs the three ports on local Ollama models |
| `openrouter/` | an adapter that runs them on OpenRouter models |
| `examples/` | `quickstart` on real local models, `recall` on scripted ones |
| `bluememotest/` | a hash embedder, a table embedder, a scripted model, judge and chooser |
| `docs/` | the documentation site, generated from `DOCS.md` |
| `TRIED.md` | what was measured against this store and rejected |
