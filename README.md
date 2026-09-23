# bluememo

*Long-term memory for an agent serving one person: a single SQLite file of self-contained sentences that settles what it hears, recalls without a model call, and forgets under pressure.*

[![check](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml/badge.svg)](https://github.com/yeomyeonggeori/bluememo/actions/workflows/check.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

> **Status: pre-alpha.** The exported API, the schema and the prompts change without notice.

```bash
go get github.com/yeomyeonggeori/bluememo
go run ./examples/recall
```

The documentation is [DOCS.md](DOCS.md), published at [bluememo.intern.kim](https://bluememo.intern.kim). The design and the measurements behind it are in [issue #3](https://github.com/yeomyeonggeori/bluememo/issues/3).

| path | holds |
|---|---|
| `.` | the store, settling, judging, recall, forgetting, sweeping |
| `migrations/` | the schema, embedded and applied by `Open` through `user_version` |
| `bluememotest/` | a hash embedder, a table embedder, a scripted model, judge and chooser |
| `docs/` | the documentation site, generated from `DOCS.md` |
