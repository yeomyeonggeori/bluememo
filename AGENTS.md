# AGENTS.md

- bluememo depends on nothing but the standard library and `modernc.org/sqlite`. A host adapts its own model, embedder, judge and identity types at the edge; do not import a host here.
- One file is one person's memory. Do not add an access-control model: the operating system decides who can open the file.
- The model judges meaning (what a sentence says, whether it is static, which period ends it, how it relates to what is held); the runtime supplies facts (candidates, dates, time) and computes what can be computed. Never merge or supersede memories by a similarity threshold.
- Time never enters ranking. It orders eviction.
- Every enum is declared once in Go. `canon_test.go` fails when a schema CHECK or the decomposition schema drifts from it.
- `migrations/` is the schema of record. Add the next file; never edit an applied one.
- No comments in code, full names, gofmt clean.
