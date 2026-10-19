// Command ddgs-mcp starts the DDGS MCP (Model Context Protocol) server — the
// Go equivalent of running ddgs/api_server/mcp.py through the Python mcp SDK.
//
// Usage:
//
//	ddgs-mcp [-pr|--proxy socks5h://127.0.0.1:9150]
//
// It speaks JSON-RPC 2.0 over stdio (one message per line): stdout carries the
// protocol, stderr carries diagnostics. Configure an MCP client with the
// binary path, e.g.
//
//	{"mcpServers": {"ddgs": {"command": "ddgs-mcp"}}}
//
// The proxy falls back to DDGS_PROXY (resolved inside ddgs.New), and the alias
// "tb" expands to the Tor Browser proxy — same as _expand_proxy_tb_alias in
// mcp.py. One DDGS instance serves the whole session (SPEC §7.5).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/mcpserver"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

func main() {
	if err := run(); err != nil {
		log.Printf("ddgs-mcp: %v", err)
		os.Exit(1)
	}
}

func run() error {
	// Both spellings share one variable: stdlib flag has no aliasing, and
	// registering two StringVars on the same variable keeps them in sync
	// (whichever appears last on the command line wins).
	var proxy string
	flag.StringVar(&proxy, "pr", "", `Proxy to send requests, e.g. socks5h://127.0.0.1:9150 ("tb" = Tor Browser)`)
	flag.StringVar(&proxy, "proxy", "", "Alias for -pr")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s (MCP stdio server, mirrors mcp.py):\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nTools: search_text, search_images, search_news, search_videos, search_books, extract_content\n"+
			"Transport: JSON-RPC 2.0 over stdin/stdout (one message per line); logs go to stderr\n")
	}
	flag.Parse()

	// ddgs.New resolves the proxy (flag, then DDGS_PROXY env) and expands the
	// "tb" alias, exactly like DDGS(proxy=_expand_proxy_tb_alias(...)) in
	// mcp.py.
	instance, err := ddgs.New(&ddgs.Config{Proxy: proxy})
	if err != nil {
		return fmt.Errorf("failed to create DDGS instance: %w", err)
	}

	// RunStdio returns nil on clean EOF (client closed stdin) and an error
	// only for transport failures.
	return mcpserver.RunStdio(searchsvc.New(instance), os.Stdin, os.Stdout, os.Stderr)
}
