// Package mcpserver implements the MCP (Model Context Protocol) server for
// ddgs-go, mirroring ddgs/api_server/mcp.py (v9.16.0).
//
// Transport: JSON-RPC 2.0 over stdio, one JSON message per line, exactly what
// the Python `mcp` SDK's stdio transport speaks and what MCP clients (Claude
// Desktop, Minis, …) expect. Responses are flushed after every message; all
// diagnostics go to a separate log writer so stdout stays a clean transport.
//
// The six tools are declared in tools.go. Search execution goes through
// internal/searchsvc.Searcher rather than *ddgs.DDGS so tests can inject a
// stub without any network access.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"unicode/utf8"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

// ServerName and ProtocolVersion are reported by initialize.
const (
	// ServerName is the MCP serverInfo.name. mcp.py constructs its server as
	// MCPServer("ddgs-search"); the Go port identifies itself as "ddgs-go"
	// per SPEC §5/D1.
	ServerName = "ddgs-go"

	// ProtocolVersion is the MCP revision advertised in initialize.
	//
	// TODO: mcp.py delegates version negotiation to the Python mcp SDK
	// (`mcp>=2.0`, see pyproject.toml), which answers with its own
	// LATEST_PROTOCOL_VERSION; that value is not pinned anywhere in the
	// upstream source. 2024-11-05 is the revision mandated by SPEC §5/D1 and
	// is understood by every current MCP client.
	ProtocolVersion = "2024-11-05"
)

// JSON-RPC 2.0 error codes (https://www.jsonrpc.org/specification#error_object).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// rpcRequest is one inbound JSON-RPC message. ID is a pointer so an absent
// "id" (a notification) can be told apart from an explicit "id": null.
type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

// rpcResponse is one outbound JSON-RPC message. Result is only omitted when
// Error is set (and vice versa).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolResult is the MCP tools/call result shape
// (mirrors what FastMCP serialises for a plain Python return value).
type toolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// contentItem is one entry of toolResult.Content.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// server holds the dependencies of one stdio session.
type server struct {
	svc searchsvc.Searcher
	log *log.Logger
}

// RunStdio serves MCP over the given streams until in reaches EOF.
//
// in is read line by line; each non-empty line is one JSON-RPC message. out
// receives only JSON-RPC responses (flushed after each one) — never logs.
// logw receives diagnostics. RunStdio returns nil on clean EOF and an error
// only for transport failures (read/write), never for protocol-level errors,
// which are reported in-band as JSON-RPC error objects.
func RunStdio(s searchsvc.Searcher, in io.Reader, out io.Writer, logw io.Writer) error {
	if s == nil {
		return errors.New("mcpserver: nil searcher")
	}
	if in == nil || out == nil {
		return errors.New("mcpserver: nil input or output stream")
	}
	if logw == nil {
		logw = io.Discard
	}

	srv := &server{svc: s, log: log.New(logw, "ddgs-mcp: ", log.LstdFlags)}
	reader := bufio.NewReader(in)
	writer := bufio.NewWriter(out)

	for {
		line, readErr := reader.ReadBytes('\n')
		// ReadBytes returns the trailing partial line together with io.EOF;
		// handle it before reporting the read result.
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if resp := srv.handleLine(trimmed); resp != nil {
				if err := writeMessage(writer, resp); err != nil {
					return fmt.Errorf("mcpserver: write response: %w", err)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				srv.log.Printf("stdin closed, exiting")
				return nil
			}
			return fmt.Errorf("mcpserver: read input: %w", readErr)
		}
	}
}

// writeMessage marshals one response, appends a newline and flushes so the
// client sees it immediately (stdio transports have no framing other than
// the newline).
func writeMessage(w *bufio.Writer, resp *rpcResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// handleLine parses and dispatches one message. It returns nil when no
// response must be written (notifications).
func (s *server) handleLine(line []byte) *rpcResponse {
	// JSON-RPC batches are valid 2.0 but unused by MCP; answer with an
	// Invalid Request rather than misclassifying it as a parse error.
	if line[0] == '[' {
		return errorResponse(nil, codeInvalidRequest, "Invalid Request: batch requests are not supported")
	}

	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return errorResponse(nil, codeParseError, "Parse error")
	}
	if req.Method == "" {
		return errorResponse(req.ID, codeInvalidRequest, "Invalid Request")
	}

	// JSON-RPC 2.0: a message without "id" is a notification and MUST NOT be
	// answered. The "notifications/" prefix check is defensive: MCP clients
	// never attach an id to notifications/initialized, but if one does we
	// still treat it as a notification.
	if req.ID == nil || strings.HasPrefix(req.Method, "notifications/") {
		s.log.Printf("notification: %s", req.Method)
		return nil
	}

	result, rpcErr := s.dispatch(req.Method, req.Params)
	if rpcErr != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: *req.ID, Error: rpcErr}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: *req.ID, Result: result}
}

// dispatch routes a request by method.
func (s *server) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.initialize(params), nil
	case "ping":
		// Not required by SPEC §5, but part of MCP's base protocol: clients
		// use it as a liveness probe and expect an empty result object.
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": Tools()}, nil
	case "tools/call":
		return s.toolsCall(params)
	default:
		// Includes "notifications/*" sent with an id, "resources/list",
		// "prompts/list" and anything else this server does not implement.
		s.log.Printf("unknown method: %s", method)
		return nil, &rpcError{Code: codeMethodNotFound, Message: "Method not found"}
	}
}

// initialize mirrors the FastMCP handshake payload.
func (s *server) initialize(params json.RawMessage) map[string]any {
	if len(params) > 0 {
		s.log.Printf("initialize params: %s", compact(params))
	}
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ddgs.LibraryVersion(),
		},
	}
}

// toolsCall handles "tools/call". Protocol-level problems (missing name,
// non-object arguments wrapper) are JSON-RPC -32602 errors; anything that
// happens while executing the tool is reported inside the tool result with
// isError:true, which is the MCP convention (and what mcp.py/FastMCP does for
// exceptions raised inside a tool).
func (s *server) toolsCall(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(bytes.TrimSpace(params)) == 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: missing tool name"}
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: missing tool name"}
	}
	s.log.Printf("tools/call: %s", p.Name)
	return s.callTool(p.Name, p.Arguments), nil
}

// callTool validates arguments and executes one tool.
func (s *server) callTool(name string, rawArgs json.RawMessage) toolResult {
	spec, ok := toolSpecByName[name]
	if !ok {
		// Chosen behaviour (SPEC §5/D2 allows either): unknown tool names are
		// reported as an isError tool result, not as -32602. Rationale: the
		// model sees a normal tool failure it can correct, and the wire shape
		// is identical to every other tool failure. FastMCP itself raises
		// -32602 "Unknown tool"; this is a documented, tested deviation.
		return toolErrorf("unknown tool: %q", name)
	}

	args := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(rawArgs); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &args); err != nil {
			return toolErrorf("invalid arguments for %q: %v", name, err)
		}
	}
	for _, req := range spec.required {
		if err := requiredArg(args, req); err != nil {
			return toolErrorf("%q: %v", name, err)
		}
	}

	if spec.category == categoryExtract {
		return s.extractTool(args)
	}
	return s.searchTool(spec, args)
}

// searchTool runs one of the five search tools.
func (s *server) searchTool(spec *toolSpec, args map[string]json.RawMessage) toolResult {
	query, err := requiredString(args, "query")
	if err != nil {
		return toolErrorf("%q: %v", spec.name, err)
	}
	opts, err := searchOptions(spec, args)
	if err != nil {
		return toolErrorf("%q: %v", spec.name, err)
	}

	// RunStdio has no caller-supplied context: a tool call lasts as long as
	// the underlying search (engine timeouts bound it, mirroring Python's
	// asyncio.to_thread around DDGS(...)).
	ctx := context.Background()

	var (
		results []ddgs.Result
		runErr  error
	)
	switch spec.category {
	case categoryText:
		results, runErr = s.svc.Text(ctx, query, opts)
	case categoryImages:
		results, runErr = s.svc.Images(ctx, query, opts)
	case categoryNews:
		results, runErr = s.svc.News(ctx, query, opts)
	case categoryVideos:
		results, runErr = s.svc.Videos(ctx, query, opts)
	case categoryBooks:
		results, runErr = s.svc.Books(ctx, query, opts)
	default:
		return toolErrorf("unknown tool category %q", spec.category)
	}
	if runErr != nil {
		// Includes ddgs.ErrUnsupportedFormat-style failures surfaced by the
		// library: MCP wants them as isError results, not JSON-RPC errors.
		return toolErrorf("%v", runErr)
	}
	if results == nil {
		// Python returns list(results), i.e. [] rather than null.
		results = []ddgs.Result{}
	}
	return jsonToolResult(results)
}

// extractTool runs extract_content, mirroring DDGS.extract's {"url",
// "content"} dict. For fmt="content" the Python dict holds raw response bytes;
// Go has no bytes-in-JSON, so valid UTF-8 is decoded and binary content is
// base64-encoded (documented deviation).
func (s *server) extractTool(args map[string]json.RawMessage) toolResult {
	rawURL, err := requiredString(args, "url")
	if err != nil {
		return toolErrorf("%q: %v", ToolExtractContent, err)
	}
	format, err := stringArg(args, "fmt", string(ddgs.FormatTextMarkdown))
	if err != nil {
		return toolErrorf("%q: %v", ToolExtractContent, err)
	}

	extracted, err := s.svc.Extract(context.Background(), rawURL, ddgs.ExtractFormat(format))
	if err != nil {
		return toolErrorf("%v", err)
	}
	if extracted == nil {
		return toolErrorf("extract returned no content for %q", rawURL)
	}

	content := extracted.Content
	if ddgs.ExtractFormat(format) == ddgs.FormatContent {
		if utf8.Valid(extracted.Bytes) {
			content = string(extracted.Bytes)
		} else {
			content = base64.StdEncoding.EncodeToString(extracted.Bytes)
		}
	}
	outURL := extracted.URL
	if outURL == "" {
		outURL = rawURL
	}
	payload := struct {
		URL     string `json:"url"`
		Content string `json:"content"`
	}{URL: outURL, Content: content}
	return jsonToolResult(payload)
}

// searchOptions builds ddgs.SearchOptions from the arguments, applying the
// Python signature defaults (region "us-en", safesearch "moderate",
// max_results 10, page 1, backend "auto", timelimit None) and forwarding the
// category filters into Extra. books mirrors mcp.py: only max_results, page
// and backend are accepted, so region/safesearch/timelimit keep the library
// defaults.
func searchOptions(spec *toolSpec, args map[string]json.RawMessage) (*ddgs.SearchOptions, error) {
	opts := &ddgs.SearchOptions{}

	if spec.category != categoryBooks {
		region, err := stringArg(args, "region", "us-en")
		if err != nil {
			return nil, err
		}
		safesearch, err := stringArg(args, "safesearch", "moderate")
		if err != nil {
			return nil, err
		}
		timelimit, err := stringArg(args, "timelimit", "")
		if err != nil {
			return nil, err
		}
		opts.Region = region
		opts.SafeSearch = safesearch
		opts.TimeLimit = timelimit
	}

	maxResults, err := intArg(args, "max_results", ddgs.DefaultMaxResults)
	if err != nil {
		return nil, err
	}
	page, err := intArg(args, "page", 1)
	if err != nil {
		return nil, err
	}
	backend, err := stringArg(args, "backend", "auto")
	if err != nil {
		return nil, err
	}
	opts.MaxResults = maxResults
	opts.Page = page
	opts.Backend = backend

	for _, name := range spec.extra {
		value, err := stringArg(args, name, "")
		if err != nil {
			return nil, err
		}
		if value == "" {
			// Python passes None; engines test `if v != ""` / `is not None`.
			continue
		}
		if opts.Extra == nil {
			opts.Extra = make(map[string]string, len(spec.extra))
		}
		opts.Extra[name] = value
	}
	return opts, nil
}

// jsonToolResult wraps an arbitrary marshallable value as a text content
// item, exactly like FastMCP serialising a Python return value.
func jsonToolResult(v any) toolResult {
	data, err := json.Marshal(v)
	if err != nil {
		return toolErrorf("failed to encode result: %v", err)
	}
	return toolResult{Content: []contentItem{{Type: "text", Text: string(data)}}}
}

// toolErrorf builds the MCP error result (isError:true + message).
func toolErrorf(format string, args ...any) toolResult {
	return toolResult{
		Content: []contentItem{{Type: "text", Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

// errorResponse builds a JSON-RPC error response. A nil id becomes JSON null
// (JSON-RPC requires "id": null when the request id cannot be determined).
func errorResponse(id *json.RawMessage, code int, message string) *rpcResponse {
	raw := json.RawMessage("null")
	if id != nil {
		raw = *id
	}
	return &rpcResponse{JSONRPC: "2.0", ID: raw, Error: &rpcError{Code: code, Message: message}}
}

// --- argument decoding -----------------------------------------------------

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// requiredArg reports a missing or null required argument.
func requiredArg(args map[string]json.RawMessage, name string) error {
	raw, ok := args[name]
	if !ok || isJSONNull(raw) {
		return fmt.Errorf("missing required argument %q", name)
	}
	return nil
}

// requiredString returns a required string argument.
func requiredString(args map[string]json.RawMessage, name string) (string, error) {
	if err := requiredArg(args, name); err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(args[name], &s); err != nil {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	return s, nil
}

// stringArg returns an optional string argument, falling back to def when the
// key is absent or null (Python's default behaviour).
func stringArg(args map[string]json.RawMessage, name, def string) (string, error) {
	raw, ok := args[name]
	if !ok || isJSONNull(raw) {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	return s, nil
}

// intArg returns an optional integer argument. JSON numbers are read as
// json.Number so large values do not lose precision; integral floats (10.0)
// are accepted, matching the leniency of Pydantic's lax mode.
func intArg(args map[string]json.RawMessage, name string, def int) (int, error) {
	raw, ok := args[name]
	if !ok || isJSONNull(raw) {
		return def, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var num json.Number
	if err := dec.Decode(&num); err != nil {
		return 0, fmt.Errorf("argument %q must be an integer", name)
	}
	i, err := num.Int64()
	if err != nil {
		f, ferr := num.Float64()
		if ferr != nil || f != math.Trunc(f) {
			return 0, fmt.Errorf("argument %q must be an integer", name)
		}
		i = int64(f)
	}
	return int(i), nil
}

// compact trims a raw JSON payload for logging.
func compact(raw json.RawMessage) string {
	const max = 512
	s := string(bytes.TrimSpace(raw))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
