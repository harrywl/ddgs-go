package apiserver

// Model-level tests: the JSON field names and defaults must match the
// Pydantic models in py-ddgs/ddgs/api_server/api.py verbatim.

import (
	"encoding/json"
	"reflect"
	"testing"

	ddgs "github.com/harrywl/ddgs-go"
)

// jsonFieldsOf returns the struct field names -> json tag names (minus ,opts).
func jsonFieldsOf(t *testing.T, v any) map[string]string {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct", v)
	}
	out := make(map[string]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Errorf("field %s.%s has no json tag", typ.Name(), f.Name)
			continue
		}
		name := tag
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				name = tag[:j]
				break
			}
		}
		out[name] = f.Name
	}
	return out
}

// assertModel checks the exact set of JSON fields and their order matches want.
func assertModel(t *testing.T, v any, want ...string) {
	t.Helper()
	got := jsonFieldsOf(t, v)
	if len(got) != len(want) {
		t.Errorf("%T has %d JSON fields (%v), want %d %v", v, len(got), sortedKeys(got), len(want), want)
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%T missing JSON field %q", v, name)
		}
	}
	// Round-trip: the marshalled object must contain exactly these keys.
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	for name := range m {
		found := false
		for _, w := range want {
			if w == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%T serialises unexpected field %q", v, name)
		}
	}
}

// sortedKeys returns the map keys in lexicographic order for error messages.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestModelFieldNamesMatchPydanticModels(t *testing.T) {
	// api.py TextSearchRequest / NewsSearchRequest.
	for _, v := range []any{TextSearchRequest{}, NewsSearchRequest{}} {
		assertModel(t, v, "query", "region", "safesearch", "timelimit", "max_results", "page", "backend")
	}
	// api.py ImagesSearchRequest.
	assertModel(t, ImagesSearchRequest{},
		"query", "region", "safesearch", "timelimit", "max_results", "page", "backend",
		"size", "color", "type_image", "layout", "license_image")
	// api.py VideosSearchRequest.
	assertModel(t, VideosSearchRequest{},
		"query", "region", "safesearch", "timelimit", "max_results", "page", "backend",
		"resolution", "duration", "license_videos")
	// api.py BooksSearchRequest — deliberately shorter.
	assertModel(t, BooksSearchRequest{}, "query", "max_results", "page", "backend")
	// api.py ExtractRequest.
	assertModel(t, ExtractRequest{}, "url", "format")
	// api.py SearchResponse / HealthResponse.
	assertModel(t, SearchResponse{}, "results")
	assertModel(t, HealthResponse{}, "status", "version", "service")
	// DDGS.extract() return dict.
	assertModel(t, ExtractResponse{}, "url", "content")
	// FastAPI HTTPException body.
	assertModel(t, errorBody{}, "detail")
}

func TestModelsSerialiseSnakeCase(t *testing.T) {
	s := "Blue"
	data, err := json.Marshal(ImagesSearchRequest{Query: "go", Color: &s})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"query":"go","region":"","safesearch":"","timelimit":null,"max_results":null,` +
		`"page":0,"backend":"","size":null,"color":"Blue","type_image":null,"layout":null,"license_image":null}`
	if string(data) != want {
		t.Errorf("serialised:\n%s\nwant:\n%s", data, want)
	}
}

func TestOptIntPresence(t *testing.T) {
	var absent TextSearchRequest
	if err := json.Unmarshal([]byte(`{"query":"go"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.MaxResults.Present {
		t.Error("max_results must be absent when the key is missing")
	}
	if absent.MaxResults.resolve() != ddgs.DefaultMaxResults {
		t.Errorf("absent max_results resolves to %d, want 10", absent.MaxResults.resolve())
	}

	var null TextSearchRequest
	if err := json.Unmarshal([]byte(`{"query":"go","max_results":null}`), &null); err != nil {
		t.Fatal(err)
	}
	if !null.MaxResults.Present || !null.MaxResults.Null {
		t.Errorf("explicit null must be recorded: %+v", null.MaxResults)
	}
	if null.MaxResults.resolve() != -1 {
		t.Error("explicit null max_results must resolve to unlimited (-1)")
	}

	var pageOpt TextSearchRequest
	if err := json.Unmarshal([]byte(`{"query":"go","page":3}`), &pageOpt); err != nil {
		t.Fatal(err)
	}
	if pageOpt.Page != 3 {
		t.Errorf("page = %d, want 3", pageOpt.Page)
	}
}

func TestOptionalFiltersArePointers(t *testing.T) {
	// Optional str|None fields must be pointers so "absent" and "empty string"
	// stay distinguishable, like Pydantic's None vs "".
	var r ImagesSearchRequest
	if err := json.Unmarshal([]byte(`{"query":"go"}`), &r); err != nil {
		t.Fatal(err)
	}
	opts := r.ToOptions()
	if len(opts.Extra) != 0 {
		t.Errorf("Extra = %v, want empty when no filter is supplied", opts.Extra)
	}

	empty := ""
	r.Size = &empty
	if len(r.ToOptions().Extra) != 0 {
		t.Error("empty filter value must not reach the engines")
	}

	// A single filter lands under the Python kwarg name.
	large := "Large"
	r2 := ImagesSearchRequest{Query: "go", Size: &large}
	if got := r2.ToOptions().Extra["size"]; got != "Large" {
		t.Errorf("Extra[size] = %q, want Large", got)
	}

	v := VideosSearchRequest{Query: "go", Duration: strPtr("medium"), LicenseVideos: strPtr("youtube")}
	extra := v.ToOptions().Extra
	if extra["duration"] != "medium" || extra["license_videos"] != "youtube" {
		t.Errorf("videos Extra = %v", extra)
	}
	if _, ok := extra["resolution"]; ok {
		t.Errorf("unset resolution must not appear in Extra: %v", extra)
	}
}

// BooksSearchRequest must not invent the region/safesearch/timelimit defaults
// api.py never passes to DDGS.books().
func TestBooksOptionsOmitNonModelFields(t *testing.T) {
	opts := BooksSearchRequest{Query: "go"}.ToOptions()
	if opts.Region != "" || opts.SafeSearch != "" || opts.TimeLimit != "" {
		t.Errorf("books options leak defaults: %+v", opts)
	}
	if opts.Page != 1 || opts.Backend != "auto" || opts.MaxResults != 10 {
		t.Errorf("books defaults = %+v", opts)
	}
}

func strPtr(s string) *string { return &s }
