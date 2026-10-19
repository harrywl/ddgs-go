package apiserver

// Unit tests for the api.py port. No network access: every search goes to a
// stub searchsvc.Searcher that records the call and replays canned results or
// errors. Assertions check the JSON field names against the Pydantic models in
// py-ddgs/ddgs/api_server/api.py.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

// ------------------------------------------------------------------- stub ---

// recordedCall captures one Searcher invocation.
type recordedCall struct {
	Method string
	Query  string
	Opts   *ddgs.SearchOptions
	// Deadline, if any, on the request context handed to the Searcher.
	HadDeadline bool
	Timeout     time.Duration
}

// stubSearcher implements searchsvc.Searcher for tests.
type stubSearcher struct {
	tb    *testing.T
	calls []recordedCall

	// results replays the canned result set (any category).
	results []ddgs.Result
	// err is returned instead of results when non-nil.
	err error
	// extractResp is returned by Extract.
	extractResp *ddgs.Extracted
	// block, when non-nil, makes the search wait for it (context tests).
	block <-chan struct{}
}

var _ searchsvc.Searcher = (*stubSearcher)(nil)

func newStub(t *testing.T) *stubSearcher { return &stubSearcher{tb: t} }

func (s *stubSearcher) record(method string, ctx context.Context, query string, opts *ddgs.SearchOptions) error {
	s.tb.Helper()
	call := recordedCall{Method: method, Query: query, Opts: opts}
	if deadline, ok := ctx.Deadline(); ok {
		call.HadDeadline = true
		call.Timeout = time.Until(deadline)
	}
	s.calls = append(s.calls, call)
	if s.block != nil {
		// Emulate an engine that honours the request context: either the test
		// releases us, or the handler's deadline fires first.
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *stubSearcher) Text(ctx context.Context, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
	if err := s.record("text", ctx, q, o); err != nil {
		return nil, err
	}
	return s.results, s.err
}

func (s *stubSearcher) Images(ctx context.Context, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
	if err := s.record("images", ctx, q, o); err != nil {
		return nil, err
	}
	return s.results, s.err
}

func (s *stubSearcher) News(ctx context.Context, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
	if err := s.record("news", ctx, q, o); err != nil {
		return nil, err
	}
	return s.results, s.err
}

func (s *stubSearcher) Videos(ctx context.Context, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
	if err := s.record("videos", ctx, q, o); err != nil {
		return nil, err
	}
	return s.results, s.err
}

func (s *stubSearcher) Books(ctx context.Context, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
	if err := s.record("books", ctx, q, o); err != nil {
		return nil, err
	}
	return s.results, s.err
}

func (s *stubSearcher) Extract(ctx context.Context, rawURL string, format ddgs.ExtractFormat) (*ddgs.Extracted, error) {
	if err := s.record("extract:"+string(format), ctx, rawURL, nil); err != nil {
		return nil, err
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.extractResp != nil {
		return s.extractResp, nil
	}
	return &ddgs.Extracted{URL: rawURL, Content: "# heading"}, nil
}

// sample results, one per category, using the exact dict keys the Python
// engines produce (see ddgs/results.py).
var sampleResults = map[string][]ddgs.Result{
	"text": {{
		"title": "Go (programming language)",
		"href":  "https://en.wikipedia.org/wiki/Go_(programming_language)",
		"body":  "Go is a statically typed programming language.",
	}},
	"images": {{
		"title":     "golang",
		"image":     "https://example.com/a.png",
		"thumbnail": "https://example.com/a_thumb.png",
		"url":       "https://example.com/page",
		"height":    720,
		"width":     1280,
		"source":    "example.com",
	}},
	"news": {{
		"date":   "2 hours ago",
		"title":  "Go 1.27 released",
		"body":   "The Go team shipped a new release.",
		"url":    "https://example.com/news",
		"image":  "https://example.com/n.png",
		"source": "Example News",
	}},
	"videos": {{
		"title":       "Go tutorial",
		"content":     "https://example.com/v",
		"description": "Intro to Go",
		"duration":    "10:00",
		"embed_html":  "<iframe></iframe>",
		"embed_url":   "https://example.com/embed",
		"image_token": "abc",
		"images":      map[string]any{"medium": "https://example.com/m.jpg"},
		"provider":    "youtube",
		"published":   "2026-01-01",
		"publisher":   "ExampleU",
		"statistics":  map[string]any{"views": 1000},
		"uploader":    "ExampleU",
	}},
	"books": {{
		"title":     "The Go Programming Language",
		"author":    "Donovan & Kernighan",
		"publisher": "Addison-Weslong",
		"info":      "book",
		"url":       "https://example.com/book",
		"thumbnail": "https://example.com/cover.jpg",
	}},
}

// ------------------------------------------------------------------ utils ---

func doRequest(t *testing.T, h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out
}

func assertJSONKeys(t *testing.T, obj map[string]any, want ...string) {
	t.Helper()
	if len(obj) != len(want) {
		t.Errorf("unexpected field count: got %d (%v), want %d %v", len(obj), keysOf(obj), len(want), want)
	}
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing JSON field %q in %v", k, keysOf(obj))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// healthHandler builds a handler with a stub searcher and quiet defaults.
func healthHandler(t *testing.T, s searchsvc.Searcher) http.Handler { return New(s) }

// ------------------------------------------------------------ / and /health ---

func TestHealthEndpoints(t *testing.T) {
	h := healthHandler(t, newStub(t))
	for _, path := range []string{"/", "/health"} {
		rec := doRequest(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q, want application/json", path, ct)
		}
		body := decodeJSONBody(t, rec)
		// api.py HealthResponse(status, version, service).
		assertJSONKeys(t, body, "status", "version", "service")
		for k, want := range map[string]string{"status": "healthy", "version": "1.0.0", "service": "DDGS API"} {
			if got := body[k]; got != want {
				t.Errorf("%s: %s = %v, want %q", path, k, got, want)
			}
		}
	}
}

func TestHealthRejectsPost(t *testing.T) {
	h := healthHandler(t, newStub(t))
	rec := doRequest(t, h, http.MethodPost, "/health", strings.NewReader(`{}`))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health: status %d, want 405", rec.Code)
	}
	if got := decodeJSONBody(t, rec)["detail"]; got != "Method Not Allowed" {
		t.Errorf("detail = %v, want %q", got, "Method Not Allowed")
	}
}

// --------------------------------------------------------- happy-path search ---

// searchSpecs describes, per category, the GET/POST request forms and the
// result dict keys api.py's Pydantic models / Python engines produce.
type searchSpec struct {
	category string
	// getQuery is a GET query string exercising every documented parameter.
	getQuery string
	// postBody is the JSON body mirroring the request model.
	postBody string
	// resultKeys are the dict keys of one result (api.py returns raw dicts).
	resultKeys []string
}

func searchSpecs() []searchSpec {
	return []searchSpec{
		{
			category:   "text",
			getQuery:   "?query=golang&region=uk-en&safesearch=off&timelimit=w&max_results=7&page=2&backend=duckduckgo",
			postBody:   `{"query":"golang","region":"uk-en","safesearch":"off","timelimit":"w","max_results":7,"page":2,"backend":"duckduckgo"}`,
			resultKeys: []string{"title", "href", "body"},
		},
		{
			category:   "images",
			getQuery:   "?query=golang&region=de-de&safesearch=on&timelimit=m&max_results=5&page=1&backend=auto&size=Large&color=Blue&type_image=photo&layout=Wide&license_image=Public",
			postBody:   `{"query":"golang","region":"de-de","safesearch":"on","timelimit":"m","max_results":5,"page":1,"backend":"auto","size":"Large","color":"Blue","type_image":"photo","layout":"Wide","license_image":"Public"}`,
			resultKeys: []string{"title", "image", "thumbnail", "url", "height", "width", "source"},
		},
		{
			category:   "news",
			getQuery:   "?query=golang&region=fr-fr&safesearch=moderate&timelimit=d&max_results=3&page=1&backend=bing_news",
			postBody:   `{"query":"golang","region":"fr-fr","safesearch":"moderate","timelimit":"d","max_results":3,"page":1,"backend":"bing_news"}`,
			resultKeys: []string{"date", "title", "body", "url", "image", "source"},
		},
		{
			category: "videos",
			getQuery: "?query=golang&region=us-en&safesearch=off&timelimit=y&max_results=4&page=3&backend=auto&resolution=high&duration=short&license_videos=creativeCommon",
			postBody: `{"query":"golang","region":"us-en","safesearch":"off","timelimit":"y","max_results":4,"page":3,"backend":"auto","resolution":"high","duration":"short","license_videos":"creativeCommon"}`,
			resultKeys: []string{"title", "content", "description", "duration", "embed_html", "embed_url",
				"image_token", "images", "provider", "published", "publisher", "statistics", "uploader"},
		},
		{
			// BooksSearchRequest has only query/max_results/page/backend.
			category:   "books",
			getQuery:   "?query=golang&max_results=2&page=1&backend=auto",
			postBody:   `{"query":"golang","max_results":2,"page":1,"backend":"auto"}`,
			resultKeys: []string{"title", "author", "publisher", "info", "url", "thumbnail"},
		},
	}
}

func TestSearchHappyPathPost(t *testing.T) {
	for _, spec := range searchSpecs() {
		t.Run(spec.category, func(t *testing.T) {
			stub := newStub(t)
			stub.results = sampleResults[spec.category]
			h := New(stub)

			rec := doRequest(t, h, http.MethodPost, "/search/"+spec.category, strings.NewReader(spec.postBody))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s, want 200", rec.Code, rec.Body.String())
			}
			body := decodeJSONBody(t, rec)
			// api.py SearchResponse: exactly one field, results.
			assertJSONKeys(t, body, "results")
			list, ok := body["results"].([]any)
			if !ok || len(list) != 1 {
				t.Fatalf("results = %v, want 1 item", body["results"])
			}
			first := list[0].(map[string]any)
			assertJSONKeys(t, first, spec.resultKeys...)

			call := wantOneCall(t, stub)
			if call.Method != spec.category {
				t.Errorf("Searcher method = %q, want %q", call.Method, spec.category)
			}
			if call.Query != "golang" {
				t.Errorf("query = %q", call.Query)
			}
			assertFullOptions(t, call.Opts, spec.category)
			if !call.HadDeadline || call.Timeout < 9*time.Second || call.Timeout > 10*time.Second {
				t.Errorf("context deadline = %v (set=%v), want ~10s (library 5s + 5s slack)", call.Timeout, call.HadDeadline)
			}
		})
	}
}

func TestSearchHappyPathGet(t *testing.T) {
	for _, spec := range searchSpecs() {
		t.Run(spec.category, func(t *testing.T) {
			stub := newStub(t)
			stub.results = sampleResults[spec.category]
			h := New(stub)

			rec := doRequest(t, h, http.MethodGet, "/search/"+spec.category+spec.getQuery, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s, want 200", rec.Code, rec.Body.String())
			}
			body := decodeJSONBody(t, rec)
			assertJSONKeys(t, body, "results")
			if list := body["results"].([]any); len(list) != 1 {
				t.Fatalf("results = %v, want 1 item", body["results"])
			}
			call := wantOneCall(t, stub)
			if call.Method != spec.category {
				t.Errorf("Searcher method = %q, want %q", call.Method, spec.category)
			}
			assertFullOptions(t, call.Opts, spec.category)
		})
	}
}

// assertFullOptions checks that every request parameter reached SearchOptions,
// including the per-category filters routed through Extra.
func assertFullOptions(t *testing.T, opts *ddgs.SearchOptions, category string) {
	t.Helper()
	if opts == nil {
		t.Fatal("SearchOptions is nil")
	}
	wantRegion, wantSafe, wantBackend := "uk-en", "off", "duckduckgo"
	wantMax, wantPage := 7, 2
	switch category {
	case "images":
		wantRegion, wantSafe, wantBackend, wantMax, wantPage = "de-de", "on", "auto", 5, 1
	case "news":
		wantRegion, wantSafe, wantBackend, wantMax, wantPage = "fr-fr", "moderate", "bing_news", 3, 1
	case "videos":
		wantRegion, wantSafe, wantBackend, wantMax, wantPage = "us-en", "off", "auto", 4, 3
	case "books":
		// api.py's books models carry no region/safesearch/timelimit.
		wantRegion, wantSafe, wantBackend, wantMax, wantPage = "", "", "auto", 2, 1
	}
	if opts.Region != wantRegion {
		t.Errorf("Region = %q, want %q", opts.Region, wantRegion)
	}
	if opts.SafeSearch != wantSafe {
		t.Errorf("SafeSearch = %q, want %q", opts.SafeSearch, wantSafe)
	}
	if opts.Backend != wantBackend {
		t.Errorf("Backend = %q, want %q", opts.Backend, wantBackend)
	}
	if opts.MaxResults != wantMax {
		t.Errorf("MaxResults = %d, want %d", opts.MaxResults, wantMax)
	}
	if opts.Page != wantPage {
		t.Errorf("Page = %d, want %d", opts.Page, wantPage)
	}
	if category == "books" {
		if opts.TimeLimit != "" {
			t.Errorf("books TimeLimit = %q, want empty", opts.TimeLimit)
		}
		return
	}
	wantTL := map[string]string{"images": "m", "news": "d", "videos": "y", "text": "w"}[category]
	if opts.TimeLimit != wantTL {
		t.Errorf("TimeLimit = %q, want %q", opts.TimeLimit, wantTL)
	}
	switch category {
	case "images":
		assertExtra(t, opts.Extra, map[string]string{
			"size": "Large", "color": "Blue", "type_image": "photo",
			"layout": "Wide", "license_image": "Public",
		})
	case "videos":
		assertExtra(t, opts.Extra, map[string]string{
			"resolution": "high", "duration": "short", "license_videos": "creativeCommon",
		})
	default:
		if len(opts.Extra) != 0 {
			t.Errorf("Extra = %v, want empty for %s", opts.Extra, category)
		}
	}
}

func assertExtra(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Extra = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Extra[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func wantOneCall(t *testing.T, s *stubSearcher) recordedCall {
	t.Helper()
	if len(s.calls) != 1 {
		t.Fatalf("Searcher call count = %d (%v), want 1", len(s.calls), s.calls)
	}
	return s.calls[0]
}

// -------------------------------------------------------------- defaults ----

func TestDefaultsMatchPydanticFields(t *testing.T) {
	cases := []struct {
		category string
		body     string
	}{
		{"text", `{"query":"go"}`},
		{"images", `{"query":"go"}`},
		{"news", `{"query":"go"}`},
		{"videos", `{"query":"go"}`},
		{"books", `{"query":"go"}`},
	}
	for _, c := range cases {
		t.Run(c.category, func(t *testing.T) {
			stub := newStub(t)
			stub.results = []ddgs.Result{}
			h := New(stub)
			rec := doRequest(t, h, http.MethodPost, "/search/"+c.category, strings.NewReader(c.body))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			opts := wantOneCall(t, stub).Opts
			wantMax, wantPage, wantBackend := 10, 1, "auto"
			if opts.MaxResults != wantMax || opts.Page != wantPage || opts.Backend != wantBackend {
				t.Errorf("max_results/page/backend = %d/%d/%q, want %d/%d/%q",
					opts.MaxResults, opts.Page, opts.Backend, wantMax, wantPage, wantBackend)
			}
			if c.category == "books" {
				return
			}
			if opts.Region != "us-en" || opts.SafeSearch != "moderate" {
				t.Errorf("region/safesearch = %q/%q, want us-en/moderate", opts.Region, opts.SafeSearch)
			}
			if opts.TimeLimit != "" {
				t.Errorf("timelimit = %q, want empty (Python None)", opts.TimeLimit)
			}
		})
	}
}

func TestGetDefaultsMatchFastAPIQueryDefaults(t *testing.T) {
	stub := newStub(t)
	h := New(stub)
	rec := doRequest(t, h, http.MethodGet, "/search/text?query=go", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	opts := wantOneCall(t, stub).Opts
	if opts.Region != "us-en" || opts.SafeSearch != "moderate" || opts.MaxResults != 10 || opts.Page != 1 || opts.Backend != "auto" {
		t.Errorf("unexpected defaults: %+v", opts)
	}
}

// max_results tri-state: omitted=10, null=-1 (unlimited), 0=-1, positive=itself.
func TestMaxResultsSemantics(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"omitted", `{"query":"go"}`, 10},
		{"null", `{"query":"go","max_results":null}`, -1},
		{"zero", `{"query":"go","max_results":0}`, -1},
		{"negative", `{"query":"go","max_results":-5}`, -1},
		{"positive", `{"query":"go","max_results":25}`, 25},
		{"string-lax", `{"query":"go","max_results":"12"}`, 12},
		{"float-integral", `{"query":"go","max_results":12.0}`, 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStub(t)
			h := New(stub)
			rec := doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(c.body))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if got := wantOneCall(t, stub).Opts.MaxResults; got != c.want {
				t.Errorf("MaxResults = %d, want %d", got, c.want)
			}
		})
	}
}

func TestResultsIsAlwaysAList(t *testing.T) {
	stub := newStub(t) // results == nil
	h := New(stub)
	rec := doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(`{"query":"go"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"results":[]}` {
		t.Errorf("body = %s, want {\"results\":[]}", got)
	}
}

// ---------------------------------------------------------------- errors ----

func TestEmptyQueryIsBadRequest(t *testing.T) {
	for _, category := range []string{"text", "images", "news", "videos", "books"} {
		t.Run(category+"/post-missing", func(t *testing.T) {
			h := New(newStub(t))
			rec := doRequest(t, h, http.MethodPost, "/search/"+category, strings.NewReader(`{}`))
			assertDetail(t, rec, http.StatusBadRequest, "query is mandatory")
		})
		t.Run(category+"/post-empty", func(t *testing.T) {
			h := New(newStub(t))
			rec := doRequest(t, h, http.MethodPost, "/search/"+category, strings.NewReader(`{"query":"  "}`))
			assertDetail(t, rec, http.StatusBadRequest, "query is mandatory")
		})
		t.Run(category+"/get-missing", func(t *testing.T) {
			h := New(newStub(t))
			rec := doRequest(t, h, http.MethodGet, "/search/"+category, nil)
			assertDetail(t, rec, http.StatusBadRequest, "query is mandatory")
		})
		t.Run(category+"/get-empty", func(t *testing.T) {
			h := New(newStub(t))
			rec := doRequest(t, h, http.MethodGet, "/search/"+category+"?query=", nil)
			assertDetail(t, rec, http.StatusBadRequest, "query is mandatory")
		})
		if len(newStub(t).calls) != 0 {
			t.Fatal("stub sanity")
		}
	}
	// The Searcher must not be called for a rejected request.
	stub := newStub(t)
	h := New(stub)
	doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(`{"query":""}`))
	doRequest(t, h, http.MethodGet, "/search/text", nil)
	if len(stub.calls) != 0 {
		t.Errorf("Searcher called %d times for empty query, want 0", len(stub.calls))
	}
}

// api.py error prefixes, per handler ("Search failed", "Image search failed", …).
func TestErrorStatusMapping(t *testing.T) {
	prefixes := map[string]string{
		"text":   "Search failed",
		"images": "Image search failed",
		"news":   "News search failed",
		"videos": "Video search failed",
		"books":  "Book search failed",
	}
	for _, category := range []string{"text", "images", "news", "videos", "books"} {
		for _, tc := range []struct {
			name       string
			err        error
			wantStatus int
		}{
			{"ratelimit", fmt.Errorf("ddgs: %w", ddgs.ErrRatelimit), http.StatusTooManyRequests},
			{"no-results", ddgs.ErrNoResults, http.StatusInternalServerError},
			{"timeout", fmt.Errorf("%w: engine timed out", ddgs.ErrTimeout), http.StatusInternalServerError},
			{"generic", errors.New("boom"), http.StatusInternalServerError},
		} {
			t.Run(category+"/"+tc.name, func(t *testing.T) {
				stub := newStub(t)
				stub.err = tc.err
				h := New(stub)

				rec := doRequest(t, h, http.MethodPost, "/search/"+category, strings.NewReader(`{"query":"go"}`))
				if rec.Code != tc.wantStatus {
					t.Fatalf("POST status %d body %s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
				}
				detail := decodeJSONBody(t, rec)["detail"].(string)
				if !strings.HasPrefix(detail, prefixes[category]+": ") {
					t.Errorf("detail %q, want prefix %q", detail, prefixes[category])
				}
				if !strings.Contains(detail, tc.err.Error()) {
					t.Errorf("detail %q should contain %q", detail, tc.err.Error())
				}

				rec = doRequest(t, h, http.MethodGet, "/search/"+category+"?query=go", nil)
				if rec.Code != tc.wantStatus {
					t.Fatalf("GET status %d body %s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
				}
				if got := decodeJSONBody(t, rec)["detail"]; !strings.HasPrefix(got.(string), prefixes[category]+": ") {
					t.Errorf("GET detail %v", got)
				}
			})
		}
	}
}

func TestMalformedJSONIsBadRequest(t *testing.T) {
	h := New(newStub(t))
	rec := doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(`{"query":`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	detail := decodeJSONBody(t, rec)["detail"].(string)
	if !strings.HasPrefix(detail, "Failed to parse request body: ") {
		t.Errorf("detail = %q", detail)
	}
}

func TestBadIntQueryParamIsBadRequest(t *testing.T) {
	h := New(newStub(t))
	rec := doRequest(t, h, http.MethodGet, "/search/text?query=go&max_results=abc", nil)
	assertDetail(t, rec, http.StatusBadRequest, "Input should be a valid integer: max_results")
	rec = doRequest(t, h, http.MethodGet, "/search/books?query=go&page=x", nil)
	assertDetail(t, rec, http.StatusBadRequest, "Input should be a valid integer: page")
}

func TestInvalidJSONBodyField(t *testing.T) {
	h := New(newStub(t))
	rec := doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(`{"query":"go","max_results":"abc"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", rec.Code, rec.Body.String())
	}
	if detail := decodeJSONBody(t, rec)["detail"].(string); !strings.HasPrefix(detail, "Failed to parse request body: ") {
		t.Errorf("detail = %q", detail)
	}
}

func TestUnknownPathAndWrongMethod(t *testing.T) {
	h := New(newStub(t))
	rec := doRequest(t, h, http.MethodGet, "/nope", nil)
	assertDetail(t, rec, http.StatusNotFound, "Not Found")

	rec = doRequest(t, h, http.MethodDelete, "/search/text", nil)
	assertDetail(t, rec, http.StatusMethodNotAllowed, "Method Not Allowed")

	// FastAPI's redirect_slashes default: trailing slash answers 307.
	rec = doRequest(t, h, http.MethodGet, "/search/text/?query=go", nil)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want 307 (body %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/search/text?query=go" {
		t.Errorf("Location = %q", loc)
	}
}

func assertDetail(t *testing.T, rec *httptest.ResponseRecorder, status int, detail string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d body %s, want %d", rec.Code, rec.Body.String(), status)
	}
	body := decodeJSONBody(t, rec)
	assertJSONKeys(t, body, "detail")
	if got := body["detail"]; got != detail {
		t.Errorf("detail = %v, want %q", got, detail)
	}
}

// ---------------------------------------------------------------- extract ---

func TestExtractEndpoints(t *testing.T) {
	stub := newStub(t)
	h := New(stub)

	rec := doRequest(t, h, http.MethodPost, "/extract", strings.NewReader(`{"url":"https://example.com","format":"text_plain"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /extract status %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeJSONBody(t, rec)
	assertJSONKeys(t, body, "url", "content")
	if body["url"] != "https://example.com" {
		t.Errorf("url = %v", body["url"])
	}
	if body["content"] != "# heading" {
		t.Errorf("content = %v", body["content"])
	}
	if got := wantOneCall(t, stub).Method; got != "extract:text_plain" {
		t.Errorf("format forwarded = %q, want text_plain", got)
	}

	// GET variant uses the Python parameter name fmt, default text_markdown.
	stub2 := newStub(t)
	h = New(stub2)
	rec = doRequest(t, h, http.MethodGet, "/extract?url=https://example.com&fmt=content", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /extract status %d body %s", rec.Code, rec.Body.String())
	}
	if got := wantOneCall(t, stub2).Method; got != "extract:content" {
		t.Errorf("format forwarded = %q, want content", got)
	}

	// Default format mirrors Field("text_markdown") for both variants.
	stub3 := newStub(t)
	h = New(stub3)
	doRequest(t, h, http.MethodPost, "/extract", strings.NewReader(`{"url":"https://example.com"}`))
	doRequest(t, h, http.MethodGet, "/extract?url=https://example.com", nil)
	if len(stub3.calls) != 2 {
		t.Fatalf("call count = %d, want 2", len(stub3.calls))
	}
	for i, call := range stub3.calls {
		if call.Method != "extract:text_markdown" {
			t.Errorf("call %d default format = %q, want text_markdown", i, call.Method)
		}
	}
}

func TestExtractBytesContentDecoded(t *testing.T) {
	stub := newStub(t)
	stub.extractResp = &ddgs.Extracted{URL: "https://example.com", Bytes: []byte("<html>hi</html>")}
	h := New(stub)
	rec := doRequest(t, h, http.MethodGet, "/extract?url=https://example.com&fmt=content", nil)
	body := decodeJSONBody(t, rec)
	if body["content"] != "<html>hi</html>" {
		t.Errorf("content = %v, want the bytes decoded to a string (jsonable_encoder)", body["content"])
	}
}

func TestExtractErrorsAndEmptyURL(t *testing.T) {
	stub := newStub(t)
	stub.err = fmt.Errorf("%w: %s", ddgs.ErrDDGS, "failed to fetch https://x: HTTP 404")
	h := New(stub)
	rec := doRequest(t, h, http.MethodPost, "/extract", strings.NewReader(`{"url":"https://x"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	detail := decodeJSONBody(t, rec)["detail"].(string)
	if !strings.HasPrefix(detail, "Extraction failed: ") {
		t.Errorf("detail = %q, want api.py prefix", detail)
	}

	rec = doRequest(t, h, http.MethodPost, "/extract", strings.NewReader(`{}`))
	assertDetail(t, rec, http.StatusBadRequest, "url is mandatory")

	rec = doRequest(t, h, http.MethodGet, "/extract", nil)
	assertDetail(t, rec, http.StatusBadRequest, "url is mandatory")
}

// ------------------------------------------------------------------- CORS ---

func TestCORSPreflight(t *testing.T) {
	h := New(newStub(t))
	req := httptest.NewRequest(http.MethodOptions, "/search/text", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "X-Test, content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status %d body %q", rec.Code, rec.Body.String())
	}
	hdr := rec.Header()
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "https://evil.example" {
		t.Errorf("allow-origin = %q, want the echoed origin (credentials allowed)", got)
	}
	if got := hdr.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("allow-credentials = %q, want true", got)
	}
	if got := hdr.Get("Access-Control-Allow-Methods"); got != "DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT, QUERY" {
		t.Errorf("allow-methods = %q", got)
	}
	if got := hdr.Get("Access-Control-Allow-Headers"); got != "X-Test, content-type" {
		t.Errorf("allow-headers = %q, want the echoed requested headers", got)
	}
	if got := hdr.Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("max-age = %q, want 600", got)
	}
	if got := hdr.Get("Vary"); !strings.Contains(got, "Access-Control-Request-Headers") {
		t.Errorf("vary = %q", got)
	}
	if got := rec.Body.String(); got != "OK" {
		t.Errorf("body = %q, want OK", got)
	}
}

func TestCORSDisallowedMethod(t *testing.T) {
	h := New(newStub(t))
	req := httptest.NewRequest(http.MethodOptions, "/search/text", nil)
	req.Header.Set("Origin", "https://x")
	req.Header.Set("Access-Control-Request-Method", "TRACE")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := rec.Body.String(); got != "Disallowed CORS method" {
		t.Errorf("body = %q", got)
	}
}

func TestCORSSimpleResponse(t *testing.T) {
	h := New(newStub(t))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://client.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://client.example" {
		t.Errorf("allow-origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("allow-credentials = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("vary = %q", got)
	}
}

func TestCORSWithoutOrigin(t *testing.T) {
	h := New(newStub(t))
	rec := doRequest(t, h, http.MethodGet, "/health", nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want unset when the request has no Origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("vary = %q, want Origin", got)
	}
}

// ---------------------------------------------------------- timeout option ---

func TestRequestTimeoutKillsSlowSearch(t *testing.T) {
	block := make(chan struct{})
	stub := newStub(t)
	stub.block = block
	h := New(stub, WithRequestTimeout(20*time.Millisecond))

	rec := doRequest(t, h, http.MethodPost, "/search/text", strings.NewReader(`{"query":"go"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s, want 500", rec.Code, rec.Body.String())
	}
	detail := decodeJSONBody(t, rec)["detail"].(string)
	if !strings.HasPrefix(detail, "Search failed: ") {
		t.Errorf("detail = %q", detail)
	}
	close(block)
}

func TestWithLibraryTimeoutOption(t *testing.T) {
	stub := newStub(t)
	h := New(stub, WithLibraryTimeout(15*time.Second))
	doRequest(t, h, http.MethodGet, "/search/text?query=go", nil)
	call := wantOneCall(t, stub)
	if !call.HadDeadline || call.Timeout < 19*time.Second || call.Timeout > 20*time.Second {
		t.Errorf("deadline = %v (set=%v), want ~20s", call.Timeout, call.HadDeadline)
	}

	stub2 := newStub(t)
	h = New(stub2, WithRequestTimeout(0))
	doRequest(t, h, http.MethodGet, "/search/text?query=go", nil)
	if wantOneCall(t, stub2).HadDeadline {
		t.Error("WithRequestTimeout(0) should disable the deadline")
	}
}

// -------------------------------------------------- live-socket integration ---

// TestServerOverRealSocket boots the handler on a 127.0.0.1:0 listener and
// speaks HTTP over a real socket. No external network is used: the Searcher is
// still the stub.
func TestServerOverRealSocket(t *testing.T) {
	stub := newStub(t)
	stub.results = sampleResults["text"]
	srv := httptest.NewServer(New(stub))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health -> %d", resp.StatusCode)
	}
	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "healthy" || health.Version != "1.0.0" || health.Service != "DDGS API" {
		t.Errorf("health = %+v", health)
	}

	resp2, err := http.Post(srv.URL+"/search/text", "application/json",
		strings.NewReader(`{"query":"golang","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("POST /search/text -> %d", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var out SearchResponse
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0]["href"] == nil {
		t.Errorf("results = %v", out.Results)
	}
	if got := wantOneCall(t, stub).Opts.MaxResults; got != 3 {
		t.Errorf("max_results = %d, want 3", got)
	}
}
