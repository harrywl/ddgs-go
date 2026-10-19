# SPEC — ddgs-go: bring to full parity with Python ddgs v9.16.0

**Repo**: `github.com/harrywl/ddgs-go` (fork of `dreamyang-liu/ddgs-go`, work dir `repo/`)
**Upstream source of truth**: `py-ddgs/` = deedy5/ddgs @ 70a5635 (v9.16.0, 2026-08-26)
**Baseline**: `go-ddgs/` = dreamyang-liu/ddgs-go @ eda6e65
**Language**: Go 1.27.2 installed at `/usr/lib/go`; build with
`export TMPDIR=/tmp GOPROXY=https://goproxy.cn,direct GOTOOLCHAIN=auto`
**Non-negotiable rule**: for every behavior, the Python file is the spec.
Read it; mirror it exactly (including quirks, documented in comments). Do not
"improve" semantics. Intentional Go-side divergences are listed in §9 and must
be preserved.

---

## §0 Work streams & acceptance gates

| Stream | Scope | Gate |
|---|---|---|
| A | Engine drift fixes (D1–D3) | `go vet ./...` clean; `go test ./...` pass; new parity tests added |
| B | CLI (`cmd/ddgs`) | builds; every subcommand mirrors `ddgs/cli.py` flags; smoke test against live engines passes |
| C | API server (`cmd/ddgs-api` + `internal/apiserver`) | builds; endpoints match `api.py` shapes; curl smoke test passes |
| D | MCP server (`cmd/ddgs-mcp` + `internal/mcpserver`) | builds; speaks MCP stdio JSON-RPC; tool list matches `mcp.py` |
| E | Repo hygiene (module rename, README, CI, LICENSE, Makefile) | `go build ./... && go test ./...` from clean checkout; CI yaml lint-valid |

Order: A first (library correctness), then B, C, D in parallel, E last.
Each stream ends with all gates green and a commit on `main` of `repo/`.

## §1 Module layout (target)

```
go.mod                     module github.com/harrywl/ddgs-go; go 1.25.0
*.go (package ddgs)        library — keep existing flat layout
cmd/ddgs/main.go           CLI entry (stream B)
cmd/ddgs-api/main.go       API server entry (stream C)
cmd/ddgs-mcp/main.go       MCP server entry (stream D)
internal/apiserver/        FastAPI-equivalent handlers (stream C)
internal/mcpserver/        MCP stdio JSON-RPC server (stream D)
internal/output/           shared csv/json/print/download helpers for CLI (stream B)
```

Rationale for `cmd/` + `internal/`: keeps the root package importable as a
library (`go get github.com/harrywl/ddgs-go`) while binaries live under cmd.
`internal/` prevents external import of server plumbing.

Allowed new deps (add to go.mod only as needed):
- CLI: `github.com/spf13/cobra` OR stdlib `flag` — prefer **stdlib flag** to
  minimize deps; if flag proves too clunky for nested subcommands, cobra is
  allowed. Decision recorded in commit message.
- API server: stdlib `net/http` (Go 1.22+ routing with method+pattern) — no
  framework dep. JSON via `encoding/json`.
- MCP server: stdlib only (JSON-RPC 2.0 over stdio is simple).
- No other third-party deps without justification in the commit message.

## §2 Stream A — engine drift fixes

### A1 (D1) Unregister yandex
- File: `registry.go`. Delete the `"yandex": newYandex,` line from the `text`
  map. Keep `engine_text.go::newYandex` (Python keeps the disabled class too).
- Add comment above registry text map: mirrors Python ENGINES; yandex disabled
  upstream since f4ba66c (2026-08-16).
- Update `parity_engine_test.go` / any test that enumerates text backends.
- New test `TestRegistryMatchesPython`: assert registry keys exactly equal
  {text: brave,duckduckgo,google,grokipedia,mojeek,startpage,wikipedia,yahoo;
  images: bing,duckduckgo; news: bing,duckduckgo,yahoo; videos: duckduckgo;
  books: annasarchive}.

### A2 (D2) Rewrite Google engine to WML endpoint
File: `engine_text.go::newGoogle`. Source of truth: `py-ddgs/ddgs/engines/google.py`.

1. `searchURL`: `https://www.google.com/wml/search`.
2. Replace `googleUA()` with a Nokia/UC WML UA generator mirroring Python
   `get_ua()`:
   ```
   firmware = random choice of {2.0617.1.0.3, 2.0625.2.0.2, 2.0635.2.0.2,
                                5.0706.4.0.1, 5.0819.4.0.1}
   ua = "NokiaN72/" + firmware +
        " Series60/2.8 Profile/MIDP-2.0 Configuration/CLDC-1.1"
   with p=0.5 append "/UC Browser" + random{7.9.1.120, 7.9.1.121, 7.9.1.122} +
                     "/27/351/UCWEB"
   ```
   Python fixes the UA once at class-import time (`headers_update` evaluated at
   class definition). In Go, generate once inside `newGoogle` (per engine
   instance) — equivalent because Python DDGS caches engine instances too.
   Use crypto/rand via existing `randInt`. Keep the old `googleUA` function
   deleted (no dead code).
3. `itemsXPath`: `//div[./div[1]/a and ./div[2][table]]`
4. `elementsXPath`: title `./div[a]/a/span[1]/text()`, href `./div[a]/a/@href`,
   body `./div[2][table]//text()`
5. `buildPayload`: keep existing CONSENT cookie set; ADD `sca_esv: "1"` to
   payload. Everything else already matches (verify char-by-char against
   Python: q, filter map on=2/moderate=1/off=0 with lowercase key lookup,
   start=(page-1)*10, hl=`{lang}-{COUNTRY}`, lr=`lang_{lang}`,
   cr=`country{COUNTRY}`, tbs=`qdr:{timelimit}` when timelimit != "").
   Note Python splits region as `country, lang = region.split("-")` WITHOUT
   lowercasing first — Go `splitRegion` lowercases. For Google the hl/lr/cr
   construction uppercases country and uses lang as-is; Python preserves the
   original case of lang from the region string. Mirror: use raw region split
   (no ToLower) for Google's hl/lr/cr to be byte-exact. (splitRegion stays for
   other engines that already pass.)
6. `preProcessHTML`: mirror `html_text[html_text.find("?>"):]` exactly:
   ```go
   e.preProcessHTML = func(text string) string {
       if i := strings.Index(text, "?>"); i >= 0 {
           return text[i:]
       }
       // Python quirk: find() == -1 → slice [-1:] → last character only
       if len(text) > 0 {
           return text[len(text)-1:]
       }
       return text
   }
   ```
   (Document the quirk in a comment.)
7. `postExtract`: unchanged (already matches: `/url?q=` rewrite via split on
   `?q=` then `&` then `?`; keep title && http-prefix).
8. Tests: extend `parity_engine_test.go` with a WML fixture. Save a trimmed
   sample of the WML response shape under `testdata/google_wml.html` (construct
   by hand to match the xpath structure — two nested divs: `div/div[1]/a/span`
   title, `div/div[2]/table` body). Assert: payload keys (incl. sca_esv),
   preProcessHTML strips prolog, postExtract rewrites `/url?q=`, UA matches
   Nokia regex `^NokiaN72/(2\.0617\.1\.0\.3|2\.0625\.2\.0\.2|2\.0635\.2\.0\.2|5\.0706\.4\.0\.1|5\.0819\.4\.0\.1) Series60/2\.8 Profile/MIDP-2\.0 Configuration/CLDC-1\.1(/UC Browser7\.9\.1\.(120|121|122)/27/351/UCWEB)?$`.

### A3 (D3) yahoo_news source separator
File: `engine_misc.go:248`. Change `" · via Yahoo"` → `" ·  via Yahoo"`
(U+00B7 + TWO spaces; Python verified via od: `302 267 040 040`).
Add test: source `"BBC ·  via Yahoo News"` → `"BBC"`.

### A4 text_rich extract format (M4)
File: `extract.go`. Python supports fmt="text_rich" via primp. Go cannot
reproduce primp's rich-text output. Add exported sentinel
`ErrUnsupportedFormat` (wraps ErrDDGS) and return it from `Extract` when
`format == "text_rich"` (add the constant `FormatTextRich ExtractFormat =
"text_rich"` so callers get a typed error instead of silent markdown
fallback). Document in Extract doc comment: text_rich requires primp; the Go
port returns ErrUnsupportedFormat.

### A5 Regression sweep
After A1–A4: `go vet ./...`, `gofmt -l .` (must be empty), `go test ./...`
(all green). Then a live smoke matrix (network permitting):
text/images/news/videos/books × max_results=3 each; record pass/fail per
category in the commit message. Failures caused by remote engines (403/empty)
are acceptable if the same backend also fails from Python at the same moment —
verify with the installed Python ddgs (`python3 -c "from ddgs import DDGS; ..."`)
and note it.

## §3 Stream B — CLI

Source of truth: `py-ddgs/ddgs/cli.py` (700 lines). Read it fully first.

Binary: `cmd/ddgs`. Name the binary `ddgs` (Python entry point is `ddgs`).

### B1 Command tree (mirror click structure)

```
ddgs version
ddgs text    -q --query (also positional fallback? Python: -q required-ish,
               cli errors "query is mandatory" from library; keep -q/--query
               and -k/--keywords deprecated alias)
             -r/--region (default us-en) -s/--safesearch {on,moderate,off}
             -t/--timelimit {d,w,m,y} -m/--max_results int=10 -p/--page int=1
             -b/--backend (repeatable → CSV; choices: auto,all + every
               registered backend name per category)
             -o/--output (csv | json | filename.csv | filename.json)
             -d/--download (flag) -dd/--download-directory -th/--threads int=10
             -pr/--proxy -v/--verify bool=True -nc/--no-color (flag)
ddgs images  same + -size/--size {Small,Medium,Large,Wallpaper}
             -color/--color {color,monochrome} (check Python's exact choices
             in cli.py lines 283-304: it has a color choice list — mirror it)
             -type/--type_image {photo,clipart,gif,transparent,line}
             -l/--layout {Square,Tall,Wide}
             -lic/--license_image (mirror Python's choices)
             (-d/-dd/-th present for images too)
ddgs videos  same base + -res/--resolution {high,standart}
             -d/--duration {short,medium,long}
             -lic/--license_videos {creativeCommon,youtube}
             (no download flags for videos — verify against cli.py)
ddgs news    base flags only (no extra filters) — verify cli.py 437-489
ddgs books   base minus region/safesearch/timelimit — verify cli.py 490-532
             (books has only -q/-k/-m/-p/-b/-o/-pr/-v/-nc)
ddgs extract <url> positional + -fmt/--fmt {text_markdown,text_plain,text,text_rich,content}
             (mirror cli.py 546-564; text_rich → print the
             ErrUnsupportedFormat message to stderr, exit 1)
ddgs mcp     -pr/--proxy → exec the MCP stdio server in-process (call
             internal/mcpserver.RunStdio) — mirrors Python's `ddgs mcp`
ddgs api     -d/--detach -s/--stop --host (default 0.0.0.0) --port (4479)
             --reload (accept + warn "not supported in Go port" OR implement
             via fsnotify — decision: accept flag, print warning, no-op)
             -pr/--proxy → runs internal/apiserver in-process; detach/stop
             via PID file at ~/.ddgs/api.pid (mirror Python's pid-file
             mechanism in cli.py 606+)
```

**IMPORTANT**: the subagent MUST open `py-ddgs/ddgs/cli.py` and transcribe the
exact option lists per command (including choices and defaults) — the sketch
above is a map, not the spec. Where Python and this sketch disagree, Python wins.

### B2 Shared behavior (mirror cli.py helpers)

- `_sanitize_query` (cli.py:101) — mirror exactly.
- `_print_data` (cli.py:74) — colored table-ish print; -nc disables color.
  Mirror the layout (keys printed per result). Use ANSI codes directly.
- `_save_data`/`_save_json`/`_save_csv` (cli.py:51-72) — `-o csv`/`json` →
  auto filename `{sanitized_query}_{function}.{ext}`; `-o name.csv`/`name.json`
  → explicit file. CSV uses the union of result keys as header (mirror Python
  csv.DictWriter behavior: fields = keys of first row + any extras; simplest
  faithful approach: ordered per-category fields from `resultFields`).
- `_download_results`/`_download_file` (cli.py:114-160) — for text/news/books:
  no downloads; images: download `image` field; videos: download `content`
  (check cli.py for exactly which field per category and the filename scheme
  — mirror). -th threads → bounded goroutine pool. -dd directory (default:
  check cli.py). Proxy passed through to the downloader HTTP client.
- Backend flag: repeatable string slice joined with "," (Python
  `_convert_tuple_to_csv`). Validate against registry keys per category;
  invalid → warning to stderr, continue with valid ones (mirror
  `_get_engines` invalid_keys warning).
- Exit codes: 0 success; 1 on any DDGS error (print `{type}: {message}` like
  Python's safe_entry_point).
- `-v/--verify`: Python takes a bool (click converts "False"/"false"/"0"…).
  Mirror: accept bool-ish string; false → Verify{SkipVerify:true}.
- `-pr/--proxy`: expand `tb` alias (library already does this — pass through).
- Global: `DDGS_PROXY` env respected by library already.
- `version` command: print a version constant. Define `const Version =
  "9.16.0-go.1"` in the root package (Python __version__ is 9.16.0; suffix
  marks the Go port revision). CLI prints it; also expose
  `func LibraryVersion() string`.

### B3 Tests
- `cmd/ddgs` unit tests: flag parsing per command (table-driven), output
  writers (csv/json to temp files), sanitize-query parity cases copied from
  Python behavior.
- No live-network tests in CI (network flaky); gate them behind
  `-tags=integration`.

## §4 Stream C — API server

Source of truth: `py-ddgs/ddgs/api_server/api.py` (455 lines). Read fully.

Package `internal/apiserver`; binary `cmd/ddgs-api`. Stdlib net/http with Go
1.22 pattern routing (`mux.HandleFunc("POST /search/text", …)`).

### C1 Endpoints (mirror api.py exactly)

```
GET  /            → HealthResponse
GET  /health      → HealthResponse
POST /search/text     body: TextSearchRequest     → SearchResponse
GET  /search/text     query params variant        → SearchResponse
POST /search/images   body: ImagesSearchRequest   → SearchResponse
GET  /search/images                               → SearchResponse
POST /search/news     body: NewsSearchRequest     → SearchResponse
GET  /search/news                                 → SearchResponse
POST /search/videos   body: VideosSearchRequest   → SearchResponse
GET  /search/videos                               → SearchResponse
POST /search/books    body: BooksSearchRequest    → SearchResponse
GET  /search/books                                → SearchResponse
```

- Request/response JSON field names: copy from api.py Pydantic models
  (snake_case). Every SearchOptions field maps 1:1 (query, region, safesearch,
  timelimit, max_results, page, backend, plus per-category filters: images
  size/color/type_image/layout/license_image; videos resolution/duration/
  license_videos). Read api.py to get the EXACT field names and defaults —
  do not guess.
- HealthResponse: mirror api.py (version, status fields — read it).
- Errors: Python raises HTTPException(400/429/500…). Mirror: 400 for bad
  request (empty query), 429 for RatelimitException, 500 otherwise; JSON body
  `{"detail": "..."}` (FastAPI shape) so existing clients keep working.
- One shared *ddgs.DDGS instance behind the mux (mirrors `_get_ddgs()`
  singleton), constructed from DDGS_PROXY env / -pr flag of `ddgs api`.
- Timeouts: per-request context with the DDGS timeout (library handles engine
  timeouts; add an overall request timeout = library timeout + 5s slack).
- CORS: mirror api.py if it adds CORSMiddleware (read it; if yes, replicate
  headers).

### C2 Tests
- `httptest` based: every endpoint, GET+POST, happy path with an injected fake
  engine (the library already supports engine-factory injection? NO — it does
  not. Instead: test handler JSON shaping with a stub search func by making
  the handler take an interface `searcher` — design handlers as
  `func(s searcher) http.HandlerFunc` so tests inject a stub. Keep the
  production wiring in `apiserver.New(d *ddgs.DDGS)`.)
- Integration test (tag-gated): boot server on 127.0.0.1:0, curl-equivalent
  via net/http, assert 200 + JSON shape.

## §5 Stream D — MCP server

Source of truth: `py-ddgs/ddgs/api_server/mcp.py` (246 lines). Read fully.

Package `internal/mcpserver`; binary `cmd/ddgs-mcp`. Stdlib only.
Protocol: JSON-RPC 2.0 over stdio, one message per line (newline-delimited
JSON) — this is what the Python `mcp` library's stdio transport does and what
MCP clients (Claude Desktop, Minis) speak.

### D1 Methods

- `initialize` → respond with protocolVersion (use "2024-11-05" unless mcp.py
  pins another — read it), capabilities {tools:{}}, serverInfo
  {name:"ddgs-go", version: LibraryVersion()}.
- `notifications/initialized` → no response (notification).
- `tools/list` → the 6 tools with JSON schemas mirroring the FastMCP tool
  signatures in mcp.py: search_text, search_images, search_news,
  search_videos, search_books, extract_content. Input schemas: derive from
  each tool's Python signature (param names, types, defaults, docstrings as
  descriptions). extract_content: {url: string (required), fmt: string
  (default "text_markdown")}.
- `tools/call` → dispatch by name; run the search via the shared *ddgs.DDGS;
  return MCP tool result: {content:[{type:"text", text: <JSON-encoded
  results>}], isError:false}. Mirror mcp.py's return shape exactly (read what
  FastMCP serializes — if the Python tools return dicts, FastMCP wraps them;
  replicate the wrapped shape).
- Unknown method → JSON-RPC error -32601. Parse error → -32700.
- Stdio loop: read lines from stdin, dispatch, write responses to stdout
  (flush per message). Logs → stderr (never stdout — it's the transport).

### D2 Tests
- Drive the server over an io.Pipe: send initialize → assert response;
  tools/list → assert 6 tools + schema field names; tools/call search_text
  with injected stub searcher (same `searcher` interface trick as stream C —
  factor it into `internal/apiserver` or a small shared `internal/searchsvc`
  package; prefer shared) → assert content shape.

## §6 Stream E — repo hygiene

- E1 `go.mod`: module → `github.com/harrywl/ddgs-go`. Update all internal
  import paths (root package imports none of itself; cmd/internal do).
  `go mod tidy` after.
- E2 LICENSE: keep MIT; update copyright line to
  `Copyright (c) 2026 harrywl` + retain original attribution
  `Copyright (c) 2026 dreamyang-liu` (derivative work — keep both).
- E3 README.md: rewrite. Sections: what it is (Go port of deedy5/ddgs, parity
  with v9.16.0), install (`go install github.com/harrywl/ddgs-go/cmd/ddgs@latest`
  + `go get` for library), quickstart (library snippet, CLI examples per
  command, API server curl examples, MCP client config JSON), backend table
  (categories × engines), parity notes (streaming scheduler, cooldown,
  text_rich unsupported, no TLS impersonation), credits (upstream links).
- E4 Makefile: `build` (all three binaries to ./bin), `test`, `vet`, `fmt`,
  `integration` (tagged tests), `release` (goreleaser-ready cross build list:
  linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64).
- E5 CI `.github/workflows/ci.yml`: on push+PR: gofmt check, go vet, go test
  (matrix: go 1.25.x + 1.27.x; os: ubuntu-latest, macos-latest,
  windows-latest). Release workflow `.github/workflows/release.yml`: on tag
  v*: build cross binaries + attach to GitHub release (use actions/go or
  goreleaser — prefer plain `golang.org/x/exp`? NO: use
  `actions/upload-release-asset`-free approach: softprops/action-gh-release).
- E6 CHANGELOG.md: 9.16.0-go.1 entry summarizing parity work (D1–D3, M1–M4).
- E7 Remove GAP-ANALYSIS.md/SPEC.md/PLAN.md from the repo root before final
  commit? NO — keep them under `docs/` (move them there in E7); they document
  the port for future maintainers.
- E8 Final gate: fresh-clone simulation — `git clone repo/ /tmp/verify && cd
  /tmp/verify && go build ./... && go vet ./... && go test ./...` all green.

## §7 Cross-cutting rules (ALL streams)

1. **Read the Python file before writing the Go file.** Every handler/engine/
   helper has a `# mirrors ddgs/...` comment naming the Python source.
2. gofmt clean; go vet clean; no new lint-visible dead code.
3. Error wrapping: all library errors wrap ErrDDGS (existing convention).
4. No network access in unit tests; live tests behind `-tags=integration`.
5. Concurrency: DDGS is already safe for concurrent use — servers share one
   instance; do not add per-request DDGS construction (Python singleton
   `_get_ddgs` is the model).
6. Comments in English (matches existing codebase).
7. Commit per stream: message format `feat(cli): …`, `fix(engines): google wml
   endpoint + yandex unregister (parity v9.16.0)`, etc. Conventional commits.
8. Do NOT touch the intentional divergences in §9.

## §8 Verification matrix (run at the end, record results in CHANGELOG)

| check | command | expected |
|---|---|---|
| fmt | `gofmt -l .` | empty |
| vet | `go vet ./...` | clean |
| unit | `go test ./...` | ok |
| race | `go test -race ./...` | ok |
| build | `go build ./...` | 3 binaries compile |
| cli smoke | `./bin/ddgs text -q "golang" -m 3` | ≥1 result printed |
| cli json | `./bin/ddgs text -q "golang" -m 3 -o json` | file written, valid JSON |
| api smoke | boot + `curl :4479/health` + POST /search/text | 200s, JSON shapes match api.py |
| mcp smoke | pipe initialize + tools/list into ./bin/ddgs-mcp | valid JSON-RPC responses, 6 tools |
| py cross-check | same query via `python3 -c "from ddgs import DDGS…"` | both return results (content may differ by engine luck) |

## §9 Intentional Go divergences (KEEP — do not "fix")

1. Streaming fan-out scheduler (vs Python wave-based executor).
2. `minWorkersWithGeneral=3` floor.
3. Engine failure cooldown (2 fails → 5 min, auto mode only).
4. `ctx context.Context` first parameter on all public methods.
5. `MaxResults: -1` for Python `max_results=None`.
6. net/http + browser UA pool instead of primp TLS impersonation (future work
   F1: evaluate `utls`).
7. `text_rich` → ErrUnsupportedFormat (instead of primp rich text).
8. `ddgs api --reload` accepted but no-op with warning.

## §10 Out of scope (record as future work in CHANGELOG)

- F1 TLS fingerprint impersonation (utls / bertyazur).
- F2 `ddgs api --reload` real implementation (fsnotify).
- F3 goroutine-based download manager parity beyond -th bounding.
- F4 Packaging: Homebrew formula, AUR, `go install` vanity imports.
