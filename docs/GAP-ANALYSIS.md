# GAP ANALYSIS — deedy5/ddgs (Python v9.16.0) vs ddgs-go (eda6e65)

Date: 2026-10-19 · Python HEAD: 70a5635 (2026-08-26) · Go HEAD: eda6e65 (2026-07-30)

The Go port was written against Python ddgs as of ~2026-07-30. Python has since
shipped v9.15.0 (2026-08-16) and v9.16.0 (2026-08-26). This document is the
complete, verified drift + missing-surface inventory.

## 1. Engine registry drift

Python `ENGINES` is built dynamically: a class registers only if
`disabled` is not True (default False) and it has `name` + `category`.
Current effective registry (v9.16.0):

| category | Python (enabled) | Go (registry.go) | Δ |
|---|---|---|---|
| text | brave, duckduckgo, google, grokipedia, mojeek, startpage, wikipedia, yahoo | same **+ yandex** | Go has extra `yandex` |
| images | bing, duckduckgo | bing, duckduckgo | ✓ |
| news | bing, duckduckgo, yahoo | bing, duckduckgo, yahoo | ✓ |
| videos | duckduckgo | duckduckgo | ✓ |
| books | annasarchive | annasarchive | ✓ |

- **D1 · yandex**: Python commit f4ba66c (2026-08-16) set `disabled = True` on
  Yandex. Go still registers `newYandex`. Fix: remove the registry entry (keep
  the engine code, mirroring Python which keeps the disabled class).
- **bing (text)** stays disabled in both. ✓

## 2. Engine implementation drift

### D2 · Google text engine — REWRITTEN upstream (3ea6cbc, 2026-08-26)

Python `ddgs/engines/google.py` now uses the WML (mobile legacy) endpoint.
Go still implements the old desktop-HTML engine. Every field differs:

| field | Python v9.16.0 (source of truth) | Go current |
|---|---|---|
| search_url | `https://www.google.com/wml/search` | `https://www.google.com/search` |
| User-Agent | `NokiaN72/{firmware} Series60/2.8 Profile/MIDP-2.0 Configuration/CLDC-1.1`, optionally suffixed `/UC Browser{uc}/27/351/UCWEB`. firmware ∈ {2.0617.1.0.3, 2.0625.2.0.2, 2.0635.2.0.2, 5.0706.4.0.1, 5.0819.4.0.1}; uc ∈ {7.9.1.120, 7.9.1.121, 7.9.1.122}; both choices random per process, UA fixed at class-import time | random Android Chrome UA (`googleUA()`) |
| items_xpath | `//div[./div[1]/a and ./div[2][table]]` | `//div[@data-hveid][.//h3]` |
| title xpath | `./div[a]/a/span[1]/text()` | `.//h3//text()` |
| href xpath | `./div[a]/a/@href` | `.//a[.//h3]/@href` |
| body xpath | `./div[2][table]//text()` | `./div/div[last()]//text()` |
| payload | `q`, `sca_esv=1`, `filter` (on=2/moderate=1/off=0), `start=(page-1)*10`, `hl={lang}-{COUNTRY}`, `lr=lang_{lang}`, `cr=country{COUNTRY}`, optional `tbs=qdr:{timelimit}` | same minus `sca_esv` |
| cookies | `google.com` → `CONSENT=YES+` before request | ✓ same |
| pre_process_html | `html_text[html_text.find("?>"):]` — strips the WML `<?xml …?>` prolog (quirk: if `?>` absent, Python slices `[-1:]` = last char only; mirror faithfully) | missing |
| post_extract | href starting `/url?q=` → take `?q=` param up to first `&` or `?`; keep only results with title AND href starting `http` | ✓ same |

### D3 · yahoo_news source separator — byte-level drift

Python `yahoo_news.py::extract_source`: `s.split(" ·  via Yahoo", 1)` —
U+00B7 MIDDLE DOT followed by **TWO** spaces (verified via od: `302 267 sp sp v i a`).
Go `engine_misc.go:248`: `strings.SplitN(src, " · via Yahoo", 2)` — **ONE** space.
Fix: use two spaces.

### Engines verified at parity (no action)

duckduckgo text/images/news/videos, bing images, bing news, yahoo text,
brave, mojeek, startpage (incl. `sc` token fetch), wikipedia (opensearch +
extract + "may refer to:" drop), grokipedia (typeahead, priority 1.9),
annasarchive (random TLD, comment-stripping preprocess, url prefix postprocess).
Also verified: `normalizeURL/Text/Date` (date format `+00:00` matches Python
`isoformat()`), `extractVQD`, `expandProxyTBAlias`, aggregator cache-fields
{href,image,url,embed_url} + longer-body replacement + frequency sort,
SimpleFilterRanker bucketing (wikipedia-top / both / title / body / neither,
Wikimedia-category skip), `max_results=None` → `-1` semantics,
DDGS_PROXY env fallback, `tb` proxy alias.

## 3. Missing surface (Python has, Go lacks entirely)

| id | feature | Python source | size |
|---|---|---|---|
| M1 | **CLI** `ddgs` — subcommands: version, text, images, videos, news, books, extract, mcp, api; flags incl. backend choice, output csv/json, download (-d/-dd/-th), color toggle | `ddgs/cli.py` | 700 lines |
| M2 | **API server** (FastAPI) — GET+POST `/search/{text,images,news,videos,books}`, GET `/`, `/health`; typed request/response models | `ddgs/api_server/api.py` | 455 lines |
| M3 | **MCP server** (stdio) — tools: search_text, search_images, search_news, search_videos, search_books, extract_content | `ddgs/api_server/mcp.py` | 246 lines |
| M4 | **extract fmt `text_rich`** — provided by primp (Rust). Go `extract.go` supports text_markdown/text_plain/text/content only | `http_client.py` | — |

Decision (see SPEC): M1–M3 get full Go implementations; M4 returns a clear
"unsupported in Go port" error (primp-internal format, no pure-Go equivalent).

## 4. Go-side improvements to KEEP (intentional divergence)

- Streaming fan-out scheduler (consume as engines return, cancel in-flight at
  max_results) vs Python's wave-based ThreadPoolExecutor.
- `minWorkersWithGeneral = 3` floor so a general SERP engine is always in
  flight alongside single-result encyclopedias.
- Per-engine failure cooldown (2 consecutive failures → 5 min skip in auto mode).
- Context propagation on every public method (`ctx context.Context` first arg).
- These are documented in code; do not "fix" them back to Python behavior.

## 5. HTTP layer note

Python uses `primp` with `impersonate="random"` (real browser TLS/JA3
fingerprints). Go uses stdlib net/http + random desktop UA strings — no TLS
impersonation. Pure-Go options exist (utls) but are out of scope for this
milestone; tracked as future work (F1). Consequence: fingerprint-sensitive
engines may rate-limit the Go client sooner than Python. The Go cooldown
mitigates this.

## 6. Repo/metadata gaps

- Go repo: 1 commit, 0 releases, no CI, README describes library only.
- go.mod `module github.com/dreamyang-liu/ddgs-go`, `go 1.25.0`.
- Target: module renamed to `github.com/harrywl/ddgs-go` (this fork), CI +
  release workflow, README covering CLI/API/MCP usage.
