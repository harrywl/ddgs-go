# PLAN — execution & delegation map

Read SPEC.md first; GAP-ANALYSIS.md has the evidence base.

## Streams & ownership

| Stream | Owner | Worktree / branch | Files touched (exclusive) | Est. |
|---|---|---|---|---|
| A · engine drift (D1–D3, M4) | SubAgent | `wt-a` / `stream-a` | registry.go, engine_text.go, engine_misc.go, extract.go, parity tests, testdata/ | 30m |
| B · CLI | SubAgent | `wt-b` / `stream-b` | cmd/ddgs/**, internal/output/** | 60m |
| C · API server | SubAgent | `wt-c` / `stream-c` | cmd/ddgs-api/**, internal/apiserver/** | 60m |
| D · MCP server | SubAgent | `wt-d` / `stream-d` | cmd/ddgs-mcp/**, internal/mcpserver/** | 45m |
| E · repo hygiene + merge | main agent (me) | `repo` / `main` | README, Makefile, CI, CHANGELOG, LICENSE, docs/ | after A–D |

File sets are disjoint by design → A–D run in parallel in separate git
worktrees; E merges everything, does hygiene, runs the §8 verification matrix,
and pushes to origin/main.

## Merge order (E)

1. `git merge stream-a` (library correctness first)
2. `git merge stream-b`, `stream-c`, `stream-d` (disjoint; conflicts unlikely —
   only go.mod/go.sum if deps were added; resolve by union + `go mod tidy`)
3. Full gate: gofmt, vet, `go test ./...`, `go test -race ./...`, build 3 bins
4. Hygiene: README rewrite, Makefile, CI/release workflows, CHANGELOG, LICENSE
   dual copyright, move GAP/SPEC/PLAN → docs/
5. Live smoke: cli/api/mcp + Python cross-check (network permitting)
6. Tag `v9.16.0-go.1`, push main + tag → GitHub release via CI

## SubAgent contract (all streams)

- Work ONLY inside your worktree; commit to your branch; do NOT push, do NOT
  touch other worktrees or `repo/`.
- Env: `export TMPDIR=/tmp GOPROXY=https://goproxy.cn,direct GOTOOLCHAIN=auto`
  (TMPDIR override is mandatory — default points at a nonexistent Android path).
- Python source of truth: `/var/minis/workspace/ddgs-port/py-ddgs/`.
- Gates before finishing: gofmt clean, go vet clean, go test ./... green,
  go build ./... green.
- Report back: files changed, test results, any spec deviations (+reason).
