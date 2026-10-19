# Pipeline Log — how this port was produced

A chronological record of the work that took `dreamyang-liu/ddgs-go` (a single-commit
Go port of [deedy5/ddgs](https://github.com/deedy5/ddgs)) to a full-surface release
at parity with Python ddgs **v9.16.0**.

Written after the fact, from the session's own tool logs. All identifiers below are
non-sensitive by construction; no API keys, tokens, account names or private
configuration values are reproduced. See [Redaction](#9-redaction) for what was
deliberately left out.

- **Repo:** `harrywl/ddgs-go`
- **Tag:** `v9.16.0-go.1`
- **Host platform:** Android (aarch64), Minis app PRoot sandbox, Alpine Linux
- **Date:** 2026-10-19

---

## 1. Objective

The user's brief, verbatim in intent:

> Go 版本是以 Python 版本为源头用 Go 重写的，可能功能上没有对齐。借用 Go 版本的这个
> Repo，然后从 Python 版本中继续学习写成 Go 版本。
>
> 1. 先把仓库克隆下来
> 2. 比较不同
> 3. 写一个计划
> 4. 写出新的 Go 版本
> 5. 在我自己的仓库里面新建一个 Repo
>
> 写出详细的 Spec，然后用 SubAgent 子代理去执行任务。

Two constraints were added mid-flight:

- **No Windows target.** Release platforms reduced to `linux/arm64`, `linux/arm`,
  `darwin/arm64`.
- **Stream B must run on DeepSeek V4.1 Flash**, not the model it started on.

---

## 2. Prerequisite: the Go toolchain

Nothing could be built until this was settled, and it took three tries.

The upstream `go.mod` requires **Go 1.25.0**; the sandbox had Go 1.23.9 from Alpine's
package repo. Attempts in order:

| # | Attempt | Result |
|---|---|---|
| 1 | `GOTOOLCHAIN=go1.25.0` | `go version go1.25.0` **segfaulted** (exit 139) under PRoot |
| 2 | rely on inherited `TMPDIR` | `go: creating work dir: stat /data/user/0/<app>/cache: no such file or directory` — the sandbox inherits a `TMPDIR` pointing at an Android app-private path that does not exist inside PRoot |
| 3 | `GOTOOLCHAIN=local` with Go 1.23.9 | dependency `html-to-markdown/v2@v2.5.2` itself requires `go >= 1.25.0` |

The fix was to install a newer toolchain manually. Mirrors were benchmarked before
downloading:

| Mirror | Probe | 5 MB timed download |
|---|---|---|
| `go.dev` | 302 → `dl.google.com` | — |
| `golang.google.cn` | 302 → `dl.google.com` | — |
| **`mirrors.aliyun.com/golang`** | **200 direct** | **0.57 s (~8.8 MB/s)** |
| Tsinghua TUNA | 404 | — |
| `goproxy.cn` | 404 | — |

Go 1.27.2 (67 MB) was pulled from Aliyun in 5.06 s and installed over `/usr/lib/go`.

> **Reusable lesson:** every `go` invocation in this environment needs
> `export TMPDIR=/tmp`. `TMPDIR ?= /tmp` in a Makefile does *not* help — make treats an
> inherited variable as already defined. The Makefile here therefore probes the value
> (`[ -d "$(TMPDIR)" ] && … || echo /tmp`) instead.

---

## 3. Gap analysis

Both repos were cloned and read in full — every engine file, plus `base.py`,
`ddgs.py`, `results.py`, `similarity.py`, `utils.py`, `http_client.py`, `cli.py`
(700 lines), `api.py` (455 lines) and `mcp.py` (246 lines), against the Go
`registry.go`, `engine*.go`, `ddgs.go`, `results.go`, `ranker.go`, `normalize.go`.

The headline finding was that **the library layer was already good**. Thirteen of
fourteen engines matched Python byte-for-byte, and the scheduler's streaming fan-out
was a genuine improvement over Python's thread-pool waves. The real gaps were
narrow and specific:

| # | Gap | Evidence |
|---|---|---|
| D1 | `yandex` still registered | Python set `disabled = True` in `f4ba66c` (v9.15.0, 2026-08-16) |
| D2 | **`google` engine stale** | Python moved to `https://www.google.com/wml/search` with a Nokia/UC WML user-agent pool in `3ea6cbc` (v9.16.0, 2026-08-26); Go still hit `/search` with an Android Chrome UA |
| D3 | `yahoo_news` source separator | Python splits on `U+00B7` + **two** spaces; Go had one space |
| M4 | `extract(fmt="text_rich")` | Silently fell back to markdown instead of failing |
| F1–F3 | CLI, API server, MCP server | Entirely absent (~1,400 lines of Python surface) |

D2 mattered most: the Go port predated the upstream change by 27 days, so it was
querying an endpoint Google had already moved off.

Findings were written to `docs/GAP-ANALYSIS.md`, the authoritative spec to
`docs/SPEC.md` (§0–§10), and the merge order to `docs/PLAN.md` **before** any code
was written.

---

## 4. Parallel execution

Work was split into five streams with **disjoint file sets**, so they could run
concurrently without merge conflicts:

| Stream | Scope | Files |
|---|---|---|
| A | Engine drift (D1–D3, M4) | `registry.go`, `engine_text.go`, `engine_misc.go`, `errors.go`, `extract.go` |
| B | CLI (F1) | `cmd/ddgs/`, `internal/output/` |
| C | API server (F2) | `internal/apiserver/`, `cmd/ddgs-api/` |
| D | MCP server (F3) | `internal/mcpserver/`, `cmd/ddgs-mcp/` |
| E | Integration & release | `Makefile`, `.github/workflows/`, `README.md`, `CHANGELOG.md`, `LICENSE`, `.gitignore`, `integration_test.go`, `docs/` |

Two pieces of shared groundwork were done **before** dispatch, precisely so the
streams would not collide:

1. Module renamed to `github.com/harrywl/ddgs-go`.
2. `internal/searchsvc.Searcher` introduced — a minimal interface (five search
   methods + `Extract`) that both servers depend on instead of `*ddgs.DDGS`
   directly, so their tests can inject a stub and run with zero network.

Each stream got its own `git worktree` + branch (`wt-a`…`wt-d`, `stream-a`…`stream-d`),
all branched from the same base commit.

### 4.1 Making two sub-agents run on two different models

This needed configuration work, and the first attempt failed for a non-obvious reason.

The single built-in sub-agent was **pinned** to a model group whose strategy is
`fallback`. A fallback group tries its entries in order, so *both* tasks would have
landed on the same first model regardless of what was requested. Worse, a pinned
sub-agent ignores `model_choice` entirely — that parameter only takes effect when the
agent's group is left on *Auto*.

The fix was to create two single-model groups and two custom sub-agents, each pinned
to one group:

| Custom sub-agent | Pinned group | Model |
|---|---|---|
| `CLI Builder` | single-model group | `anthropic/claude-haiku-5.5` (zenmux) |
| `MCP Server Builder` | single-model group | `deepseek/deepseek-v4.1-flash` (zenmux) |

Each sub-agent's `instructions` carried the environment preamble (`TMPDIR`,
`GOPROXY`, `GOTOOLCHAIN`) and the hard rule *work only in your worktree, commit, do
not push* — so a runaway agent could not touch `main` or the remote.

> **Reusable lesson:** to give sub-agents different models, pinning the agent to a
> group is not enough — the group itself must contain exactly the model you want, or
> be on Auto so `model_choice` applies. A multi-entry fallback group silently routes
> everything to its first entry.

### 4.2 Dispatch order and outcomes

| Stream | Executor | Outcome |
|---|---|---|
| A | sub-agent → **parent took over** | Sub-agent timed out at 46m30s having finished all code and passed every gate, killed by a signal mid-`git commit` leaving a stale `.lock`. Parent cleared the lock, verified all changes were on disk, and committed them. |
| C | sub-agent (default model) | Completed; every gate green; **only the commit was left undone** when its tool budget ran out. Parent committed. |
| D | sub-agent (DeepSeek) | Completed cleanly in 4m08s, 1,670 lines. |
| B | sub-agent (Haiku) → **failed** → parent wrote baseline → sub-agent (DeepSeek) finished | Haiku run died on a provider error (`Response ended incomplete`) after 13m32s with only 178 lines written. Parent wrote a working 1,645-line baseline, then re-dispatched to DeepSeek V4.1 Flash with instructions to *complete, not rewrite*. It ran 14m13s and fixed 23 divergences. |

Two of the four sub-agent runs ended without committing. Both times the code was
intact on disk — the parent's job was verification and commit, not rework. That is
the pattern worth remembering: **a sub-agent timeout is usually a bookkeeping
failure, not a work failure.** Check the worktree before re-delegating.

---

## 5. What each stream produced

### Stream A — engine drift

| Change | Detail |
|---|---|
| D1 | `"yandex"` removed from `engineRegistry["text"]`. `newYandex` is **kept** — Python likewise keeps the disabled class. `getEngines("text","yandex")` falls back to `auto` with yandex excluded. |
| D2 | `newGoogle` rewritten end to end: `/wml/search`, Nokia/UC WML UA pool, new items/elements xpaths, `sca_esv=1`, raw region split for `hl`/`lr`/`cr`, WML prolog pre-processing. The old Android-Chrome `googleUA()` was deleted outright. |
| D3 | Separator constant extracted and pinned by a byte-level test (`0xC2 0xB7` + two `0x20`). |
| M4 | `ErrUnsupportedFormat` (wrapping `ErrDDGS`) + `FormatTextRich` constant, so `text_rich` fails loudly instead of silently returning markdown. |

Two details worth recording:

- **A Python quirk was preserved deliberately.** Google's `pre_process_html` does
  `html_text[html_text.find("?>"):]`. When `find()` returns `-1`, Python slices
  `text[-1:]` — the *last character only*. The Go port reproduces this exactly rather
  than "fixing" it, because parity means matching behaviour, not intent.
- **Goldens were regenerated with real Python.** `testdata/parity_payloads.json` and
  the new `testdata/google_wml.html` expectations were produced by running the
  locally installed Python ddgs 9.16.0, not by hand. Go output then matched
  byte-for-byte.

diffstat: **15 files, +723/−46**

### Stream C — API server

FastAPI-parity HTTP server on **stdlib `net/http` only** (no framework dependency).
14 routes: `GET`+`POST` for each of the five categories, `/extract`, `/`, `/health`.

The subtle part was `max_results`, which has three distinct meanings in the Python
model (`int | None = Field(10)`): omitted → 10, `null` → falsy → unlimited, `0` →
falsy → unlimited. An `OptInt` type distinguishes key-absent from explicit-null.

Also ported: starlette's `CORSMiddleware` (concrete echoed origin because
`allow_credentials=True`), FastAPI's `{"detail": ...}` error shape, per-category
error prefixes copied verbatim, and 307 trailing-slash redirects.

30 tests, all `httptest` + stub `searchsvc.Searcher`, **zero network**.

diffstat: **7 files, +2,247**

### Stream D — MCP server

Stdio JSON-RPC 2.0 server, 6 tools, schemas transcribed from `mcp.py`'s signatures
and docstrings. Protocol details that were easy to get wrong and were got right:
notifications (no `id`) never receive a reply; parse errors reply with `"id": null`;
execution errors surface as `isError: true` rather than a JSON-RPC error; stdout
carries protocol only, logs go to stderr; EOF exits 0.

839 lines of tests.

diffstat: **5 files, +1,670**

### Stream B — CLI

Nine subcommands: `version`, `text`, `images`, `videos`, `news`, `books`, `extract`,
`mcp`, `api`. The whole `cli.py` flag tree is reproduced — repeatable `-b`, per-command
backend choice lists, category filters routed into `SearchOptions.Extra`, click-style
`Choice` validation with `(choose from …)` messages.

The DeepSeek pass found **23 divergences** from `cli.py`. The ones that mattered most:

| # | Bug | Impact |
|---|---|---|
| 1 | `ddgs api` registered `--host`/`--port`/`--reload` with an **empty short name** | `flag` panics on redefinition — the command was unusable on every invocation |
| 2 | `-v/--verify` implemented as a Go bool flag | click declares it as type `BOOL`, which *takes a value*; bare `-v` must be a usage error |
| 3 | `-b bing,duckduckgo` silently accepted | click's `multiple=True` + `Choice` validates each occurrence separately, so this is a usage error |
| 4 | Usage errors exited 1 | click's `UsageError` exits **2**; only runtime errors exit 1 |
| 16 | CSV dialect wrong | Python's `csv` excel dialect writes `\r\n` with minimal quoting; `encoding/csv` writes `\n` and quotes leading spaces. Empty result set produces an *empty file*, not a header-only one |
| 15 | JSON output escaped HTML and added a trailing newline | `json.dumps(ensure_ascii=False, indent=2)` does neither |

Two of these were cases where **the sub-agent overrode an incorrect instruction from
the parent**. The brief said "assert that empty fields are skipped"; the agent ran
Python's `_print_data` and found the opposite (Python prints them — `if v:` only
selects the *wrap* branch), implemented the Python behaviour, and left a note saying
so. That is the correct call, and it is recorded here because it is the behaviour you
want from an executor: the source of truth beats the brief.

diffstat (baseline + completion): **15 files, +3,530/−11**

### Stream E — integration & release

Done by the parent throughout, in the gaps between dispatches:

- `Makefile` with a probing `TMPDIR` (see §2), `fmt-check`/`vet`/`test`/`race`/`build`/
  `release`/`integration` targets
- CI: 2 OS × 2 Go versions; separate `integration` job, `continue-on-error` because
  live engines are inherently flaky
- Release workflow: cross-compile + `sha256sum` + GitHub release on `v*` tags
- `CHANGELOG.md`, dual-copyright `LICENSE` (retaining both upstream MIT notices),
  `.gitignore`, rewritten `README.md`, `docs/` reorganisation
- `integration_test.go`: 9 live-network tests behind `-tags=integration`

---

## 6. Verification

### Gates

| Gate | Local (PRoot) | CI (ubuntu + macos, go 1.25 & 1.27) |
|---|---|---|
| `gofmt -l .` | empty | ✅ |
| `go vet ./...` | clean | ✅ |
| `go build ./...` | ok | ✅ |
| `go test ./...` | ok | ✅ |
| `go test -race ./...` | **not runnable** — TSan: `unsupported VMA range (Found 39 - Supported 48)`, a PRoot limitation, not a code fault | ✅ all 4 matrix cells |
| `go test -tags=integration` | 9/9 pass | ✅ |

Test counts, measured not estimated: **143 top-level `Test` functions** → 134 in the
default build + 9 integration; **266 passing cases** including subtests; 0 failures.

### End-to-end smoke (real network, real binaries)

| Surface | Check | Result |
|---|---|---|
| CLI | `ddgs version` | `9.16.0-go.1` |
| CLI | `ddgs text -q … -m 3 -nc` | 3 formatted results |
| CLI | `-o json` / `-o csv` | files written, Python field order |
| CLI | `ddgs images … -d -dd <dir>` | 2 images downloaded (206 KB + 14 KB) |
| CLI | `ddgs extract -u … -f text_markdown` | markdown to stdout |
| CLI | `ddgs extract -f text_rich` | `ErrUnsupportedFormat`, **exit 1** |
| CLI | bad backend / unknown command / missing `-u` | **exit 2** each |
| CLI | `ddgs api` foreground | `/health` 200, search 200, empty query 400, CORS preflight 200 |
| CLI | `ddgs api -d` then `-s` | pidfile written → server reachable → stopped → pidfile removed → port closed |
| CLI | `ddgs mcp` | initialize → 6 tools → real `search_news` call |
| API | `ddgs-api` binary | same endpoints directly |
| MCP | `ddgs-mcp` binary | protocol-only stdout, logs on stderr, EOF exit 0 |

### Cross-validation against Python

The decisive check. Same query, same machine, same network, both implementations:

```
PY  n=3
    https://pulsar2-docs.readthedocs.io/en/latest/
    https://github.com/AXERA-TECH/pulsar2-docs
    https://github.com/AXERA-TECH/pulsar2-docs-en
GO  n=3
    https://pulsar2-docs.readthedocs.io/en/latest/
    https://github.com/AXERA-TECH/pulsar2-docs
    https://github.com/AXERA-TECH/pulsar2-docs-en
```

**Identical URLs, identical order.** The engine registry was likewise compared against
a live `from ddgs.engines import ENGINES` dump and asserted equal in both directions
(`TestRegistryMatchesPython`).

One negative result was also cross-checked rather than assumed: with `backend=google`
both implementations returned "No results found" — Google rate-limits this IP. Because
Python failed identically, it was classified as a remote condition, not a port bug.

### Release

CI and Release workflows both green on push. Published assets: **9 binaries**
(3 commands × 3 platforms) + `checksums-9.16.0-go.1.txt`.

---

## 7. Friction points

Worth recording because each one cost real time and each has a cheap workaround.

1. **Inherited `TMPDIR` points at a nonexistent Android path.** Breaks `go build`,
   `make`, and `GOTOOLCHAIN` auto-download alike. Always `export TMPDIR=/tmp`.
2. **Go 1.25 toolchain segfaults under PRoot.** Install 1.27.x manually from a mirror
   instead of relying on `GOTOOLCHAIN=auto`.
3. **Stale `.lock` files after a killed sub-agent.** `git commit` interrupted by a
   signal leaves `HEAD.lock` / `refs/heads/<branch>.lock`, and every subsequent git
   command fails with "another git process seems to be running" even though none is.
   `find .git -name '*.lock' -delete` after confirming `ps` shows no git process.
4. **`-race` cannot run under PRoot** (VMA range). Verify in CI instead.
5. **Sub-agent tool budgets run out mid-commit, not mid-edit.** Budget generously or
   expect to commit on their behalf.
6. **Piped exit codes lie.** `cmd | head` makes `$?` report `head`'s status. Redirect
   to a file and check `$?` separately when exit codes are part of the assertion.
7. **`rg -c` on a single file omits the filename prefix**, so `awk -F:` silently
   yields nothing. Per-file counts need a different idiom.
8. **Make's own startup warning about `TMPDIR` cannot be suppressed** from inside the
   Makefile — it is emitted before the file is read. It is harmless (affects only
   make's own temp dir); what matters is the value exported to child `go` processes.

---

## 8. Reusable process notes

What worked, generalised beyond this project:

- **Write the gap analysis and spec before dispatching anyone.** Sub-agents executed
  against `docs/SPEC.md` §-references; disagreements were resolved by pointing at a
  section rather than re-arguing. Two of four runs ended early and the work was still
  unambiguous to pick up.
- **Disjoint file sets make parallelism safe.** No stream touched another's files, so
  merges were trivial (three fast-forwards, no conflict resolution at all).
- **Do shared groundwork first.** The `searchsvc.Searcher` interface and the module
  rename were committed before dispatch; otherwise C and D would have both invented a
  seam and collided.
- **Verify sub-agent claims against disk.** Every report was re-checked — greps for
  the specific code it claimed to have written, byte-level comparison for the
  separator fix, gates re-run in the parent shell. All claims held, but one report
  asserted a commit that had not actually landed.
- **Let the executor overrule the brief when the source disagrees.** See §5, Stream B.
- **Pin sub-agents to single-model groups** when you want specific models, and put the
  environment preamble in the agent's standing instructions rather than every task.
- **Cross-validate against the reference implementation** with identical inputs. It
  caught nothing here — which is exactly what you want it to do — but it is the only
  check that distinguishes "parses plausibly" from "is correct".

---

## 9. Redaction

This document is published to a public repository. Removed or generalised:

- **No credentials.** No API keys, tokens, OAuth material, passwords or account
  identifiers appear. The repository was scanned before publication for key/token/
  secret/password patterns, for bearer-style strings, for cloud access-key shapes and
  for UUIDs; the only hits were search-engine protocol concepts that merely share the
  word "token" (DuckDuckGo's `vqd`, startpage's `sc` anti-bot token).
- **Model-group and sub-agent UUIDs are omitted.** §4.1 describes the groups by role
  ("single-model group") rather than by identifier.
- **Provider and model names are retained** (`zenmux`, `anthropic/claude-haiku-5.5`,
  `deepseek/deepseek-v4.1-flash`). These are public catalogue names, not credentials,
  and the model split is part of what the record documents.
- **Android app-private paths are generalised** to `/data/user/0/<app>/cache`.
- **Git author emails are not reproduced.** Note that commit history itself contains
  three author identities — the parent agent's, the sub-agents' default
  (`dev@localhost`), and the upstream fork's original author. Rewriting history to
  normalise them was considered and rejected: the upstream commit is public history
  that is not ours to alter, and a force-push would invalidate the published tag.

---

## 10. Reproducing this build

```sh
git clone https://github.com/harrywl/ddgs-go.git && cd ddgs-go

# PRoot/Android sandboxes need this; ordinary shells do not.
export TMPDIR=/tmp GOPROXY=https://goproxy.cn,direct GOTOOLCHAIN=auto

make fmt-check vet build test      # static gates
go test -tags=integration ./...    # live-network tests (needs egress)

make release VERSION=9.16.0-go.1   # dist/: 3 commands x 3 platforms
```

Or skip building and take a binary from the
[release](https://github.com/harrywl/ddgs-go/releases/tag/v9.16.0-go.1).

Companion documents: [SPEC.md](SPEC.md) (authoritative requirements),
[GAP-ANALYSIS.md](GAP-ANALYSIS.md) (drift evidence),
[PLAN.md](PLAN.md) (stream/merge ordering),
[CHANGELOG.md](../CHANGELOG.md) (what shipped).
