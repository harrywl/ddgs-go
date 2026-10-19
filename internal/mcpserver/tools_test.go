package mcpserver

import (
	"encoding/json"
	"sort"
	"testing"
)

// listedTool mirrors one tools/list entry.
type listedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
			Default     any    `json:"default"`
		} `json:"properties"`
		Required []string `json:"required"`
	} `json:"inputSchema"`
}

// listTools drives the real stdio loop and decodes tools/list.
func listTools(t *testing.T) []listedTool {
	t.Helper()
	out, _ := runServer(t, &stubSearcher{}, request(100, "tools/list", "")+"\n")
	msgs := parseResponses(t, out)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1", len(msgs))
	}
	var res struct {
		Tools []listedTool `json:"tools"`
	}
	decodeResult(t, msgs[0], &res)
	return res.Tools
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestToolsListSchema pins the six tools to the signatures in mcp.py:
// property names, required lists and schema types.
func TestToolsListSchema(t *testing.T) {
	tools := listTools(t)

	base := []string{"query", "region", "safesearch", "timelimit", "max_results", "page", "backend"}
	images := append(append([]string(nil), base...), "size", "color", "type_image", "layout", "license_image")
	videos := append(append([]string(nil), base...), "resolution", "duration", "license_videos")

	// Order follows mcp.py's declaration order.
	want := []struct {
		name     string
		props    []string
		required []string
	}{
		{"search_text", base, []string{"query"}},
		{"search_images", images, []string{"query"}},
		{"search_news", base, []string{"query"}},
		{"search_videos", videos, []string{"query"}},
		{"search_books", []string{"query", "max_results", "page", "backend"}, []string{"query"}},
		{"extract_content", []string{"url", "fmt"}, []string{"url"}},
	}

	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(tools), len(want), tools)
	}

	for i, w := range want {
		got := tools[i]
		if got.Name != w.name {
			t.Errorf("tool[%d] name = %q, want %q", i, got.Name, w.name)
			continue
		}
		if got.InputSchema.Type != "object" {
			t.Errorf("%s inputSchema.type = %q, want object", w.name, got.InputSchema.Type)
		}
		if got.Description == "" {
			t.Errorf("%s has empty description", w.name)
		}

		var propNames []string
		for name := range got.InputSchema.Properties {
			propNames = append(propNames, name)
		}
		if !equalStrings(sorted(propNames), sorted(w.props)) {
			t.Errorf("%s properties = %v, want %v", w.name, sorted(propNames), sorted(w.props))
		}
		if !equalStrings(sorted(got.InputSchema.Required), sorted(w.required)) {
			t.Errorf("%s required = %v, want %v", w.name, got.InputSchema.Required, w.required)
		}
		for name, prop := range got.InputSchema.Properties {
			if prop.Type != "string" && prop.Type != "integer" {
				t.Errorf("%s.%s type = %q, want string|integer", w.name, name, prop.Type)
			}
			if prop.Description == "" {
				t.Errorf("%s.%s has no description (mcp.py Args line)", w.name, name)
			}
		}
	}
}

// TestToolPropertyTypesAndDefaults pins the Python types/defaults that the
// Go zero value would otherwise hide.
func TestToolPropertyTypesAndDefaults(t *testing.T) {
	byName := map[string]listedTool{}
	for _, tool := range listTools(t) {
		byName[tool.Name] = tool
	}

	text, ok := byName["search_text"]
	if !ok {
		t.Fatal("search_text missing")
	}
	if got := text.Description; got != "Perform a text search using DDGS." {
		t.Errorf("search_text description = %q", got)
	}
	if got := text.InputSchema.Properties["query"].Type; got != "string" {
		t.Errorf("query type = %q, want string", got)
	}
	if got := text.InputSchema.Properties["max_results"].Type; got != "integer" {
		t.Errorf("max_results type = %q, want integer", got)
	}
	if got := text.InputSchema.Properties["page"].Type; got != "integer" {
		t.Errorf("page type = %q, want integer", got)
	}
	defaults := map[string]any{
		"region":      "us-en",
		"safesearch":  "moderate",
		"max_results": float64(10),
		"page":        float64(1),
		"backend":     "auto",
	}
	for name, want := range defaults {
		prop := text.InputSchema.Properties[name]
		if prop.Default != want {
			t.Errorf("search_text.%s default = %#v, want %#v", name, prop.Default, want)
		}
	}
	if prop, ok := text.InputSchema.Properties["timelimit"]; !ok {
		t.Error("timelimit missing")
	} else if prop.Default != nil {
		t.Errorf("timelimit default = %#v, want none (str | None = None)", prop.Default)
	}

	// search_books must NOT expose region/safesearch/timelimit (mcp.py quirk).
	books := byName["search_books"]
	for _, forbidden := range []string{"region", "safesearch", "timelimit"} {
		if _, ok := books.InputSchema.Properties[forbidden]; ok {
			t.Errorf("search_books must not have %q", forbidden)
		}
	}
	if got := books.InputSchema.Properties["query"].Description; got != "Books search query string" {
		t.Errorf("books query description = %q", got)
	}

	if got := byName["extract_content"].Description; got != "Extract content from a URL." {
		t.Errorf("extract_content description = %q", got)
	}
	if got := byName["extract_content"].InputSchema.Properties["fmt"].Default; got != "text_markdown" {
		t.Errorf("extract_content.fmt default = %#v, want text_markdown", got)
	}

	// Per-category query descriptions differ in mcp.py.
	wantQueryDesc := map[string]string{
		"search_text":   "Search query string",
		"search_images": "Image search query string",
		"search_news":   "News search query string",
		"search_videos": "Video search query string",
		"search_books":  "Books search query string",
	}
	for tool, want := range wantQueryDesc {
		if got := byName[tool].InputSchema.Properties["query"].Description; got != want {
			t.Errorf("%s.query description = %q, want %q", tool, got, want)
		}
	}
}

// TestToolsListStableJSON ensures the payload can be re-marshalled (no NaN or
// non-marshallable defaults) and keeps exactly six entries.
func TestToolsListStableJSON(t *testing.T) {
	data, err := json.Marshal(Tools())
	if err != nil {
		t.Fatalf("marshal Tools(): %v", err)
	}
	var back []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal Tools(): %v", err)
	}
	if len(back) != 6 {
		t.Fatalf("got %d tools, want 6", len(back))
	}
}
