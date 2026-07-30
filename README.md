# ddgs-go | Dux Distributed Global Search (Go)

A Go port of the Python [ddgs](https://github.com/deedy5/ddgs) library — a
metasearch library that aggregates results from diverse web search services,
with an API that mirrors the Python version.

## Install

```bash
go get github.com/dreamyang-liu/ddgs-go
```

## Quick start

```go
package main

import (
	"context"
	"fmt"

	ddgs "github.com/dreamyang-liu/ddgs-go"
)

func main() {
	d, err := ddgs.New(nil) // defaults: 5s timeout, no proxy, TLS verify on
	if err != nil {
		panic(err)
	}
	results, err := d.Text(context.Background(), "golang concurrency", nil)
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		fmt.Println(r["title"], r["href"])
	}
}
```

## API mapping (Python → Go)

| Python | Go |
|---|---|
| `DDGS(proxy=..., timeout=..., verify=...)` | `ddgs.New(&ddgs.Config{Proxy, Timeout, Verify, Threads})` |
| `ddgs.text(query, ...)` | `d.Text(ctx, query, opts)` |
| `ddgs.images(query, ...)` | `d.Images(ctx, query, opts)` |
| `ddgs.news(query, ...)` | `d.News(ctx, query, opts)` |
| `ddgs.videos(query, ...)` | `d.Videos(ctx, query, opts)` |
| `ddgs.books(query, ...)` | `d.Books(ctx, query, opts)` |
| `ddgs.extract(url, fmt)` | `d.Extract(ctx, url, format)` |
| `region="us-en"` | `opts.Region` (default `"us-en"`) |
| `safesearch="moderate"` | `opts.SafeSearch` (default `"moderate"`) |
| `timelimit="d"/"w"/"m"/"y"` | `opts.TimeLimit` |
| `max_results=10` | `opts.MaxResults` (default 10; `-1` = unlimited, like `None`) |
| `page=1` | `opts.Page` |
| `backend="auto"` | `opts.Backend` (comma-delimited, default `"auto"`) |
| `**kwargs` (size, color, resolution, …) | `opts.Extra map[string]string` |
| `DDGS_PROXY` env var | `DDGS_PROXY` env var (same) |
| `proxy="tb"` Tor alias | `Proxy: "tb"` (same) |
| `DDGSException` / `TimeoutException` | `ddgs.ErrDDGS` / `ddgs.ErrTimeout` (`errors.Is`) |

Results are returned as `[]ddgs.Result` where `Result` is
`map[string]any` with the same keys as the Python dicts
(`title`, `href`, `body` for text; `date`, `title`, `body`, `url`, `image`,
`source` for news; etc.).

## Engines

| Function | Available backends |
|---|---|
| `Text` | `brave`, `duckduckgo`, `google`, `grokipedia`, `mojeek`, `startpage`, `wikipedia`, `yahoo`, `yandex` |
| `Images` | `bing`, `duckduckgo` |
| `News` | `bing`, `duckduckgo`, `yahoo` |
| `Videos` | `duckduckgo` |
| `Books` | `annasarchive` |

Result semantics match the Python implementation: with `backend: "auto"`
engines are shuffled, wikipedia/grokipedia are prioritized for text search,
one request is issued per unique provider, results are deduplicated by URL,
counted across engines, and ranked (wikipedia first, then title+body match,
title-only, body-only, neither). Behavior is verified against Python ddgs
v9.14.4 by golden-vector parity tests (`testdata/`).

The scheduler improves on the Python thread-pool waves:

- **Streaming fan-out** — results are consumed as each engine finishes; once
  `MaxResults` is reached, in-flight requests are cancelled instead of
  waiting on the slowest engine.
- **General-engine floor** — at least one general SERP engine is in flight
  from the start, alongside the single-result encyclopedia engines.
- **Failure cooldown** — engines that fail repeatedly (e.g. IP-blocked) are
  skipped in auto mode for 5 minutes. In-memory, per `DDGS` instance; reuse
  one instance to benefit. Explicit `Backend` selection bypasses it.

## Examples

```go
ctx := context.Background()

// Specific backends, news from the last week
news, err := d.News(ctx, "AI", &ddgs.SearchOptions{
	Backend:    "duckduckgo,yahoo",
	TimeLimit:  "w",
	MaxResults: 20,
})

// Image search with filters (same kwargs as Python)
imgs, err := d.Images(ctx, "sunset", &ddgs.SearchOptions{
	Extra: map[string]string{"size": "Large", "color": "Monochrome"},
})

// Behind a proxy
d, err := ddgs.New(&ddgs.Config{Proxy: "socks5://127.0.0.1:9150"})

// Extract page content as Markdown
page, err := d.Extract(ctx, "https://example.com", ddgs.FormatTextMarkdown)
fmt.Println(page.Content)
```

## Differences from the Python version

- Methods take a `context.Context` and return `error` (Go idiom) instead of
  raising exceptions.
- `max_results=None` is expressed as `MaxResults: -1`.
- HTTP requests use `net/http` with browser-like headers instead of primp's
  TLS impersonation, so heavily-fingerprinting engines may rate-limit sooner.
- `Extract`'s markdown conversion uses
  [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown)
  (links, emphasis and tables preserved as standard CommonMark) rather than
  primp's Rust converter; output is richer but not byte-identical.
- The auto-mode scheduler streams results and cancels early (see Engines);
  the Python version waits on thread-pool waves.
- The CLI / API server / MCP server of the Python package are out of scope;
  this is the library only.

## Disclaimer

Same as upstream: this library is for educational purposes; you are
responsible for complying with the terms of service of the search engines
it talks to.
