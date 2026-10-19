# ddgs-go

**DDGS | Dux Distributed Global Search** — a metasearch library and toolkit in
Go, at parity with Python [ddgs v9.16.0](https://github.com/deedy5/ddgs).

One API aggregates results from multiple search engines at once:

| Category | Backends |
|---|---|
| text | brave, duckduckgo, google, grokipedia, mojeek, startpage, wikipedia, yahoo |
| images | bing, duckduckgo |
| news | bing, duckduckgo, yahoo |
| videos | duckduckgo |
| books | annasarchive |

Three binaries ship alongside the library: `ddgs` (CLI), `ddgs-api` (HTTP
server) and `ddgs-mcp` (MCP stdio server) — the Go equivalents of Python's
`ddgs` command, its FastAPI app and its MCP server.

## Install

```sh
# library
go get github.com/harrywl/ddgs-go

# binaries
go install github.com/harrywl/ddgs-go/cmd/ddgs@latest
go install github.com/harrywl/ddgs-go/cmd/ddgs-api@latest
go install github.com/harrywl/ddgs-go/cmd/ddgs-mcp@latest
```

Or grab a prebuilt binary (linux/arm64, linux/arm, darwin/arm64) from
[Releases](https://github.com/harrywl/ddgs-go/releases).

## Library quickstart

```go
package main

import (
	"context"
	"fmt"

	ddgs "github.com/harrywl/ddgs-go"
)

func main() {
	d, err := ddgs.New(nil) // nil = defaults: no proxy (or $DDGS_PROXY), 5s timeout, TLS verify on
	if err != nil {
		panic(err)
	}
	results, err := d.Text(context.Background(), "golang context", &ddgs.SearchOptions{
		MaxResults: 5,
		Region:     "us-en",
		Backend:    "auto", // or "duckduckgo,brave,google", or "wikipedia"
	})
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		fmt.Println(r["title"], "|", r["href"])
	}
}
```

### API ↔ Python mapping

| Python | Go |
|---|---|
| `DDGS(proxy=…, timeout=…, verify=…)` | `ddgs.New(&ddgs.Config{Proxy:…, Timeout:…, Verify:…})` |
| `d.text(q, max_results=5)` | `d.Text(ctx, q, &ddgs.SearchOptions{MaxResults: 5})` |
| `d.images/news/videos/books(…)` | `d.Images/News/Videos/Books(ctx, q, opts)` |
| `d.extract(url, fmt="text_markdown")` | `d.Extract(ctx, url, ddgs.FormatTextMarkdown)` |
| `max_results=None` (everything) | `MaxResults: -1` |
| `keywords=…` extra filters | `SearchOptions.Extra` map (`size`, `color`, `type_image`, `layout`, `license_image`, `resolution`, `duration`, `license_videos`) |
| `DDGSException` / `TimeoutException` / `RatelimitException` | errors wrapping `ddgs.ErrDDGS` / `ddgs.ErrTimeout` / `ddgs.ErrRatelimit` (`errors.Is`) |
| `DDGS_PROXY` env, `"tb"` proxy alias | same |

Every method takes a `context.Context` — cancellation and deadlines propagate
to all in-flight engine requests. `DDGS` is safe for concurrent use; construct
one and share it.

## CLI

```sh
ddgs text -q "golang context" -m 5
ddgs images -q "cat" -size Large -o json          # save to json file
ddgs images -q "cat" -d -dd ./cats -th 8          # download images
ddgs news -q "ai" -t d -b duckduckgo,bing         # last day, two backends
ddgs videos -q "gopher" -res high
ddgs books -q "dune"
ddgs extract -u https://go.dev/doc/effective_go -f text_markdown
ddgs api --port 4479                              # HTTP server
ddgs mcp                                          # MCP stdio server
ddgs version
```

Flags mirror the Python CLI (`ddgs --help` there): `-q/-k/-r/-s/-t/-m/-p/-b`,
`-o` output (`csv`, `json`, or `name.csv|name.json`), `-d/-dd/-th` downloads,
`-pr` proxy (`tb` = Tor Browser socks5h alias), `-v` verify, `-nc` no color.

## HTTP API server

```sh
ddgs-api --host 0.0.0.0 --port 4479
curl -s http://127.0.0.1:4479/search/text?query=golang&max_results=3
curl -s -X POST http://127.0.0.1:4479/search/images \
     -H 'Content-Type: application/json' \
     -d '{"query":"cat","max_results":3,"size":"Large"}'
```

Endpoints: `GET|POST /search/{text,images,news,videos,books}`, `GET|POST
/extract`, `GET /`, `GET /health`. JSON field names, defaults and the
`{"detail": …}` error shape match the Python FastAPI server, so clients of one
work against the other.

## MCP server

```sh
ddgs-mcp        # JSON-RPC 2.0 over stdio
```

Tools: `search_text`, `search_images`, `search_news`, `search_videos`,
`search_books`, `extract_content` — same names/parameters as the Python MCP
server. Client configuration:

```json
{
  "mcpServers": {
    "ddgs": {
      "command": "ddgs-mcp"
    }
  }
}
```

## How it works

`DDGS.search` fans out to engines concurrently and **streams**: as soon as any
engine returns, its results are deduplicated (by href/image/url/embed_url) and
counted; once `MaxResults` is reached all in-flight requests are cancelled.
Engines are queried in priority order (wikipedia 2.0, grokipedia 1.9, rest 1.0)
with one request per unique provider; in auto mode an engine that fails twice
in a row is put on a 5-minute cooldown. Results are finally ranked by a
wikipedia-first, token-overlap bucketing ranker — the same
`SimpleFilterRanker` as Python.

## Differences from the Python implementation

Deliberate, documented in [docs/SPEC.md](docs/SPEC.md) §9:

- Streaming fan-out scheduler instead of wave-based thread pools; failure
  cooldown per engine; `context.Context` everywhere.
- **No TLS impersonation**: Python uses primp (browser TLS/JA3 fingerprints);
  this port uses `net/http` with a browser UA pool. Fingerprint-sensitive
  engines may rate-limit the Go client sooner.
- `extract` format `text_rich` returns `ErrUnsupportedFormat` (primp-only).

## Development

```sh
make build        # bin/ddgs, bin/ddgs-api, bin/ddgs-mcp
make test         # unit tests (no network)
make integration  # live-engine tests (-tags=integration)
make lint         # gofmt check + go vet
make release      # cross-compile the release platforms into dist/
```

Parity is enforced by golden tests: engine payloads and extraction results are
asserted against fixtures generated by the Python implementation
(`testdata/parity_*.json`), and `TestRegistryMatchesPython` pins the backend
registry to upstream's `ENGINES` dict.

## Credits & License

MIT. This repo is a fork of
[dreamyang-liu/ddgs-go](https://github.com/dreamyang-liu/ddgs-go) (the original
Go port), brought to parity with
[deedy5/ddgs](https://github.com/deedy5/ddgs) v9.16.0 (MIT, Igor Babin) — the
Python original whose engine logic is mirrored here file by file. See
[LICENSE](LICENSE) and [CHANGELOG.md](CHANGELOG.md).
