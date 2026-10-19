package mcpserver

// Tool definitions, mirroring the six @mcp.tool() functions in
// ddgs/api_server/mcp.py (v9.16.0). Everything here is a transcription of the
// Python signatures and docstrings:
//
//   - name        : the Python function name (FastMCP uses it verbatim)
//   - description : the first line of the Python docstring
//   - params      : the Python parameter list, in declaration order
//   - required    : parameters that have no default in the Python signature
//   - extra       : per-category filters that Python forwards as **kwargs and
//                   that this port carries in ddgs.SearchOptions.Extra
//
// Do not "improve" these schemas: mcp.py is the source of truth, including the
// quirk that search_books has no region/safesearch/timelimit parameters.

// Tool is one entry of the tools/list response. The JSON shape matches what
// the Python mcp SDK emits for a FastMCP tool.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema is a JSON Schema object describing a tool's arguments.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
}

// Property is a single inputSchema property. Python's `str | None = None`
// parameters become plain optional strings (no "default", not required),
// which is what the task specification asks for; FastMCP would emit an
// anyOf[string, null] union instead.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
}

// Parameter descriptions copied verbatim from the "Args:" sections of the
// Python docstrings in mcp.py.
const (
	descRegion      = "Region for search (e.g., us-en, uk-en, ru-ru)"
	descSafeSearch  = "Safe search setting (on, moderate, off)"
	descTimeLimit   = "Time limit (d, w, m, y) or custom date range"
	descMaxResults  = "Maximum number of results to return"
	descPage        = "Page number of results"
	descBackend     = "Search backend (auto, or specific engine)"
	descImageSize   = "Image size (Small, Medium, Large, Wallpaper)"
	descImageColor  = "Image color filter"
	descImageType   = "Image type (photo, clipart, gif, transparent, line)"
	descImageLayout = "Image layout (Square, Tall, Wide)"
	descImageLic    = "Image license filter"
	descResolution  = "Video resolution (high, standard)"
	descDuration    = "Video duration (short, medium, long)"
	descVideoLic    = "Video license (creativeCommon, youtube)"
	descURL         = "The URL to fetch and extract content from."
	descExtractFmt  = `Output format: "text_markdown", "text_plain", "text_rich", "text" (raw HTML), "content" (raw bytes).`
)

// Tool names, exactly as declared in mcp.py.
const (
	ToolSearchText     = "search_text"
	ToolSearchImages   = "search_images"
	ToolSearchNews     = "search_news"
	ToolSearchVideos   = "search_videos"
	ToolSearchBooks    = "search_books"
	ToolExtractContent = "extract_content"
)

// Category identifiers used internally to pick the Searcher method.
const (
	categoryText    = "text"
	categoryImages  = "images"
	categoryNews    = "news"
	categoryVideos  = "videos"
	categoryBooks   = "books"
	categoryExtract = "extract"
)

// paramSpec describes one Python parameter before it is turned into a
// Property. def is nil when the Python signature has no default (i.e. the
// parameter is optional only in the `str | None = None` sense).
type paramSpec struct {
	name     string
	jsonType string
	def      any
	desc     string
}

// toolSpec couples a Tool with the dispatch metadata.
type toolSpec struct {
	name        string
	description string
	category    string
	params      []paramSpec
	required    []string
	// extra lists the argument names forwarded into SearchOptions.Extra
	// (Python passes them as **kwargs to DDGS.images()/videos()).
	extra []string
}

// baseSearchParams mirrors the shared prefix of the five search tool
// signatures. Only the query description differs per category.
func baseSearchParams(queryDesc string) []paramSpec {
	return []paramSpec{
		{name: "query", jsonType: "string", desc: queryDesc},
		{name: "region", jsonType: "string", def: "us-en", desc: descRegion},
		{name: "safesearch", jsonType: "string", def: "moderate", desc: descSafeSearch},
		{name: "timelimit", jsonType: "string", desc: descTimeLimit},
		{name: "max_results", jsonType: "integer", def: 10, desc: descMaxResults},
		{name: "page", jsonType: "integer", def: 1, desc: descPage},
		{name: "backend", jsonType: "string", def: "auto", desc: descBackend},
	}
}

// imageFilterParams mirrors the image-only kwargs of search_images
// (size/color/type_image/layout/license_image, all `str | None = None`).
func imageFilterParams() []paramSpec {
	return []paramSpec{
		{name: "size", jsonType: "string", desc: descImageSize},
		{name: "color", jsonType: "string", desc: descImageColor},
		{name: "type_image", jsonType: "string", desc: descImageType},
		{name: "layout", jsonType: "string", desc: descImageLayout},
		{name: "license_image", jsonType: "string", desc: descImageLic},
	}
}

// videoFilterParams mirrors the video-only kwargs of search_videos
// (resolution/duration/license_videos, all `str | None = None`).
func videoFilterParams() []paramSpec {
	return []paramSpec{
		{name: "resolution", jsonType: "string", desc: descResolution},
		{name: "duration", jsonType: "string", desc: descDuration},
		{name: "license_videos", jsonType: "string", desc: descVideoLic},
	}
}

// toolSpecs is the ordered tool list returned by tools/list. Order follows
// mcp.py's declaration order (text, images, news, videos, books, extract).
var toolSpecs = []toolSpec{
	{
		name:        ToolSearchText,
		description: "Perform a text search using DDGS.",
		category:    categoryText,
		params:      baseSearchParams("Search query string"),
		required:    []string{"query"},
	},
	{
		name:        ToolSearchImages,
		description: "Perform an image search using DDGS.",
		category:    categoryImages,
		params:      append(baseSearchParams("Image search query string"), imageFilterParams()...),
		required:    []string{"query"},
		extra:       []string{"size", "color", "type_image", "layout", "license_image"},
	},
	{
		name:        ToolSearchNews,
		description: "Perform a news search using DDGS.",
		category:    categoryNews,
		params:      baseSearchParams("News search query string"),
		required:    []string{"query"},
	},
	{
		name:        ToolSearchVideos,
		description: "Perform a video search using DDGS.",
		category:    categoryVideos,
		params:      append(baseSearchParams("Video search query string"), videoFilterParams()...),
		required:    []string{"query"},
		extra:       []string{"resolution", "duration", "license_videos"},
	},
	{
		// mcp.py: search_books(query, max_results=10, page=1, backend="auto")
		// — deliberately no region/safesearch/timelimit.
		name:        ToolSearchBooks,
		description: "Perform a book search using DDGS.",
		category:    categoryBooks,
		params: []paramSpec{
			{name: "query", jsonType: "string", desc: "Books search query string"},
			{name: "max_results", jsonType: "integer", def: 10, desc: descMaxResults},
			{name: "page", jsonType: "integer", def: 1, desc: descPage},
			{name: "backend", jsonType: "string", def: "auto", desc: descBackend},
		},
		required: []string{"query"},
	},
	{
		name:        ToolExtractContent,
		description: "Extract content from a URL.",
		category:    categoryExtract,
		params: []paramSpec{
			{name: "url", jsonType: "string", desc: descURL},
			{name: "fmt", jsonType: "string", def: "text_markdown", desc: descExtractFmt},
		},
		required: []string{"url"},
	},
}

// toolSpecByName indexes toolSpecs for tools/call dispatch.
var toolSpecByName = func() map[string]*toolSpec {
	m := make(map[string]*toolSpec, len(toolSpecs))
	for i := range toolSpecs {
		m[toolSpecs[i].name] = &toolSpecs[i]
	}
	return m
}()

// Tools returns the tools/list payload: the six tools with schemas derived
// from the mcp.py signatures.
func Tools() []Tool {
	tools := make([]Tool, 0, len(toolSpecs))
	for i := range toolSpecs {
		tools = append(tools, toolSpecs[i].tool())
	}
	return tools
}

// tool converts the declarative spec into its wire representation.
func (s *toolSpec) tool() Tool {
	props := make(map[string]Property, len(s.params))
	for _, p := range s.params {
		props[p.name] = Property{Type: p.jsonType, Description: p.desc, Default: p.def}
	}
	return Tool{
		Name:        s.name,
		Description: s.description,
		InputSchema: InputSchema{
			Type:       "object",
			Properties: props,
			Required:   append([]string(nil), s.required...),
		},
	}
}
