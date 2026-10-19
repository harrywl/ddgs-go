package apiserver

// server.go — routes, handlers and error mapping for the Go port of
// ddgs/api_server/api.py. See the package comment in apiserver.go for the
// endpoint table.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/searchsvc"
)

// logger mirrors api.py's `logging.basicConfig(level=logging.INFO)` plus the
// per-handler logger.warning("Error in ... search: %s", e) calls.
var logger = log.New(os.Stderr, "ddgs-api: ", log.LstdFlags)

// timeoutSlack is the extra head-room added on top of the library timeout for
// the per-request context deadline (SPEC §4: overall request timeout =
// library timeout + 5s slack).
const timeoutSlack = 5 * time.Second

// libraryDefaultTimeout mirrors Python DDGS(timeout=5).
const libraryDefaultTimeout = 5 * time.Second

// maxBodyBytes bounds POST request bodies. uvicorn/FastAPI impose no hard
// limit; 1 MiB is ample for these flat JSON payloads.
const maxBodyBytes = 1 << 20

// server holds the wiring shared by every handler.
type server struct {
	searcher searchsvc.Searcher
	timeout  time.Duration // per-request overall deadline
}

// Option configures New.
type Option func(*server)

// WithLibraryTimeout sets the library timeout used to compute the per-request
// deadline (deadline = library timeout + 5s slack), mirroring api.py which
// builds DDGS() with the library default timeout of 5 seconds.
func WithLibraryTimeout(d time.Duration) Option {
	return func(s *server) {
		if d > 0 {
			s.timeout = d + timeoutSlack
		}
	}
}

// WithRequestTimeout overrides the per-request deadline outright. A value <= 0
// disables the deadline. Handy in tests.
func WithRequestTimeout(d time.Duration) Option {
	return func(s *server) {
		s.timeout = d
	}
}

// New builds the API handler. s is the search backend: in production
// searchsvc.New(ddgsInstance) around a single shared *ddgs.DDGS (SPEC §7.5,
// mirroring api.py's _get_ddgs()), in tests a stub Searcher.
func New(s searchsvc.Searcher, opts ...Option) http.Handler {
	srv := &server{searcher: s, timeout: libraryDefaultTimeout + timeoutSlack}
	for _, o := range opts {
		o(srv)
	}

	mux := http.NewServeMux()
	// api.py: @app.get("/") and @app.get("/health") -> HealthResponse.
	mux.HandleFunc("GET /{$}", srv.handleHealth)
	mux.HandleFunc("GET /health", srv.handleHealth)

	for _, cat := range categories {
		mux.HandleFunc("POST "+cat.path, srv.postHandler(cat))
		mux.HandleFunc("GET "+cat.path, srv.getHandler(cat))
	}
	// api.py also exposes POST/GET /extract (DDGS.extract).
	mux.HandleFunc("POST /extract", srv.postExtractHandler)
	mux.HandleFunc("GET /extract", srv.getExtractHandler)

	// Anything else: answer with FastAPI's JSON error shape instead of
	// net/http's plain-text 404/405.
	mux.HandleFunc("/", srv.handleNoMatch)

	// api.py registers CORSMiddleware(allow_origins=["*"],
	// allow_credentials=True, allow_methods=["*"], allow_headers=["*"]).
	return &corsMiddleware{next: mux}
}

// --------------------------------------------------------------- endpoints ---

// searchCategory bundles what differs between the five search endpoints of
// api.py: the URL path, the handler's error-message prefix ("Search failed",
// "Image search failed", ...), the two request decoders (POST body / GET query
// string) and the Searcher method to call.
type searchCategory struct {
	path        string
	errPrefix   string
	decodeJSON  func(data []byte) (string, *ddgs.SearchOptions, error)
	decodeQuery func(q url.Values) (string, *ddgs.SearchOptions, error)
	call        func(ctx context.Context, s searchsvc.Searcher, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
}

var categories = []searchCategory{
	{
		path:        "/search/text",
		errPrefix:   "Search failed",
		decodeJSON:  decodeBody[TextSearchRequest](func(m TextSearchRequest) (string, *ddgs.SearchOptions) { return m.Query, m.ToOptions() }),
		decodeQuery: decodeCommonSearch,
		call: func(ctx context.Context, s searchsvc.Searcher, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
			return s.Text(ctx, q, o)
		},
	},
	{
		path:      "/search/images",
		errPrefix: "Image search failed",
		decodeJSON: decodeBody[ImagesSearchRequest](func(m ImagesSearchRequest) (string, *ddgs.SearchOptions) {
			return m.Query, m.ToOptions()
		}),
		decodeQuery: func(q url.Values) (string, *ddgs.SearchOptions, error) {
			query, opts, err := decodeCommonSearch(q)
			if err != nil {
				return "", nil, err
			}
			// api.py search_images_get forwards size/color/type_image/layout/
			// license_image to DDGS.images(**kwargs) -> Extra here.
			opts.Extra = extraFrom([]filterField{
				{"size", optValue(q, "size")},
				{"color", optValue(q, "color")},
				{"type_image", optValue(q, "type_image")},
				{"layout", optValue(q, "layout")},
				{"license_image", optValue(q, "license_image")},
			})
			return query, opts, nil
		},
		call: func(ctx context.Context, s searchsvc.Searcher, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
			return s.Images(ctx, q, o)
		},
	},
	{
		path:        "/search/news",
		errPrefix:   "News search failed",
		decodeJSON:  decodeBody[NewsSearchRequest](func(m NewsSearchRequest) (string, *ddgs.SearchOptions) { return m.Query, m.ToOptions() }),
		decodeQuery: decodeCommonSearch,
		call: func(ctx context.Context, s searchsvc.Searcher, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
			return s.News(ctx, q, o)
		},
	},
	{
		path:      "/search/videos",
		errPrefix: "Video search failed",
		decodeJSON: decodeBody[VideosSearchRequest](func(m VideosSearchRequest) (string, *ddgs.SearchOptions) {
			return m.Query, m.ToOptions()
		}),
		decodeQuery: func(q url.Values) (string, *ddgs.SearchOptions, error) {
			query, opts, err := decodeCommonSearch(q)
			if err != nil {
				return "", nil, err
			}
			opts.Extra = extraFrom([]filterField{
				{"resolution", optValue(q, "resolution")},
				{"duration", optValue(q, "duration")},
				{"license_videos", optValue(q, "license_videos")},
			})
			return query, opts, nil
		},
		call: func(ctx context.Context, s searchsvc.Searcher, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
			return s.Videos(ctx, q, o)
		},
	},
	{
		// api.py BooksSearchRequest / search_books_get carry only query,
		// max_results, page, backend.
		path:        "/search/books",
		errPrefix:   "Book search failed",
		decodeJSON:  decodeBody[BooksSearchRequest](func(m BooksSearchRequest) (string, *ddgs.SearchOptions) { return m.Query, m.ToOptions() }),
		decodeQuery: decodeBooksSearch,
		call: func(ctx context.Context, s searchsvc.Searcher, q string, o *ddgs.SearchOptions) ([]ddgs.Result, error) {
			return s.Books(ctx, q, o)
		},
	},
}

// decodeBody unmarshals a POST body into the mirrored Pydantic model.
func decodeBody[T any](mapFn func(T) (string, *ddgs.SearchOptions)) func([]byte) (string, *ddgs.SearchOptions, error) {
	return func(data []byte) (string, *ddgs.SearchOptions, error) {
		var model T
		if err := json.Unmarshal(data, &model); err != nil {
			return "", nil, &requestError{status: http.StatusBadRequest,
				detail: "Failed to parse request body: " + err.Error()}
		}
		query, opts := mapFn(model)
		return query, opts, nil
	}
}

// decodeCommonSearch parses the GET query parameters shared by the text /
// images / news / videos handlers of api.py.
func decodeCommonSearch(q url.Values) (string, *ddgs.SearchOptions, error) {
	query := q.Get("query")
	maxResults, err := intValue(q, "max_results", ddgs.DefaultMaxResults)
	if err != nil {
		return "", nil, err
	}
	page, err := intValue(q, "page", 1)
	if err != nil {
		return "", nil, err
	}
	opts := baseOptions(
		stringValue(q, "region", "us-en"),
		stringValue(q, "safesearch", "moderate"),
		optValue(q, "timelimit"),
		OptInt{Present: true, Value: maxResults},
		pageAtLeastOne(page),
		stringValue(q, "backend", "auto"),
	)
	return query, opts, nil
}

// decodeBooksSearch parses GET /search/books query params (no region etc.).
func decodeBooksSearch(q url.Values) (string, *ddgs.SearchOptions, error) {
	query := q.Get("query")
	maxResults, err := intValue(q, "max_results", ddgs.DefaultMaxResults)
	if err != nil {
		return "", nil, err
	}
	page, err := intValue(q, "page", 1)
	if err != nil {
		return "", nil, err
	}
	opts := &ddgs.SearchOptions{
		MaxResults: maxResults,
		Page:       pageAtLeastOne(page),
		Backend:    stringValue(q, "backend", "auto"),
	}
	return query, opts, nil
}

func stringValue(q url.Values, key, def string) string {
	if v := q.Get(key); v != "" {
		return v
	}
	return def
}

// optValue returns a pointer to a query parameter (nil when absent), mirroring
// Python's optional `str | None = None` parameters.
func optValue(q url.Values, key string) *string {
	vs, ok := q[key]
	if !ok || len(vs) == 0 {
		return nil
	}
	v := vs[0]
	return &v
}

// intValue coerces an int query parameter the way FastAPI does in lax mode.
// Unparseable input answers 400 (FastAPI answers 422; see the deviations note).
func intValue(q url.Values, key string, def int) (int, error) {
	vs, ok := q[key]
	if !ok || len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
		return def, nil
	}
	raw := strings.TrimSpace(vs[0])
	if n, err := strconv.Atoi(raw); err == nil {
		return n, nil
	}
	// "10.0" is valid int input for pydantic in lax mode.
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f == float64(int64(f)) {
		return int(f), nil
	}
	return 0, &requestError{status: http.StatusBadRequest,
		detail: fmt.Sprintf("Input should be a valid integer: %s", key)}
}

func pageAtLeastOne(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

// ---------------------------------------------------------------- handlers ---

// postHandler builds a POST /search/<category> handler.
func (s *server) postHandler(cat searchCategory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "Failed to parse request body: "+err.Error())
			return
		}
		query, opts, err := cat.decodeJSON(data)
		if err != nil {
			s.writeSearchError(w, r, cat, err)
			return
		}
		s.runSearch(w, r, cat, query, opts)
	}
}

// getHandler builds a GET /search/<category> handler.
func (s *server) getHandler(cat searchCategory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query, opts, err := cat.decodeQuery(r.URL.Query())
		if err != nil {
			s.writeSearchError(w, r, cat, err)
			return
		}
		s.runSearch(w, r, cat, query, opts)
	}
}

// runSearch mirrors the body of every api.py search handler: call the library,
// wrap results in SearchResponse, log and raise on failure.
func (s *server) runSearch(w http.ResponseWriter, r *http.Request, cat searchCategory, query string, opts *ddgs.SearchOptions) {
	if strings.TrimSpace(query) == "" {
		// "query is mandatory" -> 400. api.py lets the library's DDGSException
		// escape as 500 "Search failed: query is mandatory."; SPEC §4 pins 400
		// for the Go port.
		s.writeSearchError(w, r, cat, &requestError{status: http.StatusBadRequest, detail: "query is mandatory"})
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()

	results, err := cat.call(ctx, s.searcher, query, opts)
	if err != nil {
		s.writeSearchError(w, r, cat, err)
		return
	}
	if results == nil {
		results = []ddgs.Result{} // Pydantic: results is a list, never null
	}
	writeJSON(w, http.StatusOK, SearchResponse{Results: results})
}

// requestContext derives the per-request context with the overall deadline.
func (s *server) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), s.timeout)
}

// postExtractHandler mirrors api.py extract_content (POST /extract).
func (s *server) postExtractHandler(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to parse request body: "+err.Error())
		return
	}
	var req ExtractRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Failed to parse request body: "+err.Error())
		return
	}
	if req.Format == "" {
		req.Format = string(ddgs.FormatTextMarkdown) // Field default
	}
	s.runExtract(w, r, req.URL, req.Format)
}

// getExtractHandler mirrors api.py extract_content_get (GET /extract?url=&fmt=).
// Note the Python query parameter is named fmt, not format.
func (s *server) getExtractHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("fmt")
	if format == "" {
		format = string(ddgs.FormatTextMarkdown)
	}
	s.runExtract(w, r, q.Get("url"), format)
}

func (s *server) runExtract(w http.ResponseWriter, r *http.Request, rawURL, format string) {
	if strings.TrimSpace(rawURL) == "" {
		writeError(w, http.StatusBadRequest, "url is mandatory")
		return
	}
	ctx, cancel := s.requestContext(r)
	defer cancel()

	ex, err := s.searcher.Extract(ctx, rawURL, ddgs.ExtractFormat(format))
	if err != nil {
		logger.Printf("Error extracting content: %v", err)
		writeError(w, statusForError(err), "Extraction failed: "+err.Error())
		return
	}
	content := ex.Content
	if ddgs.ExtractFormat(format) == ddgs.FormatContent {
		// FastAPI's jsonable_encoder renders bytes as a decoded string.
		content = string(ex.Bytes)
	}
	writeJSON(w, http.StatusOK, ExtractResponse{URL: ex.URL, Content: content})
}

// handleHealth answers GET / and GET /health with HealthResponse.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:  "healthy",
		Version: APIVersion,
		Service: APIServiceName,
	})
}

// handleNoMatch reproduces Starlette/FastAPI's JSON 404 / 405 and the
// redirect_slashes 307 for requests that matched no registered route.
func (s *server) handleNoMatch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if knownRoute(path) {
		// The path exists but no method pattern matched: Starlette answers 405.
		writeError(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	// FastAPI default redirect_slashes=True: "/search/text/" -> 307.
	if trimmed := strings.TrimRight(path, "/"); trimmed != "" && trimmed != path {
		if knownRoute(trimmed) {
			target := trimmed
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Not Found")
}

// knownRoute reports whether an exact path is a registered endpoint.
func knownRoute(path string) bool {
	switch path {
	case "/", "/health", "/extract":
		return true
	}
	for _, cat := range categories {
		if cat.path == path {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- errors ---

// errorBody is FastAPI's HTTPException JSON shape: {"detail": "..."}.
type errorBody struct {
	Detail string `json:"detail"`
}

// requestError is an already-shaped HTTP error raised while decoding a request.
type requestError struct {
	status int
	detail string
}

func (e *requestError) Error() string { return e.detail }

// statusForError maps library errors onto HTTP status codes:
//   - ddgs.ErrRatelimit (Python RatelimitException) -> 429 (SPEC §4)
//   - everything else (ErrNoResults, ErrTimeout, ErrDDGS, ...) -> 500,
//     mirroring api.py's blanket `except Exception` handler. Python raises
//     DDGSException("No results found.") for an empty result set and FastAPI
//     turns it into a 500 — Go mirrors that rather than inventing a 404.
func statusForError(err error) int {
	var re *requestError
	if errors.As(err, &re) {
		return re.status
	}
	if errors.Is(err, ddgs.ErrRatelimit) {
		return http.StatusTooManyRequests
	}
	return http.StatusInternalServerError
}

// writeSearchError logs and emits the FastAPI-shaped error response, prefixing
// library errors with the category-specific text api.py uses.
func (s *server) writeSearchError(w http.ResponseWriter, r *http.Request, cat searchCategory, err error) {
	logger.Printf("Error in %s search (%s): %v", strings.TrimPrefix(cat.path, "/search/"), r.Method, err)
	status := statusForError(err)
	detail := cat.errPrefix + ": " + err.Error()
	var re *requestError
	if errors.As(err, &re) {
		detail = re.detail
	}
	writeError(w, status, detail)
}

// writeJSON serialises v with FastAPI/uvicorn's content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"Internal Server Error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError emits a FastAPI-shaped JSON error.
func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, errorBody{Detail: detail})
}
