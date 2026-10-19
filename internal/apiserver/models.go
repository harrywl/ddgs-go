package apiserver

// Request/response models, mirroring the Pydantic models declared in
// ddgs/api_server/api.py (v9.16.0). JSON field names are snake_case and are
// copied verbatim from the Python `Field(...)` declarations — including the
// per-category filter fields (images: size/color/type_image/layout/
// license_image; videos: resolution/duration/license_videos) that the Python
// handlers forward to DDGS as **kwargs. In Go those filters are carried in
// SearchOptions.Extra, whose keys match the kwargs names the engines read
// (see engine_duckduckgo.go, mirroring ddgs/engines/duckduckgo_images.py and
// duckduckgo_videos.py).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	ddgs "github.com/harrywl/ddgs-go"
)

// APIServiceName / APIVersion mirror the FastAPI(...) metadata and the
// HealthResponse literals in api.py: title "DDGS API", version "1.0.0".
const (
	APIServiceName = "DDGS API"
	APIVersion     = "1.0.0"
)

// OptInt mirrors a Pydantic `int | None = 10` field. It records both "key
// absent" (use the model default) and "key present but null" (Python None),
// because api.py's max_results has distinct behaviours:
//
//   - omitted       -> 10 (the Field default)
//   - explicit null -> Python None -> falsy -> return everything found
//   - explicit 0    -> falsy in Python -> everything found
//   - negative      -> Python slices results[:negative]; Go has no equivalent,
//     treated as unlimited (documented deviation).
//
// Per SPEC §9.5 the Go library spells "unlimited" as MaxResults: -1.
type OptInt struct {
	Present bool
	Null    bool
	Value   int
}

// UnmarshalJSON implements json.Unmarshaler. Presence and nullness are
// recorded, and the lenient coercions Pydantic applies in lax mode ("10",
// 10.0) are accepted.
func (o *OptInt) UnmarshalJSON(data []byte) error {
	o.Present = true
	trimmed := bytes.TrimSpace(data)
	if string(trimmed) == "null" {
		o.Null = true
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("%q is not a valid integer", s)
		}
		o.Value = n
		return nil
	}
	var f float64
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return err
	}
	if f != math.Trunc(f) {
		return fmt.Errorf("%v is not a valid integer", f)
	}
	o.Value = int(f)
	return nil
}

// MarshalJSON round-trips the field the way Pydantic serialises it.
func (o OptInt) MarshalJSON() ([]byte, error) {
	if !o.Present || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

// resolve maps the tri-state presence model onto ddgs.SearchOptions.MaxResults.
func (o OptInt) resolve() int {
	if !o.Present {
		return ddgs.DefaultMaxResults // Python Field default max_results=10
	}
	if o.Null || o.Value <= 0 {
		return -1 // Python None/0 is falsy -> unlimited (SPEC §9.5)
	}
	return o.Value
}

// HealthResponse mirrors api.py HealthResponse(status, version, service).
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Service string `json:"service"`
}

// SearchResponse mirrors api.py SearchResponse(results: list[dict[str, Any]]).
// Results is always serialised as a JSON array, never null.
type SearchResponse struct {
	Results []ddgs.Result `json:"results"`
}

// TextSearchRequest mirrors api.py TextSearchRequest.
type TextSearchRequest struct {
	Query      string  `json:"query"`
	Region     string  `json:"region"`
	SafeSearch string  `json:"safesearch"`
	TimeLimit  *string `json:"timelimit"`
	MaxResults OptInt  `json:"max_results"`
	Page       int     `json:"page"`
	Backend    string  `json:"backend"`
}

// ImagesSearchRequest mirrors api.py ImagesSearchRequest.
type ImagesSearchRequest struct {
	Query        string  `json:"query"`
	Region       string  `json:"region"`
	SafeSearch   string  `json:"safesearch"`
	TimeLimit    *string `json:"timelimit"`
	MaxResults   OptInt  `json:"max_results"`
	Page         int     `json:"page"`
	Backend      string  `json:"backend"`
	Size         *string `json:"size"`
	Color        *string `json:"color"`
	TypeImage    *string `json:"type_image"`
	Layout       *string `json:"layout"`
	LicenseImage *string `json:"license_image"`
}

// NewsSearchRequest mirrors api.py NewsSearchRequest.
type NewsSearchRequest struct {
	Query      string  `json:"query"`
	Region     string  `json:"region"`
	SafeSearch string  `json:"safesearch"`
	TimeLimit  *string `json:"timelimit"`
	MaxResults OptInt  `json:"max_results"`
	Page       int     `json:"page"`
	Backend    string  `json:"backend"`
}

// VideosSearchRequest mirrors api.py VideosSearchRequest.
type VideosSearchRequest struct {
	Query         string  `json:"query"`
	Region        string  `json:"region"`
	SafeSearch    string  `json:"safesearch"`
	TimeLimit     *string `json:"timelimit"`
	MaxResults    OptInt  `json:"max_results"`
	Page          int     `json:"page"`
	Backend       string  `json:"backend"`
	Resolution    *string `json:"resolution"`
	Duration      *string `json:"duration"`
	LicenseVideos *string `json:"license_videos"`
}

// BooksSearchRequest mirrors api.py BooksSearchRequest — note the Python model
// has no region/safesearch/timelimit fields at all.
type BooksSearchRequest struct {
	Query      string `json:"query"`
	MaxResults OptInt `json:"max_results"`
	Page       int    `json:"page"`
	Backend    string `json:"backend"`
}

// ExtractRequest mirrors api.py ExtractRequest (url + format).
type ExtractRequest struct {
	URL    string `json:"url"`
	Format string `json:"format"`
}

// ExtractResponse mirrors the dict returned by DDGS.extract():
// {"url": ..., "content": ...}. FastAPI's jsonable_encoder renders raw bytes
// as a decoded UTF-8 string, which is what Content does for format=content.
type ExtractResponse struct {
	URL     string `json:"url"`
	Content string `json:"content"`
}

// filterField pairs an engine kwarg name with the request value carrying it.
type filterField struct {
	name  string
	value *string
}

// extraFrom collects non-empty filters into SearchOptions.Extra keyed by the
// Python kwarg names the engines read. Nil/empty entries are dropped, matching
// Python, which passes None and lets engines use kwargs.get(...) checks.
func extraFrom(fields []filterField) map[string]string {
	var out map[string]string
	for _, f := range fields {
		if f.value == nil || *f.value == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(fields))
		}
		out[f.name] = *f.value
	}
	return out
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// baseOptions applies the Python Field defaults that a zero Go value would
// otherwise hide: region "us-en", safesearch "moderate", page 1, backend
// "auto". ddgs.DDGS re-applies the same defaults internally; doing it here
// keeps the SearchOptions handed to the Searcher explicit and assertable.
func baseOptions(region, safesearch string, timelimit *string, maxResults OptInt, page int, backend string) *ddgs.SearchOptions {
	if region == "" {
		region = "us-en"
	}
	if safesearch == "" {
		safesearch = "moderate"
	}
	if page < 1 {
		page = 1
	}
	if backend == "" {
		backend = "auto"
	}
	return &ddgs.SearchOptions{
		Region:     region,
		SafeSearch: safesearch,
		TimeLimit:  derefString(timelimit),
		MaxResults: maxResults.resolve(),
		Page:       page,
		Backend:    backend,
	}
}

// ToOptions converts a TextSearchRequest to SearchOptions.
func (r TextSearchRequest) ToOptions() *ddgs.SearchOptions {
	return baseOptions(r.Region, r.SafeSearch, r.TimeLimit, r.MaxResults, r.Page, r.Backend)
}

// ToOptions converts a NewsSearchRequest to SearchOptions.
func (r NewsSearchRequest) ToOptions() *ddgs.SearchOptions {
	return baseOptions(r.Region, r.SafeSearch, r.TimeLimit, r.MaxResults, r.Page, r.Backend)
}

// ToOptions converts an ImagesSearchRequest to SearchOptions, forwarding the
// image filters as engine kwargs (SearchOptions.Extra), exactly like api.py
// passes size/color/type_image/layout/license_image to DDGS.images(**kwargs).
func (r ImagesSearchRequest) ToOptions() *ddgs.SearchOptions {
	opts := baseOptions(r.Region, r.SafeSearch, r.TimeLimit, r.MaxResults, r.Page, r.Backend)
	opts.Extra = extraFrom([]filterField{
		{"size", r.Size},
		{"color", r.Color},
		{"type_image", r.TypeImage},
		{"layout", r.Layout},
		{"license_image", r.LicenseImage},
	})
	return opts
}

// ToOptions converts a VideosSearchRequest to SearchOptions, forwarding the
// video filters (resolution/duration/license_videos) as engine kwargs.
func (r VideosSearchRequest) ToOptions() *ddgs.SearchOptions {
	opts := baseOptions(r.Region, r.SafeSearch, r.TimeLimit, r.MaxResults, r.Page, r.Backend)
	opts.Extra = extraFrom([]filterField{
		{"resolution", r.Resolution},
		{"duration", r.Duration},
		{"license_videos", r.LicenseVideos},
	})
	return opts
}

// ToOptions converts a BooksSearchRequest to SearchOptions. api.py calls
// DDGS.books(query=..., max_results=..., page=..., backend=...) only, so no
// region/safesearch/timelimit defaults are applied here (the library applies
// its own).
func (r BooksSearchRequest) ToOptions() *ddgs.SearchOptions {
	page := r.Page
	if page < 1 {
		page = 1
	}
	backend := r.Backend
	if backend == "" {
		backend = "auto"
	}
	return &ddgs.SearchOptions{
		MaxResults: r.MaxResults.resolve(),
		Page:       page,
		Backend:    backend,
	}
}
