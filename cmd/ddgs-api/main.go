// Command ddgs-api starts the DDGS REST API server — the Go equivalent of
// `ddgs api` in the Python package (ddgs/cli.py's api command serving
// ddgs/api_server/api.py).
//
// Usage:
//
//	ddgs-api [--host 0.0.0.0] [--port 4479] [-pr|--proxy socks5h://127.0.0.1:9150]
//	         [--timeout 5s] [--reload]
//
// The proxy falls back to the DDGS_PROXY environment variable, mirroring
// api.py's _get_ddgs(); the alias "tb" expands to the Tor Browser proxy.
//
// One shared *ddgs.DDGS instance serves every request (SPEC §7.5 — the Go form
// of api.py's _get_ddgs() singleton), and the process shuts down gracefully on
// SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/apiserver"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

const (
	defaultHost    = "0.0.0.0" // cli.py: --host default "0.0.0.0"
	defaultPort    = 4479      // cli.py: --port default 4479
	defaultTimeout = 5 * time.Second
	defaultGrace   = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	var (
		host     = flag.String("host", defaultHost, "Host to bind the server to")
		port     = flag.Int("port", defaultPort, "Port to bind the server to")
		proxy    = flag.String("pr", "", `Proxy to send requests, e.g. socks5h://127.0.0.1:9150 ("tb" = Tor Browser)`)
		timeout  = flag.Duration("timeout", defaultTimeout, "Library per-engine request timeout")
		grace    = flag.Duration("shutdown-timeout", defaultGrace, "Graceful shutdown timeout")
		reload   = flag.Bool("reload", false, "Auto-reload on code changes (CLI parity; accepted but no-op)")
		proxyAlt = flag.String("proxy", "", "Alias for -pr")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s (mirrors `ddgs api`):\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nEndpoints:\n"+
			"  GET  /  ,  /health\n"+
			"  GET|POST /search/{text,images,news,videos,books}\n"+
			"  GET|POST /extract\n")
	}
	flag.Parse()

	if *reload {
		// SPEC §9.8: --reload is accepted for CLI parity but not implemented
		// (real hot-reload needs fsnotify; recorded as future work F2).
		log.Println("warning: --reload is not supported by the Go server; ignoring")
	}

	// A single shared DDGS instance: the Go form of api.py's _get_ddgs().
	// ddgs.New resolves the proxy (flag, then DDGS_PROXY env) and expands the
	// "tb" alias, exactly like _expand_proxy_tb_alias(os.environ["DDGS_PROXY"]).
	chosenProxy := *proxy
	if chosenProxy == "" {
		chosenProxy = *proxyAlt
	}
	instance, err := ddgs.New(&ddgs.Config{
		Proxy:   chosenProxy,
		Timeout: *timeout,
	})
	if err != nil {
		return fmt.Errorf("failed to create DDGS instance: %w", err)
	}

	handler := apiserver.New(
		searchsvc.New(instance),
		apiserver.WithLibraryTimeout(*timeout),
	)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	// Startup banner mirrors cli.py's api() foreground branch.
	fmt.Printf("Starting DDGS API server on http://%s\n", addr)
	switch {
	case chosenProxy != "":
		fmt.Printf("Using proxy: %s\n", chosenProxy)
	case os.Getenv("DDGS_PROXY") != "":
		fmt.Printf("Using proxy: %s\n", os.Getenv("DDGS_PROXY"))
	}
	fmt.Println("Press Ctrl+C to stop")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Graceful shutdown: stop accepting new connections, let in-flight
		// searches finish within the grace window.
		stop()
		log.Printf("shutting down (allowing %s for in-flight requests)", *grace)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), *grace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}
		return nil
	}
}
