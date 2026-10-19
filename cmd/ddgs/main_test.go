package main

// Flag-parsing tests for the ddgs CLI. The expectations are transcribed from
// ddgs/cli.py (v9.16.0): every assertion below corresponds to one
// @click.option in the Python source.

import (
	"errors"
	"flag"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	ddgs "github.com/harrywl/ddgs-go"
)

// TestSearchFlagSetsMatchClick enumerates every registered flag name and
// compares it with the opts of cli.py's @click.option decorators (dumped via
// `p = cmd.params; "/".join(p.opts)`). Both spellings must exist, and nothing
// else may be registered.
func TestSearchFlagSetsMatchClick(t *testing.T) {
	t.Parallel()

	common := []string{
		"q", "query", "k", "keywords",
		"m", "max_results", "p", "page",
		"b", "backend", "o", "output", "pr", "proxy",
		"v", "verify", "nc", "no-color",
	}
	scope := []string{"r", "region", "s", "safesearch", "t", "timelimit"}
	download := []string{"d", "download", "dd", "download-directory", "th", "threads"}
	images := []string{"size", "c", "color", "type", "type_image", "l", "layout", "lic", "license_image"}
	videos := []string{"res", "resolution", "d", "duration", "lic", "license_videos"}

	want := map[string][]string{
		"text":   concat(common, scope, download),
		"images": concat(common, scope, images, download),
		"videos": concat(common, scope, videos),
		"news":   concat(common, scope),
		"books":  common, // cli.py: books has no -r/-s/-t and no download flags
	}

	for category, names := range want {
		fs, _ := newSearchFlagSet(category)
		got := map[string]bool{}
		fs.VisitAll(func(f *flag.Flag) { got[f.Name] = true })
		if len(got) != len(names) {
			t.Errorf("%s registers %d flags, cli.py declares %d: got %v", category, len(got), len(names), keysOf(got))
		}
		for _, n := range names {
			if !got[n] {
				t.Errorf("%s: flag %q missing (cli.py declares it)", category, n)
			}
		}
	}
}

// TestFlagDefaultsMatchClick pins the default of every option that has one in
// cli.py (fs.Lookup(...).DefValue is the string form flag prints in -help).
func TestFlagDefaultsMatchClick(t *testing.T) {
	t.Parallel()

	fs, _ := newSearchFlagSet("text")
	want := map[string]string{
		"r":          "us-en",
		"region":     "us-en",
		"s":          "moderate",
		"safesearch": "moderate",
		"t":          "",
		"m":          "10",
		"p":          "1",
		"b":          "",
		"d":          "false",
		"th":         "10",
		"v":          "true",
		"nc":         "false",
	}
	for name, wantDefault := range want {
		f := fs.Lookup(name)
		if f == nil {
			t.Errorf("flag %q not registered", name)
			continue
		}
		if f.DefValue != wantDefault {
			t.Errorf("flag %q default = %q, want %q", name, f.DefValue, wantDefault)
		}
	}
}

// TestAPIFlagSetNames checks the api option tree (cli.py: -d/--detach,
// -s/--stop, --host, --port, --reload, -pr/--proxy).
func TestAPIFlagSetNames(t *testing.T) {
	t.Parallel()

	fs := newAPIFlagSet(&apiFlags{})
	got := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { got[f.Name] = true })
	for _, n := range []string{"d", "detach", "s", "stop", "host", "port", "reload", "pr", "proxy"} {
		if !got[n] {
			t.Errorf("api flag %q missing", n)
		}
	}
	if len(got) != 9 {
		t.Errorf("api registers %d flags, want 9: %v", len(got), keysOf(got))
	}
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestParseSearchFlagsDefaults checks the click defaults of each command.
func TestParseSearchFlagsDefaults(t *testing.T) {
	t.Parallel()

	for _, category := range []string{"text", "images", "videos", "news", "books"} {
		f, err := parseSearchFlags(category, nil)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", category, err)
		}
		if f.query != "" || f.keywords != "" {
			t.Errorf("%s: query/keywords default = %q/%q, want empty", category, f.query, f.keywords)
		}
		if f.maxResults != 10 {
			t.Errorf("%s: max_results default = %d, want 10", category, f.maxResults)
		}
		if f.page != 1 {
			t.Errorf("%s: page default = %d, want 1", category, f.page)
		}
		if len(f.backends) != 0 {
			t.Errorf("%s: backends default = %v, want none (SearchOptions.Backend falls back to auto)", category, f.backends)
		}
		if f.output != "" || f.proxy != "" {
			t.Errorf("%s: output/proxy default = %q/%q, want empty", category, f.output, f.proxy)
		}
		if !f.verify {
			t.Errorf("%s: -v/--verify default = false, want true", category)
		}
		if f.noColor {
			t.Errorf("%s: -nc default = true, want false", category)
		}
		if category != "books" {
			if f.region != "us-en" {
				t.Errorf("%s: region default = %q, want us-en", category, f.region)
			}
			if f.safesearch != "moderate" {
				t.Errorf("%s: safesearch default = %q, want moderate", category, f.safesearch)
			}
			if f.timelimit != "" {
				t.Errorf("%s: timelimit default = %q, want empty", category, f.timelimit)
			}
		}
		if category == "text" || category == "images" {
			if f.download {
				t.Errorf("%s: -d default = true, want false", category)
			}
			if f.downloadDir != "" {
				t.Errorf("%s: -dd default = %q, want empty", category, f.downloadDir)
			}
			if f.threads != 10 {
				t.Errorf("%s: -th default = %d, want 10", category, f.threads)
			}
		}
	}
}

// TestParseSearchFlagsRepresentative parses one representative arg set per
// command and checks both the flag struct and the resulting SearchOptions.
func TestParseSearchFlagsRepresentative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		category string
		args     []string
		want     ddgs.SearchOptions
		// extra flags to check on the struct
		check func(t *testing.T, f *searchFlags)
	}{
		{
			name:     "text",
			category: "text",
			args: []string{
				"-q", "golang context", "-r", "uk-en", "-s", "off", "-t", "w",
				"-m", "5", "-p", "2", "-b", "bing", "-b", "duckduckgo",
				"-o", "out.json", "-pr", "socks5h://127.0.0.1:9150", "-nc",
			},
			want: ddgs.SearchOptions{
				Region:     "uk-en",
				SafeSearch: "off",
				TimeLimit:  "w",
				MaxResults: 5,
				Page:       2,
				Backend:    "bing,duckduckgo",
			},
			check: func(t *testing.T, f *searchFlags) {
				if f.output != "out.json" {
					t.Errorf("output = %q, want out.json", f.output)
				}
				if f.proxy != "socks5h://127.0.0.1:9150" {
					t.Errorf("proxy = %q", f.proxy)
				}
				if !f.noColor {
					t.Error("noColor = false, want true")
				}
				if f.download || f.downloadDir != "" || f.threads != 10 {
					t.Errorf("download flags = %v/%q/%d", f.download, f.downloadDir, f.threads)
				}
			},
		},
		{
			name:     "images",
			category: "images",
			args: []string{
				"-q", "cat", "-m", "3", "-b", "duckduckgo",
				"-size", "Large", "-c", "Red", "-type", "photo", "-l", "Tall",
				"-lic", "Modify", "-d", "-dd", "/tmp/imgs", "-th", "4",
			},
			want: ddgs.SearchOptions{
				Region:     "us-en",
				SafeSearch: "moderate",
				MaxResults: 3,
				Page:       1,
				Backend:    "duckduckgo",
				Extra: map[string]string{
					"size":          "Large",
					"color":         "Red",
					"type_image":    "photo",
					"layout":        "Tall",
					"license_image": "Modify",
				},
			},
			check: func(t *testing.T, f *searchFlags) {
				if !f.download {
					t.Error("images -d should set download")
				}
				if f.downloadDir != "/tmp/imgs" {
					t.Errorf("downloadDir = %q", f.downloadDir)
				}
				if f.threads != 4 {
					t.Errorf("threads = %d, want 4", f.threads)
				}
			},
		},
		{
			name:     "videos",
			category: "videos",
			args: []string{
				"-q", "go tutorial", "-r", "de-de", "-s", "on", "-t", "m",
				"-m", "7", "-p", "3", "-b", "duckduckgo",
				"-res", "high", "-d", "short", "-lic", "youtube",
			},
			want: ddgs.SearchOptions{
				Region:     "de-de",
				SafeSearch: "on",
				TimeLimit:  "m",
				MaxResults: 7,
				Page:       3,
				Backend:    "duckduckgo",
				Extra: map[string]string{
					"resolution":     "high",
					"duration":       "short",
					"license_videos": "youtube",
				},
			},
			check: func(t *testing.T, f *searchFlags) {
				// cli.py reuses -d for --duration on videos: it is NOT the
				// download flag (videos has no download flag at all).
				if f.duration != "short" {
					t.Errorf("duration = %q, want short (-d maps to --duration)", f.duration)
				}
				if f.download {
					t.Error("videos -d must not set download")
				}
			},
		},
		{
			name:     "news",
			category: "news",
			args:     []string{"-q", "ai", "-r", "ru-ru", "-s", "on", "-t", "d", "-m", "2", "-p", "4", "-b", "yahoo"},
			want: ddgs.SearchOptions{
				Region:     "ru-ru",
				SafeSearch: "on",
				TimeLimit:  "d",
				MaxResults: 2,
				Page:       4,
				Backend:    "yahoo",
			},
		},
		{
			name:     "books",
			category: "books",
			args:     []string{"-q", "dune", "-m", "9", "-p", "2", "-b", "annasarchive"},
			want: ddgs.SearchOptions{
				// books has no region/safesearch/timelimit in cli.py
				MaxResults: 9,
				Page:       2,
				Backend:    "annasarchive",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseSearchFlags(tc.category, tc.args)
			if err != nil {
				t.Fatalf("parseSearchFlags(%s, %v): %v", tc.category, tc.args, err)
			}
			got := searchOptionsOf(tc.category, f)
			assertOptions(t, got, &tc.want)
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

// assertOptions compares the SearchOptions that the CLI produces.
func assertOptions(t *testing.T, got, want *ddgs.SearchOptions) {
	t.Helper()
	if got.Region != want.Region {
		t.Errorf("Region = %q, want %q", got.Region, want.Region)
	}
	if got.SafeSearch != want.SafeSearch {
		t.Errorf("SafeSearch = %q, want %q", got.SafeSearch, want.SafeSearch)
	}
	if got.TimeLimit != want.TimeLimit {
		t.Errorf("TimeLimit = %q, want %q", got.TimeLimit, want.TimeLimit)
	}
	if got.MaxResults != want.MaxResults {
		t.Errorf("MaxResults = %d, want %d", got.MaxResults, want.MaxResults)
	}
	if got.Page != want.Page {
		t.Errorf("Page = %d, want %d", got.Page, want.Page)
	}
	if got.Backend != want.Backend {
		t.Errorf("Backend = %q, want %q", got.Backend, want.Backend)
	}
	if len(got.Extra) != len(want.Extra) {
		t.Fatalf("Extra = %v, want %v", got.Extra, want.Extra)
	}
	for k, v := range want.Extra {
		if got.Extra[k] != v {
			t.Errorf("Extra[%q] = %q, want %q", k, got.Extra[k], v)
		}
	}
}

// TestBooksRejectsSearchScopeFlags asserts that -r/-s/-t are not defined for
// books (cli.py's books command declares only q/k/m/p/b/o/pr/v/nc).
func TestBooksRejectsSearchScopeFlags(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"-q", "dune", "-r", "ru-ru"},
		{"-q", "dune", "-s", "off"},
		{"-q", "dune", "-t", "d"},
	} {
		if _, err := parseSearchFlags("books", args); err == nil {
			t.Errorf("books %v: expected an unknown-flag error", args)
		}
	}
}

// TestNoDownloadFlagsOutsideTextAndImages asserts that -d/-dd/-th are only
// registered for text and images. On news/books -d is unknown; on videos -d
// is --duration (handled in TestParseSearchFlagsRepresentative).
func TestNoDownloadFlagsOutsideTextAndImages(t *testing.T) {
	t.Parallel()

	for _, category := range []string{"news", "books"} {
		for _, args := range [][]string{
			{"-q", "x", "-d"},
			{"-q", "x", "-dd", "/tmp/x"},
			{"-q", "x", "-th", "3"},
		} {
			if _, err := parseSearchFlags(category, args); err == nil {
				t.Errorf("%s %v: expected an unknown-flag error", category, args)
			}
		}
	}
}

// TestInvalidChoices asserts that every click.Choice list rejects values
// outside it, mirroring click's usage-error path.
func TestInvalidChoices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		category string
		args     []string
	}{
		{"text backend", "text", []string{"-q", "x", "-b", "nope"}},
		{"text comma backend", "text", []string{"-q", "x", "-b", "bing,duckduckgo"}},
		{"text safesearch", "text", []string{"-q", "x", "-s", "maybe"}},
		{"text timelimit", "text", []string{"-q", "x", "-t", "h"}},
		{"images backend", "images", []string{"-q", "x", "-b", "google"}},
		{"images size", "images", []string{"-q", "x", "-size", "huge"}},
		{"images color", "images", []string{"-q", "x", "-c", "chartreuse"}},
		{"images type", "images", []string{"-q", "x", "-type", "png"}},
		{"images layout", "images", []string{"-q", "x", "-l", "round"}},
		{"images license", "images", []string{"-q", "x", "-lic", "free"}},
		{"videos backend", "videos", []string{"-q", "x", "-b", "bing"}},
		// cli.py's videos timelimit choices are d/w/m — "y" is text-only
		{"videos timelimit y", "videos", []string{"-q", "x", "-t", "y"}},
		{"videos resolution", "videos", []string{"-q", "x", "-res", "4k"}},
		{"videos duration", "videos", []string{"-q", "x", "-d", "tiny"}},
		{"videos license", "videos", []string{"-q", "x", "-lic", "mit"}},
		{"news backend", "news", []string{"-q", "x", "-b", "brave"}},
		{"books backend", "books", []string{"-q", "x", "-b", "bing"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSearchFlags(tc.category, tc.args); err == nil {
				t.Fatalf("%s %v: expected a choice validation error", tc.category, tc.args)
			}
		})
	}
}

// TestValidChoicesAccepted is the positive counterpart: every listed choice is
// accepted, and the exact choice lists match cli.py.
func TestValidChoicesAccepted(t *testing.T) {
	t.Parallel()

	// cli.py: images -c has 14 choices, -lic has 6.
	if got := len(colorChoices); got != 14 {
		t.Errorf("images color choices = %d, want 14 (%v)", got, colorChoices)
	}
	if got := len(licenseImageChoices); got != 6 {
		t.Errorf("images license choices = %d, want 6 (%v)", got, licenseImageChoices)
	}
	// cli.py: videos -t is d/w/m only.
	if got := strings.Join(videoTimeChoices, ","); got != "d,w,m" {
		t.Errorf("videos timelimit choices = %q, want d,w,m", got)
	}
	// cli.py: text -b lists every backend including the disabled yandex.
	if got := strings.Join(backendChoices["text"], ","); got !=
		"auto,all,bing,brave,duckduckgo,google,grokipedia,mojeek,startpage,yandex,yahoo,wikipedia" {
		t.Errorf("text backend choices = %q", got)
	}

	for _, c := range colorChoices {
		if _, err := parseSearchFlags("images", []string{"-q", "x", "-c", c}); err != nil {
			t.Errorf("color %q rejected: %v", c, err)
		}
	}
	for _, c := range licenseImageChoices {
		if _, err := parseSearchFlags("images", []string{"-q", "x", "-lic", c}); err != nil {
			t.Errorf("license %q rejected: %v", c, err)
		}
	}
	for _, c := range timelimitChoices {
		if _, err := parseSearchFlags("text", []string{"-q", "x", "-t", c}); err != nil {
			t.Errorf("text timelimit %q rejected: %v", c, err)
		}
	}
	for _, c := range resolutionChoices {
		if _, err := parseSearchFlags("videos", []string{"-q", "x", "-res", c}); err != nil {
			t.Errorf("resolution %q rejected: %v", c, err)
		}
	}
}

// TestKeywordsOverridesQuery covers cli.py's `query = keywords or query`
// (the deprecated -k/--keywords alias wins when given).
func TestKeywordsOverridesQuery(t *testing.T) {
	t.Parallel()

	f, err := parseSearchFlags("text", []string{"-q", "new", "-k", "old"})
	if err != nil {
		t.Fatal(err)
	}
	if f.keywords != "old" || f.query != "new" {
		t.Fatalf("parsed = %q/%q, want old/new", f.keywords, f.query)
	}
	if got := effectiveQuery(f); got != "old" {
		t.Errorf("effectiveQuery = %q, want old", got)
	}

	f, err = parseSearchFlags("text", []string{"--keywords", "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveQuery(f); got != "legacy" {
		t.Errorf("effectiveQuery = %q, want legacy", got)
	}

	f, err = parseSearchFlags("text", []string{"-q", "only"})
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveQuery(f); got != "only" {
		t.Errorf("effectiveQuery = %q, want only", got)
	}
}

// TestMaxResultsZeroIsUnlimited: click passes -m 0 straight through, and the
// Python library treats a falsy max_results as "return everything".
func TestMaxResultsZeroIsUnlimited(t *testing.T) {
	t.Parallel()

	f, err := parseSearchFlags("text", []string{"-q", "x", "-m", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchOptionsOf("text", f).MaxResults; got != -1 {
		t.Errorf("MaxResults = %d, want -1 for -m 0", got)
	}
}

// TestEmptyBackendRejected: click validates every -b occurrence, so an empty
// value is a usage error rather than "not given".
func TestEmptyBackendRejected(t *testing.T) {
	t.Parallel()

	if _, err := parseSearchFlags("text", []string{"-q", "x", "-b", ""}); err == nil {
		t.Error("empty -b should be rejected")
	}
}

// TestMaxResultsNegativeIsUnlimited: cli.py passes -m straight through, and
// the library treats a falsy max_results as "everything" (SPEC §9.5 spells
// that MaxResults: -1).
func TestMaxResultsNegativeIsUnlimited(t *testing.T) {
	t.Parallel()

	f, err := parseSearchFlags("text", []string{"-q", "x", "-m", "-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchOptionsOf("text", f).MaxResults; got != -1 {
		t.Errorf("MaxResults = %d, want -1", got)
	}
}

// TestVerifyTakesValue mirrors @click.option("-v", "--verify", default=True):
// the option takes a value, and click's BOOL type accepts the usual spellings.
func TestVerifyTakesValue(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		arg  string
		want bool
	}{
		{"true", true}, {"True", true}, {"1", true}, {"yes", true}, {"on", true}, {"t", true},
		{"false", false}, {"False", false}, {"0", false}, {"no", false}, {"off", false}, {"f", false},
	} {
		f, err := parseSearchFlags("text", []string{"-q", "x", "-v", c.arg})
		if err != nil {
			t.Fatalf("-v %s: %v", c.arg, err)
		}
		if f.verify != c.want {
			t.Errorf("-v %s = %v, want %v", c.arg, f.verify, c.want)
		}
	}

	// click errors with "Option '-v' requires an argument".
	if _, err := parseSearchFlags("text", []string{"-q", "x", "-v"}); err == nil {
		t.Error("bare -v should require an argument")
	}
	// ...and rejects values that are not bool-ish.
	if _, err := parseSearchFlags("text", []string{"-q", "x", "-v", "maybe"}); err == nil {
		t.Error("-v maybe should be rejected")
	}
	// verifyOf(-v false) must disable TLS checks.
	if v := verifyOf(false); !v.SkipVerify {
		t.Errorf("verifyOf(false) = %+v, want SkipVerify", v)
	}
	if v := verifyOf(true); v.SkipVerify {
		t.Errorf("verifyOf(true) = %+v, want verification on", v)
	}
}

// TestRunVersion checks `ddgs version` prints ddgs.LibraryVersion().
func TestRunVersion(t *testing.T) {
	out := captureStdout(t, func() {
		if err := run([]string{"version"}); err != nil {
			t.Fatalf("run(version): %v", err)
		}
	})
	want := ddgs.LibraryVersion() + "\n"
	if out != want {
		t.Errorf("version output = %q, want %q", out, want)
	}
	// The Python __version__ is 9.16.0; the port appends its revision.
	if !strings.HasPrefix(out, "9.16.0") {
		t.Errorf("version = %q, want a 9.16.0 prefix", out)
	}
}

// TestRunUnknownCommand asserts click's "no such command" failure path.
func TestRunUnknownCommand(t *testing.T) {
	err := run([]string{"nope"})
	if err == nil {
		t.Fatal("run(nope) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "no such command") {
		t.Errorf("error = %q, want it to mention 'no such command'", err)
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Error("unknown command should be a usageError (click exits 2)")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the command", err)
	}
}

// TestRunNoCommand asserts the usage error when no subcommand is given.
func TestRunNoCommand(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("run(nil) = nil, want an error")
	}
}

// TestRunHelp asserts `ddgs help` (and -h/--help) succeed and print usage.
func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		out := captureStdout(t, func() {
			if err := run([]string{arg}); err != nil {
				t.Fatalf("run(%s): %v", arg, err)
			}
		})
		if !strings.Contains(out, "Usage: ddgs <command>") {
			t.Errorf("run(%s) output = %q, want the usage text", arg, out)
		}
		for _, cmd := range []string{"version", "text", "images", "videos", "news", "books", "extract", "mcp", "api"} {
			if !strings.Contains(out, cmd) {
				t.Errorf("usage text does not mention %q", cmd)
			}
		}
	}
}

// TestExtractFlagDefaults covers the extract command's option tree:
// -u/--url (required), -f/--format (default text_markdown), -o/--output,
// -pr/--proxy, -v/--verify.
func TestExtractFlagDefaults(t *testing.T) {
	t.Parallel()

	if err := checkChoice("-f/--format", "text_markdown", extractFmtChoices); err != nil {
		t.Errorf("default format rejected: %v", err)
	}
	for _, want := range []string{"text_markdown", "text_plain", "text_rich", "text", "content"} {
		found := false
		for _, c := range extractFmtChoices {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("extract format choices missing %q: %v", want, extractFmtChoices)
		}
	}
	if err := checkChoice("-f/--format", "markdown", extractFmtChoices); err == nil {
		t.Error("invalid extract format accepted")
	}
	// extract has no -nc option in cli.py.
	if err := runExtract([]string{"-u", "https://example.com", "-nc"}); err == nil {
		t.Error("extract -nc should be an unknown flag")
	}
	// -u is required.
	if err := runExtract(nil); err == nil {
		t.Error("extract without -u should fail")
	}
}

// TestAPIDefaults pins the api command's defaults (host 0.0.0.0, port 4479).
func TestAPIDefaults(t *testing.T) {
	t.Parallel()

	f := &apiFlags{}
	fs := newAPIFlagSet(f)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if f.host != "0.0.0.0" {
		t.Errorf("host default = %q, want 0.0.0.0", f.host)
	}
	if f.port != 4479 {
		t.Errorf("port default = %d, want 4479", f.port)
	}
	if f.detach || f.stop || f.reload {
		t.Errorf("bool defaults = %v/%v/%v, want false", f.detach, f.stop, f.reload)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	// Always restore, even if a Fatalf unwinds through here.
	t.Cleanup(func() { os.Stdout = old })
	fn()
	os.Stdout = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
