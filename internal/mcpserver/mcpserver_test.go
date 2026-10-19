package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

// stubSearcher records the last call and returns canned data, so the whole
// test suite runs without touching the network.
type stubSearcher struct {
	textResults  []ddgs.Result
	imageResults []ddgs.Result
	newsResults  []ddgs.Result
	videoResults []ddgs.Result
	bookResults  []ddgs.Result
	extracted    *ddgs.Extracted
	err          error

	lastCategory string
	lastQuery    string
	lastOpts     *ddgs.SearchOptions
	lastURL      string
	lastFormat   ddgs.ExtractFormat
	calls        int
}

func (s *stubSearcher) record(category, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	s.calls++
	s.lastCategory, s.lastQuery, s.lastOpts = category, query, opts
	if s.err != nil {
		return nil, s.err
	}
	switch category {
	case categoryText:
		return s.textResults, nil
	case categoryImages:
		return s.imageResults, nil
	case categoryNews:
		return s.newsResults, nil
	case categoryVideos:
		return s.videoResults, nil
	case categoryBooks:
		return s.bookResults, nil
	default:
		return nil, fmt.Errorf("unexpected category %q", category)
	}
}

func (s *stubSearcher) Text(_ context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	return s.record(categoryText, query, opts)
}

func (s *stubSearcher) Images(_ context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	return s.record(categoryImages, query, opts)
}

func (s *stubSearcher) News(_ context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	return s.record(categoryNews, query, opts)
}

func (s *stubSearcher) Videos(_ context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	return s.record(categoryVideos, query, opts)
}

func (s *stubSearcher) Books(_ context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	return s.record(categoryBooks, query, opts)
}

func (s *stubSearcher) Extract(_ context.Context, rawURL string, format ddgs.ExtractFormat) (*ddgs.Extracted, error) {
	s.calls++
	s.lastCategory, s.lastURL, s.lastFormat = categoryExtract, rawURL, format
	if s.err != nil {
		return nil, s.err
	}
	return s.extracted, nil
}

var _ searchsvc.Searcher = (*stubSearcher)(nil)

// rpcMsg is a decoded JSON-RPC response.
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// runServer feeds input to RunStdio and returns stdout and stderr/log output.
func runServer(t *testing.T, s searchsvc.Searcher, input string) (string, string) {
	t.Helper()
	var out, logBuf bytes.Buffer
	if err := RunStdio(s, strings.NewReader(input), &out, &logBuf); err != nil {
		t.Fatalf("RunStdio: %v", err)
	}
	return out.String(), logBuf.String()
}

// parseResponses splits newline-delimited JSON responses.
func parseResponses(t *testing.T, out string) []rpcMsg {
	t.Helper()
	var msgs []rpcMsg
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m rpcMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response line is not valid JSON: %q: %v", line, err)
		}
		if m.JSONRPC != "2.0" {
			t.Errorf("response jsonrpc = %q, want %q", m.JSONRPC, "2.0")
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// request renders one JSON-RPC request line. params is raw JSON or "".
func request(id int, method, params string) string {
	if params == "" {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, method)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
}

// decodeResult unmarshals a response result into v.
func decodeResult(t *testing.T, m rpcMsg, v any) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", *m.Error)
	}
	if err := json.Unmarshal(m.Result, v); err != nil {
		t.Fatalf("unmarshal result %s: %v", m.Result, err)
	}
}

func TestInitializeHandshake(t *testing.T) {
	stub := &stubSearcher{}
	out, _ := runServer(t, stub, request(1, "initialize",
		`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`)+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}
	if string(msgs[0].ID) != "1" {
		t.Errorf("response id = %s, want 1", msgs[0].ID)
	}

	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	decodeResult(t, msgs[0], &res)

	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, ProtocolVersion)
	}
	if res.ServerInfo.Name != "ddgs-go" {
		t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, "ddgs-go")
	}
	if res.ServerInfo.Version != ddgs.LibraryVersion() {
		t.Errorf("serverInfo.version = %q, want %q", res.ServerInfo.Version, ddgs.LibraryVersion())
	}
	if res.Capabilities.Tools == nil {
		t.Errorf("capabilities.tools missing, got %+v", res.Capabilities)
	}
	if stub.calls != 0 {
		t.Errorf("initialize must not run a search, calls = %d", stub.calls)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	// A notification alone must produce zero bytes on stdout.
	out, _ := runServer(t, &stubSearcher{}, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	if out != "" {
		t.Fatalf("notification produced output: %q", out)
	}

	// …and the session keeps working afterwards.
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		request(2, "tools/list", "") + "\n"
	out, _ = runServer(t, &stubSearcher{}, input)
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1 (notification must be silent)", len(msgs))
	}
	if string(msgs[0].ID) != "2" {
		t.Errorf("response id = %s, want 2", msgs[0].ID)
	}
}

func TestToolsCallSearchText(t *testing.T) {
	fixed := []ddgs.Result{
		{"title": "Go", "href": "https://go.dev/", "body": "The Go programming language"},
		{"title": "Docs", "href": "https://go.dev/doc/", "body": "Documentation"},
	}
	stub := &stubSearcher{textResults: fixed}
	args := `{"query":"golang","region":"uk-en","safesearch":"off","timelimit":"w","max_results":2,"page":3,"backend":"duckduckgo"}`
	out, _ := runServer(t, stub, request(3, "tools/call",
		fmt.Sprintf(`{"name":"search_text","arguments":%s}`, args))+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}

	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if res.IsError {
		t.Fatalf("isError = true, content = %+v", res.Content)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one {type:text}", res.Content)
	}

	var got []ddgs.Result
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("content text is not a JSON result array: %v (%q)", err, res.Content[0].Text)
	}
	if len(got) != len(fixed) {
		t.Fatalf("got %d results, want %d", len(got), len(fixed))
	}
	for i := range fixed {
		for k, v := range fixed[i] {
			if got[i][k] != v {
				t.Errorf("result[%d][%q] = %v, want %v", i, k, got[i][k], v)
			}
		}
	}

	if stub.lastCategory != categoryText || stub.lastQuery != "golang" {
		t.Errorf("search call = (%q, %q), want (text, golang)", stub.lastCategory, stub.lastQuery)
	}
	opts := stub.lastOpts
	if opts == nil {
		t.Fatal("nil SearchOptions")
	}
	if opts.Region != "uk-en" || opts.SafeSearch != "off" || opts.TimeLimit != "w" ||
		opts.MaxResults != 2 || opts.Page != 3 || opts.Backend != "duckduckgo" {
		t.Errorf("options = %+v", opts)
	}
	if len(opts.Extra) != 0 {
		t.Errorf("Extra = %v, want empty for text search", opts.Extra)
	}
}

func TestToolsCallSearchDefaults(t *testing.T) {
	stub := &stubSearcher{}
	out, _ := runServer(t, stub, request(4, "tools/call",
		`{"name":"search_text","arguments":{"query":"x"}}`)+"\n")
	msgs := parseResponses(t, out)
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
	if strings.TrimSpace(res.Content[0].Text) != "[]" {
		t.Errorf("nil results must encode as [], got %q", res.Content[0].Text)
	}

	opts := stub.lastOpts
	if opts == nil {
		t.Fatal("nil SearchOptions")
	}
	want := ddgs.SearchOptions{
		Region:     "us-en",
		SafeSearch: "moderate",
		MaxResults: 10,
		Page:       1,
		Backend:    "auto",
	}
	if opts.Region != want.Region || opts.SafeSearch != want.SafeSearch ||
		opts.TimeLimit != "" || opts.MaxResults != want.MaxResults ||
		opts.Page != want.Page || opts.Backend != want.Backend {
		t.Errorf("default options = %+v, want %+v", *opts, want)
	}
}

func TestToolsCallImagesAndVideosExtra(t *testing.T) {
	tests := []struct {
		tool     string
		args     string
		category string
		want     map[string]string
	}{
		{
			tool:     "search_images",
			args:     `{"query":"cats","size":"Large","color":"blue","layout":"Wide"}`,
			category: categoryImages,
			want:     map[string]string{"size": "Large", "color": "blue", "layout": "Wide"},
		},
		{
			tool:     "search_videos",
			args:     `{"query":"cats","resolution":"high","duration":"short","license_videos":"youtube"}`,
			category: categoryVideos,
			want:     map[string]string{"resolution": "high", "duration": "short", "license_videos": "youtube"},
		},
		{
			// Explicit nulls / empty strings are dropped, mirroring Python's
			// None defaults.
			tool:     "search_images",
			args:     `{"query":"cats","size":null,"color":""}`,
			category: categoryImages,
			want:     map[string]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			stub := &stubSearcher{}
			out, _ := runServer(t, stub, request(5, "tools/call",
				fmt.Sprintf(`{"name":%q,"arguments":%s}`, tc.tool, tc.args))+"\n")
			msgs := parseResponses(t, out)
			if stub.lastCategory != tc.category {
				t.Fatalf("category = %q, want %q", stub.lastCategory, tc.category)
			}
			if got := stub.lastOpts.Extra; len(got) != len(tc.want) {
				t.Fatalf("Extra = %v, want %v", got, tc.want)
			} else {
				for k, v := range tc.want {
					if got[k] != v {
						t.Errorf("Extra[%q] = %q, want %q", k, got[k], v)
					}
				}
			}
			var res struct {
				IsError bool `json:"isError"`
			}
			decodeResult(t, msgs[0], &res)
			if res.IsError {
				t.Errorf("isError = true for %s", tc.tool)
			}
		})
	}
}

func TestToolsCallBooksOptions(t *testing.T) {
	stub := &stubSearcher{bookResults: []ddgs.Result{{"title": "book"}}}
	out, _ := runServer(t, stub, request(6, "tools/call",
		`{"name":"search_books","arguments":{"query":"go","max_results":5,"page":2,"backend":"annasarchive"}}`)+"\n")
	msgs := parseResponses(t, out)

	var res struct {
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if res.IsError {
		t.Fatalf("unexpected isError for books")
	}
	if stub.lastCategory != categoryBooks || stub.lastQuery != "go" {
		t.Errorf("call = (%q, %q)", stub.lastCategory, stub.lastQuery)
	}
	opts := stub.lastOpts
	if opts.MaxResults != 5 || opts.Page != 2 || opts.Backend != "annasarchive" {
		t.Errorf("options = %+v", opts)
	}
	// mcp.py's search_books has no region/safesearch/timelimit: the library
	// defaults must apply, i.e. the fields stay empty here.
	if opts.Region != "" || opts.SafeSearch != "" || opts.TimeLimit != "" {
		t.Errorf("books options must not carry region/safesearch/timelimit: %+v", opts)
	}
}

func TestToolsCallExtractContent(t *testing.T) {
	stub := &stubSearcher{extracted: &ddgs.Extracted{
		URL:     "https://example.com/",
		Content: "# Example",
	}}
	out, _ := runServer(t, stub, request(7, "tools/call",
		`{"name":"extract_content","arguments":{"url":"https://example.com/"}}`)+"\n")
	msgs := parseResponses(t, out)

	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if res.IsError {
		t.Fatalf("isError = true: %+v", res.Content)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("content = %+v", res.Content)
	}
	var payload struct {
		URL     string `json:"url"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
		t.Fatalf("content text is not an object: %v (%q)", err, res.Content[0].Text)
	}
	if payload.URL != "https://example.com/" || payload.Content != "# Example" {
		t.Errorf("payload = %+v", payload)
	}
	if stub.lastURL != "https://example.com/" || stub.lastFormat != ddgs.FormatTextMarkdown {
		t.Errorf("extract call = (%q, %q), want (https://example.com/, text_markdown)",
			stub.lastURL, stub.lastFormat)
	}
}

func TestToolsCallExtractContentBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		want  string
	}{
		{name: "utf8 decoded", bytes: []byte("<html>ok</html>"), want: "<html>ok</html>"},
		{name: "binary base64", bytes: []byte{0xff, 0xfe, 0x00}, want: "//4A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubSearcher{extracted: &ddgs.Extracted{
				URL:   "https://example.com/raw",
				Bytes: tc.bytes,
			}}
			out, _ := runServer(t, stub, request(8, "tools/call",
				`{"name":"extract_content","arguments":{"url":"https://example.com/raw","fmt":"content"}}`)+"\n")
			msgs := parseResponses(t, out)
			var res struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			decodeResult(t, msgs[0], &res)
			if res.IsError {
				t.Fatalf("isError = true: %+v", res)
			}
			var payload struct {
				URL     string `json:"url"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.Content != tc.want {
				t.Errorf("content = %q, want %q", payload.Content, tc.want)
			}
			if stub.lastFormat != ddgs.FormatContent {
				t.Errorf("format = %q, want content", stub.lastFormat)
			}
		})
	}
}

// TestExtractTextRichUnsupported exercises the real library (no network: the
// Go port rejects text_rich before any HTTP request, SPEC §9.7) to prove the
// mcpserver surfaces ErrUnsupportedFormat as an isError result.
func TestExtractTextRichUnsupported(t *testing.T) {
	instance, err := ddgs.New(nil)
	if err != nil {
		t.Fatalf("ddgs.New: %v", err)
	}
	out, _ := runServer(t, searchsvc.New(instance), request(9, "tools/call",
		`{"name":"extract_content","arguments":{"url":"https://example.com/","fmt":"text_rich"}}`)+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if !res.IsError {
		t.Fatalf("text_rich must be an error result, got %+v", res)
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "unsupported format") {
		t.Errorf("error text = %+v, want ErrUnsupportedFormat message", res.Content)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	out, _ := runServer(t, &stubSearcher{}, request(10, "tools/call",
		`{"name":"search_nope","arguments":{}}`)+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 || msgs[0].Error != nil {
		t.Fatalf("unknown tool must be an isError result, got %+v", msgs)
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if !res.IsError {
		t.Errorf("isError = false, want true")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "unknown tool") {
		t.Errorf("content = %+v, want unknown-tool message", res.Content)
	}
}

func TestToolsCallMissingRequiredArgument(t *testing.T) {
	out, _ := runServer(t, &stubSearcher{}, request(11, "tools/call",
		`{"name":"search_text","arguments":{"region":"us-en"}}`)+"\n")
	msgs := parseResponses(t, out)
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "query") {
		t.Errorf("result = %+v, want isError mentioning query", res)
	}
}

func TestToolsCallSearchError(t *testing.T) {
	stub := &stubSearcher{err: errors.New("boom")}
	out, _ := runServer(t, stub, request(12, "tools/call",
		`{"name":"search_news","arguments":{"query":"x"}}`)+"\n")
	msgs := parseResponses(t, out)
	if msgs[0].Error != nil {
		t.Fatalf("tool failure must not be a JSON-RPC error: %+v", msgs[0].Error)
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	decodeResult(t, msgs[0], &res)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "boom") {
		t.Errorf("result = %+v, want isError with boom", res)
	}
}

func TestUnknownMethod(t *testing.T) {
	out, _ := runServer(t, &stubSearcher{}, request(13, "resources/list", "")+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}
	if msgs[0].Error == nil {
		t.Fatalf("expected JSON-RPC error, got result %s", msgs[0].Result)
	}
	if msgs[0].Error.Code != -32601 {
		t.Errorf("code = %d, want -32601", msgs[0].Error.Code)
	}
	if string(msgs[0].ID) != "13" {
		t.Errorf("id = %s, want 13", msgs[0].ID)
	}
}

func TestMalformedLine(t *testing.T) {
	out, _ := runServer(t, &stubSearcher{}, "this is not json\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 || msgs[0].Error == nil {
		t.Fatalf("expected one parse-error response, got %+v", msgs)
	}
	if msgs[0].Error.Code != -32700 {
		t.Errorf("code = %d, want -32700", msgs[0].Error.Code)
	}
	if string(msgs[0].ID) != "null" {
		t.Errorf("id = %s, want null", msgs[0].ID)
	}
}

func TestPingAndPartialLineAtEOF(t *testing.T) {
	// No trailing newline: the final message must still be handled.
	out, _ := runServer(t, &stubSearcher{}, `{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}
	if string(msgs[0].ID) != `"abc"` {
		t.Errorf("id = %s, want \"abc\"", msgs[0].ID)
	}
	if string(msgs[0].Result) != "{}" {
		t.Errorf("ping result = %s, want {}", msgs[0].Result)
	}
}

func TestInvalidRequestWithoutID(t *testing.T) {
	// A malformed request (no method) that is not a notification is answered
	// with id:null, per JSON-RPC.
	out, _ := runServer(t, &stubSearcher{}, `{"jsonrpc":"2.0","id":null,"params":{}}`+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 || msgs[0].Error == nil {
		t.Fatalf("expected one error response, got %+v", msgs)
	}
	if msgs[0].Error.Code != -32600 {
		t.Errorf("code = %d, want -32600", msgs[0].Error.Code)
	}
	if string(msgs[0].ID) != "null" {
		t.Errorf("id = %s, want null", msgs[0].ID)
	}
}

func TestToolsCallInvalidParams(t *testing.T) {
	out, _ := runServer(t, &stubSearcher{}, request(14, "tools/call", `{"arguments":{}}`)+"\n")
	msgs := parseResponses(t, out)
	if msgs[0].Error == nil || msgs[0].Error.Code != -32602 {
		t.Fatalf("expected -32602, got %+v", msgs)
	}
}
