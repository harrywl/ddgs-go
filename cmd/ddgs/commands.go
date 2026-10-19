package main

// The extract / mcp / api subcommands of the ddgs CLI, mirroring the
// corresponding @cli.command() definitions in ddgs/cli.py.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/apiserver"
	"github.com/harrywl/ddgs-go/internal/mcpserver"
	"github.com/harrywl/ddgs-go/internal/output"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

// ---------------------------------------------------------------------------
// ddgs extract
// ---------------------------------------------------------------------------

// runExtract mirrors cli.py's `extract` command:
//
//	ddgs extract -u URL [-f fmt] [-o output] [-pr proxy] [-v verify]
//
// Without -o the result is echoed as "URL: ...\n\n<content>"; with -o it is
// saved through the same _save_data path as the search commands (a single-row
// list holding the {url, content} dict).
func runExtract(args []string) error {
	var (
		url    string
		format string
		out    string
		proxy  string
		verify bool
	)
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stringFlag(fs, &url, "u", "url", "", "URL to extract content from")
	stringFlag(fs, &format, "f", "format", "text_markdown", "output format ("+joinChoices(extractFmtChoices)+")")
	stringFlag(fs, &out, "o", "output", "", "json or filename.json (save the results to a file)")
	stringFlag(fs, &proxy, "pr", "proxy", "", "the proxy to send requests, example: socks5h://127.0.0.1:9150")
	// cli.py's extract also uses @click.option("-v", "--verify", default=True),
	// i.e. a BOOL-valued option that takes an argument.
	boolishFlag(fs, &verify, "v", "verify", true, "verify SSL when making the request")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ddgs extract -u URL [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return asUsageError(err)
	}
	if url == "" {
		// click would fail with "Missing option '-u' / '--url'".
		return usagef("missing required option -u/--url")
	}
	if err := checkChoice("-f/--format", format, extractFmtChoices); err != nil {
		return asUsageError(err)
	}

	d, err := ddgs.New(&ddgs.Config{Proxy: proxy, Verify: verifyOf(verify)})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	extracted, err := d.Extract(ctx, url, ddgs.ExtractFormat(format))
	if err != nil {
		// Surfaces ErrUnsupportedFormat for -f text_rich with the library's
		// explanatory message rather than a generic failure.
		return err
	}

	content := extracted.Content
	if ddgs.ExtractFormat(format) == ddgs.FormatContent {
		content = string(extracted.Bytes)
	}

	if out == "" {
		fmt.Printf("URL: %s\n\n", url)
		fmt.Println(content)
		return nil
	}

	// cli.py builds [{k: v.decode() if bytes else v}] and saves it under the
	// "extract" function name; the sanitize step uses the URL as the query.
	// The row order is url then content, matching Python's dict insertion
	// order (see output.extraFieldOrder).
	row := ddgs.Result{"url": extracted.URL, "content": content}
	// _save_data is silent on success; it returns nothing and prints nothing.
	_, err = output.SaveData(output.SanitizeQuery(url), []ddgs.Result{row}, "extract", "extract", out)
	return err
}

// ---------------------------------------------------------------------------
// ddgs mcp
// ---------------------------------------------------------------------------

// runMCP mirrors cli.py's `mcp` command: set DDGS_PROXY from -pr when given,
// then run the stdio server until stdin closes.
func runMCP(args []string) error {
	var proxy string
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stringFlag(fs, &proxy, "pr", "proxy", "", "the proxy to send requests, example: socks5h://127.0.0.1:9150")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ddgs mcp [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return asUsageError(err)
	}

	// cli.py sets os.environ["DDGS_PROXY"] before starting the server; the
	// library reads that variable when no proxy is configured explicitly.
	if proxy != "" {
		os.Setenv("DDGS_PROXY", proxy)
	}

	d, err := ddgs.New(&ddgs.Config{Proxy: proxy})
	if err != nil {
		return err
	}
	// stdout is the JSON-RPC transport; logs must go to stderr only.
	return mcpserver.RunStdio(searchsvc.New(d), os.Stdin, os.Stdout, os.Stderr)
}

// ---------------------------------------------------------------------------
// ddgs api
// ---------------------------------------------------------------------------

// apiPIDFile mirrors cli.py's _PID_FILE = ~/.cache/ddgs/api.pid.
func apiPIDFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.cache/ddgs/api.pid", nil
}

// apiFlags holds the options of cli.py's `api` command.
type apiFlags struct {
	detach bool
	stop   bool
	host   string
	port   int
	reload bool
	proxy  string
}

// newAPIFlagSet registers the api option tree on a fresh FlagSet.
func newAPIFlagSet(f *apiFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	boolFlag(fs, &f.detach, "d", "detach", false, "run the server in detached mode (background)")
	boolFlag(fs, &f.stop, "s", "stop", false, "stop the detached server")
	stringFlag(fs, &f.host, "", "host", "0.0.0.0", "host to bind the server to")
	intFlag(fs, &f.port, "", "port", 4479, "port to bind the server to")
	boolFlag(fs, &f.reload, "", "reload", false, "enable auto-reload on code changes")
	stringFlag(fs, &f.proxy, "pr", "proxy", "", "the proxy to send requests, example: socks5h://127.0.0.1:9150")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ddgs api [options]\n\nOptions:\n")
		fs.PrintDefaults()
	}
	return fs
}

// parseAPIFlags parses the api command's args.
func parseAPIFlags(args []string) (*apiFlags, error) {
	f := &apiFlags{}
	fs := newAPIFlagSet(f)
	if err := fs.Parse(args); err != nil {
		return nil, asUsageError(err)
	}
	if fs.NArg() > 0 {
		return nil, usagef("unexpected argument %q", fs.Arg(0))
	}
	return f, nil
}

// runAPI mirrors cli.py's `api` command: foreground serve, -d detach, -s stop.
func runAPI(args []string) error {
	f, err := parseAPIFlags(args)
	if err != nil {
		return err
	}

	if f.stop {
		return stopDetachedAPI()
	}
	if f.reload {
		// SPEC §9.8: accepted for CLI compatibility, not implemented.
		fmt.Fprintln(os.Stderr, "warning: --reload is not supported in the Go port; ignoring")
	}
	if f.detach {
		return startDetachedAPI(f.host, f.port, f.proxy)
	}
	return serveAPI(f.host, f.port, f.proxy)
}

// serveAPI runs the API server in the foreground with graceful shutdown.
func serveAPI(host string, port int, proxy string) error {
	if proxy != "" {
		os.Setenv("DDGS_PROXY", proxy)
	}
	d, err := ddgs.New(&ddgs.Config{Proxy: proxy})
	if err != nil {
		return err
	}
	handler := apiserver.New(searchsvc.New(d))
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	fmt.Printf("Starting DDGS API server on http://%s\n", addr)
	if proxy != "" {
		fmt.Printf("Using proxy: %s\n", proxy)
	}
	fmt.Println("Press Ctrl+C to stop")

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-sig:
		fmt.Fprintln(os.Stderr, "\nshutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

// startDetachedAPI mirrors the detach branch of cli.py's `api`: re-exec this
// binary without -d, write the child PID to the pid file, and report it.
func startDetachedAPI(host string, port int, proxy string) error {
	pidFile, err := apiPIDFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(pidFile), 0o755); err != nil {
		return err
	}

	// Rebuild the argv without -d/--detach so the child runs in foreground.
	argv := []string{os.Args[0], "api", "--host", host, "--port", strconv.Itoa(port)}
	if proxy != "" {
		argv = append(argv, "-pr", proxy)
	}
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 — argv[0] is this binary
	cmd.Stdout = nil                          // cli.py uses DEVNULL for both streams
	cmd.Stderr = nil
	cmd.SysProcAttr = detachedSysProcAttr()
	if proxy != "" {
		cmd.Env = append(os.Environ(), "DDGS_PROXY="+proxy)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// cli.py sleeps 0.5s and checks the process is still alive. Because Go's
	// cmd.ProcessState is only populated after Wait, poll the child directly
	// via signal 0 instead of inspecting ProcessState.
	time.Sleep(500 * time.Millisecond)
	if childGone(cmd.Process.Pid) {
		return fmt.Errorf("failed to start server: process exited early")
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	// Do not reap the child; it must outlive this command.
	_ = cmd.Process.Release()

	fmt.Printf("DDGS API server started in detached mode on http://%s (PID: %d)\n", addrOf(host, port), pid)
	if proxy != "" {
		fmt.Printf("Using proxy: %s\n", proxy)
	}
	return nil
}

// stopDetachedAPI mirrors the -s branch of cli.py's `api`.
func stopDetachedAPI() error {
	pidFile, err := apiPIDFile()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No detached server is running (PID file not found)")
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		_ = os.Remove(pidFile)
		return fmt.Errorf("invalid PID file contents %q", string(raw))
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		err = proc.Signal(syscall.SIGTERM)
	}
	switch {
	case errors.Is(err, os.ErrProcessDone) || isNoSuchProcess(err):
		fmt.Printf("Server process (PID: %d) was not running, cleaning up PID file\n", pid)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Failed to stop server: %v\n", err)
		_ = os.Remove(pidFile)
		return err
	default:
		fmt.Printf("DDGS API server stopped (PID: %d)\n", pid)
	}
	_ = os.Remove(pidFile)
	return nil
}

func joinChoices(c []string) string { return strings.Join(c, ", ") }
