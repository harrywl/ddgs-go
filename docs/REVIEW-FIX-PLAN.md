# Code Review — Findings and Fix Plan

**Status:** proposed · **Reviewed at:** `ee6a021` (2026-10-19) · **Baseline:** Python ddgs v9.16.0

This document records the findings of a full-repository review of the Go port and
turns every actionable finding into a scoped fix. Findings are grouped into three
batches so each batch can ship as an independent PR. Every item names the exact
file and line, the concrete change, its acceptance test, and whether it changes
public behaviour.

Nothing here is a rewrite. The review found the codebase to be well above typical
port quality: no panics across 14 engines × 3000 fuzzed inputs, XPath extraction
byte-identical to lxml on the shipped fixtures, 93.1% statement coverage in the
API layer, and a clean `-race` run across the CI matrix. The fixes below are
targeted, not structural.

## Summary

This document defines **21 fix items** (F1–F21); F21 bundles the small items.
They cover the 32 findings recorded in the review report.

| Severity | Count | Meaning |
|---|---|---|
| blocker | 0 | — |
| major | 11 | security exposure, wrong output, or a broken promise of parity |
| minor | 9 | edge cases, robustness, undocumented behaviour differences |
| nit | 1 bundle | dead code, redundancy, readability (17 sub-items) |
| question | 2 | needs a maintainer decision |

Verification legend used in the appendix: **V** = reproduced locally during this
review (code path read and, where noted, exercised); **R** = reported by the
side-by-side engine/CLI comparison against real Python ddgs 9.16.0.

---

# Batch 1 — Security defaults (ship first, no API breaks)

## F1. `/extract` accepts any target address (SSRF)

**Where:** `extract.go:66-104`; callers `internal/apiserver/server.go:357-406`,
`internal/mcpserver/mcpserver.go:334-375`

**Problem.** `Extract` passes the caller-supplied URL straight to `net/http`.
Reproduced by running `ddgs-api` locally and asking it to fetch a loopback-only
probe page:

```
POST /extract {"url":"http://127.0.0.1:18765/probe_secret.html","fmt":"text"}
→ {"url":"http://127.0.0.1:18765/probe_secret.html","content":"SECRET-INTERNAL-PAGE-OK"}
```

The same capability is exposed as the `extract_content` MCP tool. On a host with
a metadata service or an admin port, this is a straightforward pivot.

**Change.**

1. Add `Config.AllowPrivateNetworks bool` (default `false`; zero value keeps the
   current behaviour only when explicitly opted in).
2. Add an unexported `urlPolicy` used by `Extract`:
   - scheme allowlist `{http, https}`;
   - resolve the host and reject `IsLoopback`, `IsPrivate`, `IsLinkLocalUnicast`,
     `IsLinkLocalMulticast`, `IsUnspecified`, `IsMulticast`;
   - **enforce at dial time**, not only before the request: install
     `Transport.DialContext` that validates the resolved `net.IP` before
     connecting. A pre-flight DNS check alone is bypassable by rebinding.
3. Add `CheckRedirect` that re-validates every hop and caps redirects at 5.
4. When blocked, return a typed `ErrBlockedTarget` wrapping `ErrDDGS` so the API
   can map it to 400 and MCP can return `isError`.

**Acceptance.** New tests in `extract_test.go`:
`TestExtractRejectsLoopback`, `TestExtractRejectsPrivate`,
`TestExtractRejectsFileScheme`, `TestExtractRejectsRedirectToLoopback`,
`TestExtractAllowsPrivateWhenOptedIn`. The first four must fail today; the last
must pass only with `AllowPrivateNetworks: true`.

**Compatibility.** Python `ddgs.extract` has no such guard, so this is a new
documented divergence (add to SPEC §9). Default-safe is the right call for a
library that also ships a server binary; the opt-in preserves the Python
behaviour for callers who need it.

## F2. `ddgs-api` binds `0.0.0.0` by default with no authentication

**Where:** `cmd/ddgs-api/main.go:36`; `cmd/ddgs/commands.go:168`

**Problem.** The default host is `0.0.0.0`, the server has no auth of any kind
(`rg -i 'authorization|bearer|api[_-]?key' internal/apiserver cmd/ddgs-api`
returns nothing), and CORS reflects any `Origin` with
`Access-Control-Allow-Credentials: true` (`internal/apiserver/cors.go:44-52`).
Combined with F1 this is LAN-reachable SSRF; combined with F3 it is a remote
memory-pressure vector.

**Change.**

1. Default host becomes `127.0.0.1` in both binaries and both flag sets.
2. Add `--allow-remote` (or keep `--host 0.0.0.0` as the explicit opt-in) and
   print a one-line warning to stderr when a non-loopback host is used.
3. README: add a "Exposing the API" paragraph stating there is no auth and that
   a reverse proxy must provide it.
4. Do **not** change the CORS behaviour in this batch — it is a faithful copy of
   `api.py` and changing it would break browser clients. Revisit only together
   with an auth story.

**Acceptance.** `cmd/ddgs/main_test.go` already pins the API defaults; update
the expectation to `127.0.0.1` and add a case for the explicit override.

## F3. Response bodies have no size cap

**Where:** `httpclient.go:190` (`io.ReadAll`), `internal/output/download.go:203`
(`io.Copy`), `internal/mcpserver/mcpserver.go:120` (`ReadBytes('\n')`)

**Problem.** `http.Client.Timeout` bounds time, not bytes. A fast origin can
stream hundreds of MB inside the 5 s budget. `downloadFile` streams to disk with
no limit at all. The MCP stdio reader accumulates an unbounded single line.

**Change.**

1. `httpClient.request`: `io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))`
   with `maxResponseBytes = 8 << 20`; return `ErrResponseTooLarge` when the
   limit is hit (detect by reading one extra byte).
2. `downloadFile`: wrap the body in a counting reader; abort at
   `maxDownloadBytes` (suggest 256 MiB) and remove the partial file.
3. `RunStdio`: replace `bufio.Reader.ReadBytes` with a `bufio.Scanner` configured
   with `Buffer(make([]byte, 64<<10), 4<<20)`; on overflow return JSON-RPC
   `-32600`.
4. Make the API body limit report `413 Payload Too Large` instead of a truncated
   JSON parse error (`internal/apiserver/server.go:298,358`): read
   `maxBodyBytes+1` and check the length.

**Acceptance.** Table tests with `httptest` serving 8 MiB + 1 byte, asserting the
typed error; an MCP test feeding a 5 MiB line.

---

# Batch 2 — Correctness

## F4. `api -s` signals an arbitrary PID from the pid file

**Where:** `cmd/ddgs/commands.go:316-324`

**Problem.** The file is parsed with `Atoi` and passed straight to
`os.FindProcess` + `SIGTERM`. There is no `pid > 1` guard and no identity check,
so a corrupt file containing `-1` is `kill(-1, SIGTERM)` — every process the user
can signal. A stale file whose PID has been reused kills an unrelated process.

**Change.**

1. Reject `pid <= 1` and non-positive values with a clear error; delete the file.
2. Store a JSON record instead of a bare integer:
   `{"pid": 1234, "start": "<process start token>"}`. On Linux read
   `/proc/<pid>/stat` field 22 (starttime); on macOS use
   `ps -p <pid> -o lstart=`. Refuse to signal when the token differs.
3. If the identity check is unavailable, fall back to verifying
   `/proc/<pid>/cmdline` contains the current binary name before signalling.

**Acceptance.** `TestStopRejectsInvalidPID` (`-1`, `0`, `1`, `"abc"`),
`TestStopRejectsReusedPID` (write a pid file for a live unrelated process and
assert it is not signalled).

## F5. `api -d` reports success even when the child died immediately

**Where:** `cmd/ddgs/proc.go:21-23`; `cmd/ddgs/commands.go:283-293`

**Problem.** `childGone` is `kill(pid, 0) == ESRCH`. A zombie still exists in the
process table, so an early-exiting child (port already in use) is reported as
alive; the CLI prints "started" and overwrites the pid file, losing the PID of the
server that is actually listening. `-s` can then no longer stop it.

**Change.** Replace liveness polling with an explicit readiness handshake:

1. Parent creates `os.Pipe()`, passes the write end via `cmd.ExtraFiles`, and
   tells the child through an env var (`DDGS_API_READY_FD=3`).
2. The child writes `ok\n` **after** `net.Listen` succeeds, then closes the fd.
3. Parent reads with a 2 s deadline. EOF without `ok` (or a timeout) means the
   child failed; reap it with `cmd.Wait()` and return an error without touching
   the pid file.
4. On success call `cmd.Process.Release()` and only then write the pid file.

This also fixes the zombie leak: the parent never leaves an unreaped child on the
failure path.

**Acceptance.** `TestDetachFailsWhenPortBusy` (occupy the port, run `api -d`,
assert non-zero exit, no pid file written, original server still running) and
`TestDetachWritesPIDAfterReady`.

## F6. Bing Images sends `filterui:age-lt0` for every timelimit

**Where:** `engine_misc.go:45-48`

**Problem.** The lookup table is keyed `day/week/month/year`, but
`SearchOptions.TimeLimit` carries `d/w/m/y`, so the lookup returns 0:

```go
minutes := map[string]int{"day": 1440, "week": 10080, "month": 44640, "year": 525600}[p.TimeLimit]
payload["qft"] = fmt.Sprintf("filterui:age-lt%d", minutes) // d → age-lt0
```

Upstream `bing_images.py` uses the same key set, so Python raises `KeyError` and
the engine fails loudly. The Go port silently issues a semantically wrong filter.
`parity_engine_test.go:81-83` hides it by skipping every case where Python raised.

**Change.** Mirror upstream: return
`fmt.Errorf("%w: bing images timelimit %q unsupported", ErrDDGS, p.TimeLimit)`
when the key is absent. Do not invent a mapping — that would silently diverge.
Add a comment noting this is an upstream bug (the fix belongs in ddgs, and this
port should follow once it lands).

**Acceptance.** Golden case with `timelimit=w`: both implementations must now
return an error. Un-skip error cases in `parity_engine_test.go` (see F15).

## F7. `images.height` / `images.width` are `float64`, and print as `100.0`

**Where:** `engine_duckduckgo.go:70-84`; `internal/output/pyformat.go:120-127`;
test `engine_test.go:76-79` pins the wrong type

**Problem.** `json.Unmarshal` into `map[string]any` yields `float64` for every
number. Python's `json.loads` yields `int`. `pyString(float64(100))` calls
`pyFloat`, which appends `.0`, so `ddgs images -o csv` writes `100.0` where Python
writes `100`. Downstream code asserting `r["height"].(int)` panics.

**Change.**

1. In `ddgJSONExtract`, decode with `json.Decoder.UseNumber()` and convert
   `json.Number` to `int64` when it is integral, `float64` otherwise. Keep the
   `Result` value shape identical to Python.
2. Update `engine_test.go` to assert `int64` (or `int`) rather than `float64`.
3. Make `parity_engine_test.go`'s comparison type-sensitive
   (`reflect.DeepEqual` for scalars) so this class of drift fails the golden.
4. Leave `pyFloat` as is for genuine floats; it correctly mirrors `str(float)`.

**Acceptance.** A new golden case for DDG images asserting
`height == int64(100)`; a CLI test asserting the CSV cell is `100`, not `100.0`.

## F8. `wrapText` drops the separating space when breaking a long token

**Where:** `internal/output/print.go:239`

**Problem.** The long-word branch reserves a column for the space
(`if cur != "" { space-- }`) but then appends `runes[:space]` directly:
`see https://…` becomes `seehttps://…`, and the line ends one column short.
Long URLs and space-free CJK bodies hit this on every result.

**Change.** When `cur != ""`, append the separating space before the slice and
compute the slice length from the remaining width:

```go
if cur != "" {
    cur += " "
}
avail := limit - utf8.RuneCountInString(cur)
cur += string(runes[:avail])
```

**Acceptance.** Table test with a 400-character URL and with a long CJK string,
asserting every space is preserved and no line exceeds the width. Add the
`textwrap` reference output from Python for the same inputs.

## F9. `/dev/null` is treated as a TTY, which makes the CLI hang

**Where:** `internal/output/style.go:139-148`; `internal/output/print.go:59-66`

**Problem.** `fi.Mode()&os.ModeCharDevice != 0` is true for `/dev/null`,
`/dev/zero` and serial devices. `ddgs text -q x > /dev/null` therefore enables
ANSI colour and calls `pause()`, which reads stdin waiting for Enter. The prompt
went to `/dev/null`, so the process looks wedged.

**Change.** Use `golang.org/x/term.IsTerminal(int(f.Fd()))` (already in the
`golang.org/x` family; ~200 lines, no cgo). Keep the `nil` guard. Update the
comment that currently explains why `click._compat.isatty` is not reproduced.

**Acceptance.** `TestIsTTYFalseForDevNull` and a CLI test running with
stdout redirected to `/dev/null` and stdin a pipe, asserting it returns without
reading stdin.

## F10. CSV writes `None` for null values

**Where:** `internal/output/save.go:196`

**Problem.** `pyString(nil)` returns `"None"`, but Python's `csv.DictWriter`
writes an empty cell for `None`. Only CSV differs; terminal output correctly
prints `None` for parity.

**Change.** In the CSV row builder, write `""` when the value is `nil`.

**Acceptance.** Golden CSV with a result carrying `date: nil`; compare against
Python's `csv` output byte-for-byte.

## F11. `GET /search/books?max_results=0` returns 10, POST returns everything

**Where:** `internal/apiserver/server.go:231-247` vs `internal/apiserver/models.go:90-99`

**Problem.** The GET decoder assigns `MaxResults: maxResults` directly, so 0
reaches the library and means "default 10". The POST decoder goes through
`OptInt.resolve()`, which maps 0/None to `-1` (unlimited). Python treats 0 as
falsy in both paths and returns everything.

**Change.** Wrap the GET value: `OptInt{Present: true, Value: maxResults}`.

**Acceptance.** `TestBooksGetZeroMaxResultsUnlimited` asserting
`SearchOptions.MaxResults == -1` for both verbs.

## F12. Failure cooldown applies to an explicit backend list

**Where:** `ddgs.go:334`

**Problem.** `isAuto := backend == "auto" || backend == "all" || strings.Contains(backend, ",")`
but `getEngines` treats a comma list as an explicit selection, and SPEC §9.3 says
the cooldown is "auto mode only". `backend=duckduckgo,brave` therefore skips
cooled-down engines, contradicting both the spec and the comment above
`failureCooldown` ("direct backend selection bypasses it").

**Change.** Drop the `strings.Contains(backend, ",")` clause; keep a single
helper used by both `search` and `getEngines`.

**Acceptance.** `TestCooldownNotAppliedToExplicitList` — put an engine on
cooldown, search with `backend=name1,name2`, assert it is still called.

## F13. Empty results are counted as engine failures

**Where:** `ddgs.go:352`

**Problem.** `ok := r.err == nil && len(r.results) > 0`. Two consecutive rare
queries that legitimately return nothing put the engine on a 5-minute cooldown.
The `len(healthy) > 0` guard prevents a total blackout, but recall drops.
The comment says this is intended to detect "200 + challenge page"; that signal
should come from the HTTP layer, not from a legitimate empty result set.

**Change.** After F14 lands, count only errors (including `ErrHTTPStatus`) for
cooldown; an empty but successful response records neither success nor failure.
If challenge detection is still wanted, key it on a typed
`ErrChallengePage` returned by the parser.

**Acceptance.** `TestEmptyResultsDoNotCoolDown` — two empty searches followed by
a search that must still call the engine.

---

# Batch 3 — Observability, limits, and test integrity

## F14. Non-200 responses and parse failures are completely silent

**Where:** `engine.go:92-98`, `engine.go:168-170`; no logger anywhere in the root package

**Problem.** Any non-200 becomes an empty string with a nil error, so a 403, a
429 and a genuine zero-result query are indistinguishable. The caller only sees
`ErrNoResults`, and the cooldown logic misclassifies the cause. Python at least
logs `logger.info("Error in engine %s: %r", ...)`.

**Change.**

1. Add `ErrHTTPStatus` carrying `StatusCode` and `URL`; return it from
   `requestText` for non-200.
2. Add `Config.Logger *slog.Logger` (nil → `slog.New(slog.DiscardHandler)`),
   and log one line per engine failure at `Debug`: engine, status, elapsed.
3. Let `DDGS.search` swallow `ErrHTTPStatus` for result purposes (preserving the
   current user-visible behaviour) but count it as a failure and log it.
4. Log parse errors the same way instead of returning bare `nil`.

**Acceptance.** `TestSearchLogsHTTPFailure` with a `slog` handler writing to a
buffer, asserting the status appears; existing behaviour tests must stay green.

## F15. Parity goldens skip every case where Python failed, and omit three engines

**Where:** `parity_engine_test.go:36-49,81-83,88-95,173-179`;
`testdata/gen_engine_golden.py:49-68`

**Problem.** The golden data has no `**kwargs` cases at all, so the image/video
filters were never compared. Every case where Python raised is skipped, which is
exactly where F6 hid. `startpage`, `wikipedia` and `grokipedia` are absent from
the factory table; their statement coverage is 20.0%, 16.7% and 23.8%. Extract
goldens cover only four fixtures.

**Change.**

1. Extend `gen_engine_golden.py` with kwargs cases (`size`, `color`,
   `type_image`, `layout`, `license_image`, `resolution`, `duration`,
   `license_videos`) and with cases that make Python raise.
2. Stop skipping `c.Error != ""`: assert that Go returns an error too, and pin
   the error class.
3. Add `startpage`, `wikipedia`, `grokipedia` to the factory table; add extract
   goldens for `brave`, `mojeek`, `yahoo`, `startpage`, `yahoo_news`,
   `bing_news`, `bing_images`, `annasarchive`.
4. Record `upstream_version: "9.16.0"` in every fixture (the current generator
   header still says v9.14.4; verified harmless because `utils.py`,
   `similarity.py` and `results.py` are byte-identical between the two tags).
5. Replace the author-machine absolute paths in both generators
   (`/opt/dlami/nvme/...`, `/tmp/ddgs-src`) with a `--python-src` argument.

**Acceptance.** `go test ./...` green with the enlarged golden set; a deliberate
bug injected into one engine must fail at least one parity test.

## F16. API and MCP accept unbounded `max_results`

**Where:** `internal/apiserver/models.go:51-89`; `internal/mcpserver/mcpserver.go:504-525`

**Problem.** A JSON number such as `1e300` truncates to `MaxInt64` on arm64 and
`MinInt64` on amd64; both end up meaning "as many as possible". One request then
lets every engine run to completion.

**Change.** Validate in both decoders: accept `-1` (unlimited) or `1..1000`;
reject everything else with 400 (`OptInt`) / `-32602` or a tool error (MCP).

**Acceptance.** Table test over `-2`, `0`, `1e300`, `1001`, `"abc"`.

## F17. MCP answers nothing for `notifications/*` carrying an id

**Where:** `internal/mcpserver/mcpserver.go:181-184`

**Problem.** `req.ID == nil || strings.HasPrefix(req.Method, "notifications/")`
treats a request with an id as a notification, violating JSON-RPC 2.0 ("a request
with an id must be answered").

**Change.** Only `req.ID == nil` marks a notification. Keep the defensive log
line for `notifications/*`.

**Acceptance.** `TestNotificationWithIDGetsResponse`.

## F18. A panic in one engine takes down the process

**Where:** `ddgs.go:311-318`

**Problem.** Fan-out goroutines have no `recover`. Any parser panic (untrusted
HTML/JSON) terminates the whole server. The fuzz run found none today, but the
surface is untrusted input.

**Change.** `defer func() { if r := recover(); r != nil { ch <- engineResult{key: ..., err: fmt.Errorf("%w: engine %s panicked: %v", ErrDDGS, e.Name(), r)} } }()`
so the search degrades to one failed engine.

**Acceptance.** A fake engine that panics; the search must return the other
engines' results and log the panic once.

## F19. Transport uses zero values; no way to close idle connections

**Where:** `httpclient.go:63-65,100-110`

**Problem.** `IdleConnTimeout` zero means "never expire"; `TLSClientConfig` being
non-nil disables automatic HTTP/2. There is no `Close`, so a host that builds a
`DDGS` per request leaks idle connections and their goroutines.

**Change.** Set `IdleConnTimeout: 90s`, `MaxIdleConnsPerHost: 4`,
`ForceAttemptHTTP2: true`; add `func (d *DDGS) Close()` that calls
`CloseIdleConnections()` on every cached engine.

**Acceptance.** `TestCloseIdleConnections` (no error, idempotent).

## F20. Shared cookie jar mixes region state across concurrent searches

**Where:** `engine_text.go:86,193,225`; `httpclient.go:120-133`; `ddgs.go:251-261`

**Problem.** Google/Brave/Mojeek call `setCookies` inside `buildPayload`, but the
engine instance is cached and shared, so concurrent searches with different
`region` values can interleave cookies. Python has the same flaw, so this is
parity — but it should be documented at minimum.

**Change.** Either (a) document "one DDGS instance with concurrent different
regions is not isolated" in the `DDGS` doc comment, or (b) clone the jar per
search. Recommend (a) for this round: (b) changes connection reuse.

**Acceptance.** Documentation-only; no test.

## F21. Smaller items

| Where | Issue | Change |
|---|---|---|
| `internal/output/download.go:149-163` | decoded `/` survives, so `os.Create` fails on a missing subdirectory | `filepath.Base` before the `..` replacement; reject `\x00` |
| `internal/apiserver/server.go:428-433` | 405 has no `Allow` header | set `Allow` from the matched route's methods |
| `internal/mcpserver/mcpserver.go:39-45` | `initialize` always answers `2024-11-05` | echo the client's version when supported, else answer the latest supported one |
| `httpclient.go:49,108` | `userAgent` field is written but never read | delete it |
| `ddgs.go:214-217` | `sort.Strings` immediately followed by `rand.Shuffle` | drop the sort, or drop the shuffle |
| `httpclient.go:150-157` | string-concatenated query breaks with `#fragment` | build with `url.Values` + `URL.RawQuery` |
| `engine_wiki.go:119-128` | empty `slug` builds `https://grokipedia.com/page/` | drop the item or return an error |
| `engine_wiki.go:66-84` | extract fetch errors and bad JSON are swallowed | return the error; let the engine fail like Python |
| `engine_misc.go:99-107` | absolute dates parsed as UTC, Python uses local tz | `time.ParseInLocation(..., time.Local)` or document the divergence |
| `engine_misc.go:67-81` | malformed Bing image entries are skipped, Python fails the engine | document as an intentional divergence in SPEC §9 |
| `engine.go:193-199` | `splitRegion` accepts malformed regions | require exactly two segments; reuse `splitRawRegion` |
| `engine_text.go:128` | degradation path slices bytes, Python slices runes | `utf8.DecodeLastRuneInString` |
| `engine_misc.go:270-275` | Anna's Archive TLD randomised per instance | hoist to a package-level variable |
| `engine_misc.go:33-38` | `Extra["max_results"]` is never set by the library | delete, or comment why it exists |
| `parity_engine_test.go:155-156` | `fmt.Sprint` hides int/float differences | type-sensitive comparison (covered by F7) |
| `google_wml_test.go:9-10` | hand-authored fixture proves nothing about the real DOM | capture one real WML response as a fixture |
| `.github/workflows/ci.yml` | integration job is `continue-on-error` and push-only | add a scheduled run and surface failures in the summary |

---

# Newly documented divergences (SPEC §9 additions)

These are **not** bugs to fix in this round; they are behaviour differences that
should be written down so the next contributor does not "fix" them:

1. `Extract` blocks private/loopback targets unless `AllowPrivateNetworks` is set
   (F1 — new, deliberate hardening).
2. Same-provider engines are serialised (`launchedProviders`), whereas Python
   launches them concurrently and deduplicates only after success.
3. Malformed result entries are skipped rather than failing the whole engine
   (Bing Images).
4. MCP `extract_content` base64-encodes non-UTF-8 bodies for `fmt=content`.
5. MCP reports unknown tools as `isError: true` tool results instead of `-32602`.

# Test plan and gates

Every batch must keep the existing gates green:

```
gofmt -l .        # empty
go vet ./...      # clean
go test ./...     # all green
go test -race ./...   # CI only; TSan is unavailable under PRoot/aarch64
```

Additional gates introduced by this plan:

- `TestXPathsCompile` (from the engine review): parse every registered engine's
  XPath at test time, so a typo fails before production traffic reaches it.
- Parity tests must fail when an engine's payload diverges in **type**, not just
  in `fmt.Sprint` output.
- A `-short` mode must skip the new `httptest`-based SSRF tests' external DNS
  lookups (use literal IPs to keep them hermetic).

# Out of scope

- TLS impersonation (`utls`) — tracked as F1 in CHANGELOG's known limitations.
- `ddgs api --reload` (needs fsnotify or a re-exec loop) — F2.
- `/docs`, `/openapi.json` — F3.
- Packaging (Homebrew, AUR) — F4.
- Authentication for the HTTP API: out of scope for a library port; document it
  instead.

# Appendix — findings index

| # | Sev | Location | Issue | Verified |
|---|---|---|---|---|
| F1 | major | `extract.go:66` | SSRF via `/extract` and `extract_content` | V |
| F2 | major | `cmd/ddgs-api/main.go:36` | `0.0.0.0` default, no auth, `*`+credentials CORS | V |
| F3 | major | `httpclient.go:190` | unbounded response bodies / MCP line | V |
| F4 | major | `cmd/ddgs/commands.go:316` | unvalidated PID from file | V |
| F5 | major | `cmd/ddgs/proc.go:21` | zombie passes liveness check | V |
| F6 | major | `engine_misc.go:45` | Bing Images timelimit always 0 | V |
| F7 | major | `engine_duckduckgo.go:70` | `height`/`width` float64, printed `100.0` | V |
| F8 | major | `internal/output/print.go:239` | space lost when wrapping long tokens | V |
| F9 | major | `internal/output/style.go:139` | `/dev/null` treated as TTY → hang | V |
| F10 | minor | `internal/output/save.go:196` | CSV writes `None` for null | V |
| F11 | minor | `internal/apiserver/server.go:231` | books GET/POST `max_results=0` differ | V |
| F12 | minor | `ddgs.go:334` | cooldown applies to explicit backend list | V |
| F13 | minor | `ddgs.go:352` | empty results count as failures | V |
| F14 | major | `engine.go:92` | non-200/parse failures silent | V |
| F15 | major | `parity_engine_test.go:81` | goldens skip Python errors, 3 engines missing | V |
| F16 | minor | `internal/apiserver/models.go:51` | unbounded `max_results` | V |
| F17 | minor | `internal/mcpserver/mcpserver.go:181` | notification with id unanswered | V |
| F18 | minor | `ddgs.go:311` | no panic recovery in fan-out | V |
| F19 | minor | `httpclient.go:63` | transport zero values, no `Close()` | V |
| F20 | minor | `engine_text.go:86` | shared cookie jar across regions | R |
| F21 | nit | see table above | 17 small items | R |

Severity counts: 11 major, 9 minor, 17 small items bundled under F21, and 2 open
questions (F20 and the divergence list require a decision rather than code).
