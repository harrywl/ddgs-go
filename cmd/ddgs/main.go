// Command ddgs is the CLI for the ddgs metasearch library, mirroring
// ddgs/cli.py of the Python upstream (deedy5/ddgs v9.16.0).
//
// Subcommands: version, text, images, videos, news, books, extract, mcp, api.
// Flags reproduce cli.py's @click.option declarations, including the short and
// long spellings, the per-command backend choices and the category-specific
// filters.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	ddgs "github.com/harrywl/ddgs-go"
	"github.com/harrywl/ddgs-go/internal/output"
)

// usageText is printed for `ddgs` and `ddgs help`. It mirrors the click group
// help plus the command list.
const usageText = `Usage: ddgs <command> [options]

DDGS CLI tool.

Commands:
  version    Print the library version.
  text       Perform a text metasearch.
  images     Perform an images metasearch.
  videos     Perform a videos metasearch.
  news       Perform a news metasearch.
  books      Perform a books metasearch.
  extract    Extract content from a URL.
  mcp        Start the MCP server over stdio.
  api        Start/stop the HTTP API server.

Run 'ddgs <command> --help' for the options of a command.

Examples:
  ddgs text -q "golang context" -m 5
  ddgs images -q cat -size Large -o json
  ddgs news -q ai -t d -b duckduckgo -b bing
  ddgs extract -u https://go.dev/doc/effective_go -f text_markdown
  ddgs api --port 4479
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		// `-h`/`--help` on a subcommand makes the flag package return
		// flag.ErrHelp after printing usage; click exits 0 in that case.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "%s\n", err)
		var ue *usageError
		if errors.As(err, &ue) {
			os.Exit(2) // click's UsageError exit status
		}
		os.Exit(1)
	}
}

// usageError marks click UsageError-equivalent failures (bad option, bad
// choice, missing required option, unknown command). click exits with status
// 2 for those, and 0/1 for command results.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// usagef builds a usageError.
func usagef(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}

// asUsageError tags err as a usage error unless it is flag.ErrHelp (which the
// caller turns into a successful exit).
func asUsageError(err error) error {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return err
	}
	return &usageError{err: err}
}

// run dispatches one subcommand, mirroring click's group behaviour. An unknown
// or missing command prints the usage text and fails, like click does.
func run(args []string) error {
	if len(args) == 0 {
		// click's Group prints its help and exits 2 when no command is given.
		fmt.Fprint(os.Stderr, usageText)
		return usagef("no command given")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Println(ddgs.LibraryVersion())
		return nil
	case "text":
		return runSearch("text", rest)
	case "images":
		return runSearch("images", rest)
	case "videos":
		return runSearch("videos", rest)
	case "news":
		return runSearch("news", rest)
	case "books":
		return runSearch("books", rest)
	case "extract":
		return runExtract(rest)
	case "mcp":
		return runMCP(rest)
	case "api":
		return runAPI(rest)
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return nil
	default:
		fmt.Fprint(os.Stderr, usageText)
		return usagef("no such command %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// Flag plumbing
// ---------------------------------------------------------------------------

// backendList collects a repeatable -b/--backend flag into a comma-joined
// string, mirroring cli.py's multiple=True + _convert_tuple_to_csv.
type backendList []string

func (b *backendList) String() string { return strings.Join(*b, ",") }

func (b *backendList) Set(v string) error {
	// click's multiple=True Choices validate each occurrence on its own, so
	// `-b bing,duckduckgo` is an invalid choice in cli.py — only repeated
	// -b flags are accepted. Keep that behaviour.
	*b = append(*b, v)
	return nil
}

// searchFlags holds the flags shared by the five search commands. Not every
// command registers every field — books has no region/safesearch/timelimit,
// and only text/images have the download flags. The subsets mirror cli.py.
type searchFlags struct {
	query      string
	keywords   string // deprecated alias, cli.py -k/--keywords
	region     string
	safesearch string
	timelimit  string
	maxResults int
	page       int
	backends   backendList
	output     string
	proxy      string
	verify     bool
	noColor    bool

	// text/images only
	download    bool
	downloadDir string
	threads     int

	// images only
	size       string
	color      string
	typeImage  string
	layout     string
	licenseImg string

	// videos only
	resolution    string
	duration      string
	licenseVideos string
}

// stringFlag registers the same variable under both the short and the long
// spelling (Go's flag package treats -x and --x identically, so registering
// "q" and "query" covers -q/--q/-query/--query).
func stringFlag(fs *flag.FlagSet, p *string, short, long, value, usage string) {
	if short != "" {
		fs.StringVar(p, short, value, usage)
	}
	if long != "" && long != short {
		fs.StringVar(p, long, value, usage)
	}
}

func boolFlag(fs *flag.FlagSet, p *bool, short, long string, value bool, usage string) {
	if short != "" {
		fs.BoolVar(p, short, value, usage)
	}
	if long != "" && long != short {
		fs.BoolVar(p, long, value, usage)
	}
}

func intFlag(fs *flag.FlagSet, p *int, short, long string, value int, usage string) {
	if short != "" {
		fs.IntVar(p, short, value, usage)
	}
	if long != "" && long != short {
		fs.IntVar(p, long, value, usage)
	}
}

// boolishValue mirrors click's BOOL option type, which cli.py gets implicitly
// for `-v/--verify` (default=True, no is_flag): the option takes a value and
// accepts true/false, 1/0, yes/no, on/off, t/f, y/n (case-insensitive).
// It deliberately does not implement flag.Getter/IsBoolFlag, so a bare `-v`
// errors exactly like click's "Option '-v' requires an argument".
type boolishValue struct{ p *bool }

func (b boolishValue) String() string {
	if b.p == nil {
		return "false"
	}
	if *b.p {
		return "true"
	}
	return "false"
}

func (b boolishValue) Set(s string) error {
	v, ok := parseBoolish(s)
	if !ok {
		return fmt.Errorf("invalid boolean value %q", s)
	}
	*b.p = v
	return nil
}

// parseBoolish maps click.BOOL's accepted spellings to a bool.
func parseBoolish(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "t", "yes", "y", "on":
		return true, true
	case "0", "false", "f", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

// boolishFlag registers a click-BOOL-style option (takes a value).
func boolishFlag(fs *flag.FlagSet, p *bool, short, long string, value bool, usage string) {
	*p = value
	if short != "" {
		fs.Var(boolishValue{p: p}, short, usage)
	}
	if long != "" && long != short {
		fs.Var(boolishValue{p: p}, long, usage)
	}
}

// backendChoices mirrors the click.Choice list of each command's -b flag.
// "yandex" stays listed for text even though the engine is disabled upstream:
// cli.py still offers it, and the library warns and falls back to auto.
var backendChoices = map[string][]string{
	"text":   {"auto", "all", "bing", "brave", "duckduckgo", "google", "grokipedia", "mojeek", "startpage", "yandex", "yahoo", "wikipedia"},
	"images": {"auto", "all", "bing", "duckduckgo"},
	"videos": {"auto", "all", "duckduckgo"},
	"news":   {"auto", "all", "bing", "duckduckgo", "yahoo"},
	"books":  {"auto", "all", "annasarchive"},
}

// choiceValues mirrors the remaining click.Choice options per command.
var (
	safesearchChoices = []string{"on", "moderate", "off"}
	timelimitChoices  = []string{"d", "w", "m", "y"}
	videoTimeChoices  = []string{"d", "w", "m"} // cli.py videos: no "y"
	sizeChoices       = []string{"Small", "Medium", "Large", "Wallpaper"}
	colorChoices      = []string{
		"color", "Monochrome", "Red", "Orange", "Yellow", "Green", "Blue",
		"Purple", "Pink", "Brown", "Black", "Gray", "Teal", "White",
	}
	typeImageChoices    = []string{"photo", "clipart", "gif", "transparent", "line"}
	layoutChoices       = []string{"Square", "Tall", "Wide"}
	licenseImageChoices = []string{"any", "Public", "Share", "ShareCommercially", "Modify", "ModifyCommercially"}
	resolutionChoices   = []string{"high", "standart"}
	durationChoices     = []string{"short", "medium", "long"}
	licenseVideoChoices = []string{"creativeCommon", "youtube"}
	extractFmtChoices   = []string{"text_markdown", "text_plain", "text_rich", "text", "content"}
)

// checkChoice mirrors click.Choice validation: an empty value means the option
// was not given (all these options default to None in cli.py).
func checkChoice(name, value string, allowed []string) error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("invalid value for %s: %q (choose from %s)", name, value, strings.Join(allowed, ", "))
}

// ---------------------------------------------------------------------------
// Search commands
// ---------------------------------------------------------------------------

// parseSearchFlags registers exactly the options cli.py declares for one
// search command and parses args, applying click.Choice validation. It is
// split out of runSearch so the per-command flag tree can be tested without
// touching the network.
func parseSearchFlags(category string, args []string) (*searchFlags, error) {
	fs, f := newSearchFlagSet(category)
	if err := fs.Parse(args); err != nil {
		return nil, asUsageError(err)
	}
	if fs.NArg() > 0 {
		return nil, usagef("unexpected argument %q", fs.Arg(0))
	}
	if err := validateSearchFlags(category, f); err != nil {
		return nil, asUsageError(err)
	}
	return f, nil
}

// newSearchFlagSet builds the FlagSet and flag struct for a search command.
// Splitting it from parsing lets tests enumerate the registered options and
// compare them with cli.py's @click.option list.
func newSearchFlagSet(category string) (*flag.FlagSet, *searchFlags) {
	f := &searchFlags{}
	fs := flag.NewFlagSet(category, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// Flags common to all five commands.
	stringFlag(fs, &f.query, "q", "query", "", category+" search query")
	stringFlag(fs, &f.keywords, "k", "keywords", "", "(Deprecated) "+category+" search query")
	intFlag(fs, &f.maxResults, "m", "max_results", 10, "maximum number of results")
	intFlag(fs, &f.page, "p", "page", 1, "page number of results")
	fs.Var(&f.backends, "b", "search backend (repeatable): "+strings.Join(backendChoices[category], ", "))
	fs.Var(&f.backends, "backend", "search backend (repeatable): "+strings.Join(backendChoices[category], ", "))
	stringFlag(fs, &f.output, "o", "output", "", "csv, json or filename.csv|json (save the results to a file)")
	stringFlag(fs, &f.proxy, "pr", "proxy", "", "the proxy to send requests, example: socks5h://127.0.0.1:9150")
	// cli.py: @click.option("-v", "--verify", default=True) — a BOOL-valued
	// option, not a flag, so it takes a value ("-v false").
	boolishFlag(fs, &f.verify, "v", "verify", true, "verify SSL when making the request")
	boolFlag(fs, &f.noColor, "nc", "no-color", false, "disable color output")

	// books has no region/safesearch/timelimit in cli.py.
	if category != "books" {
		stringFlag(fs, &f.region, "r", "region", "us-en", "us-en, ru-ru, etc.")
		stringFlag(fs, &f.safesearch, "s", "safesearch", "moderate", "on, moderate, off")
		tl := timelimitChoices
		tlUsage := "day, week, month, year"
		if category == "videos" {
			tl = videoTimeChoices
			tlUsage = "day, week, month"
		}
		stringFlag(fs, &f.timelimit, "t", "timelimit", "", tlUsage+" ("+strings.Join(tl, ", ")+")")
	}

	// Download flags: text and images only.
	if category == "text" || category == "images" {
		boolFlag(fs, &f.download, "d", "download", false, "download results. -dd to set custom directory")
		stringFlag(fs, &f.downloadDir, "dd", "download-directory", "", "specify custom download directory")
		intFlag(fs, &f.threads, "th", "threads", 10, "download threads, default=10")
	}

	// Category-specific filters.
	switch category {
	case "images":
		stringFlag(fs, &f.size, "size", "size", "", "image size ("+strings.Join(sizeChoices, ", ")+")")
		stringFlag(fs, &f.color, "c", "color", "", "image color filter")
		stringFlag(fs, &f.typeImage, "type", "type_image", "", "image type ("+strings.Join(typeImageChoices, ", ")+")")
		stringFlag(fs, &f.layout, "l", "layout", "", "image layout ("+strings.Join(layoutChoices, ", ")+")")
		stringFlag(fs, &f.licenseImg, "lic", "license_image", "", "image license filter")
	case "videos":
		stringFlag(fs, &f.resolution, "res", "resolution", "", "video resolution ("+strings.Join(resolutionChoices, ", ")+")")
		// cli.py reuses -d for --duration here (videos has no download flag).
		stringFlag(fs, &f.duration, "d", "duration", "", "video duration ("+strings.Join(durationChoices, ", ")+")")
		stringFlag(fs, &f.licenseVideos, "lic", "license_videos", "", "video license ("+strings.Join(licenseVideoChoices, ", ")+")")
	}

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ddgs %s [options]\n\nOptions:\n", category)
		fs.PrintDefaults()
	}
	return fs, f
}

// runSearch implements text/images/videos/news/books. The per-command flag
// sets and post-search actions follow cli.py exactly.
func runSearch(category string, args []string) error {
	f, err := parseSearchFlags(category, args)
	if err != nil {
		return err
	}

	query := effectiveQuery(f)

	d, err := ddgs.New(&ddgs.Config{Proxy: f.proxy, Verify: verifyOf(f.verify)})
	if err != nil {
		return err
	}
	opts := searchOptionsOf(category, f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results, err := dispatchSearch(ctx, d, category, query, opts)
	if err != nil {
		return err
	}

	// cli.py sanitizes the query for filenames (books passes it through raw).
	fileQuery := query
	if category != "books" {
		fileQuery = output.SanitizeQuery(query)
	}

	// cli.py::text/images use two independent `if` blocks (save AND download
	// may both run); videos/news/books have no download branch at all.
	if category == "text" || category == "images" {
		if f.output != "" {
			if _, err := output.SaveData(fileQuery, results, category, category, f.output); err != nil {
				return err
			}
		}
		if f.download {
			if _, _, err := output.DownloadResults(fileQuery, results, category, category,
				f.proxy, f.downloadDir, f.threads, f.verify); err != nil {
				return err
			}
		}
		if f.output == "" && !f.download {
			output.PrintData(results, category, f.noColor)
		}
		return nil
	}

	if f.output != "" {
		_, err := output.SaveData(fileQuery, results, category, category, f.output)
		return err
	}
	output.PrintData(results, category, f.noColor)
	return nil
}

// effectiveQuery mirrors cli.py's `query = keywords or query`: the deprecated
// -k/--keywords alias wins when given. The library then rejects an empty
// result with "query is mandatory", exactly like Python.
func effectiveQuery(f *searchFlags) string {
	if f.keywords != "" {
		return f.keywords
	}
	return f.query
}

// dispatchSearch calls the library method for a category.
func dispatchSearch(ctx context.Context, d *ddgs.DDGS, category, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error) {
	switch category {
	case "text":
		return d.Text(ctx, query, opts)
	case "images":
		return d.Images(ctx, query, opts)
	case "videos":
		return d.Videos(ctx, query, opts)
	case "news":
		return d.News(ctx, query, opts)
	case "books":
		return d.Books(ctx, query, opts)
	default:
		return nil, fmt.Errorf("unknown category %q", category)
	}
}

// searchOptionsOf builds ddgs.SearchOptions from the parsed flags. Backend
// defaults to "auto" when -b was not given, mirroring cli.py's
// default=["auto"]. Category filters go into Extra, which is how the library
// passes Python's **kwargs to the engines.
func searchOptionsOf(category string, f *searchFlags) *ddgs.SearchOptions {
	backend := "auto"
	if len(f.backends) > 0 {
		backend = f.backends.String()
	}
	maxResults := f.maxResults
	if maxResults <= 0 {
		// cli.py passes the int straight through and the library treats a
		// falsy max_results as "everything"; SPEC §9.5 spells that -1.
		maxResults = -1
	}
	opts := &ddgs.SearchOptions{
		MaxResults: maxResults,
		Page:       f.page,
		Backend:    backend,
	}
	if category != "books" {
		opts.Region = f.region
		opts.SafeSearch = f.safesearch
		opts.TimeLimit = f.timelimit
	}

	extra := map[string]string{}
	switch category {
	case "images":
		setIfGiven(extra, "size", f.size)
		setIfGiven(extra, "color", f.color)
		setIfGiven(extra, "type_image", f.typeImage)
		setIfGiven(extra, "layout", f.layout)
		setIfGiven(extra, "license_image", f.licenseImg)
	case "videos":
		setIfGiven(extra, "resolution", f.resolution)
		setIfGiven(extra, "duration", f.duration)
		setIfGiven(extra, "license_videos", f.licenseVideos)
	}
	if len(extra) > 0 {
		opts.Extra = extra
	}
	return opts
}

func setIfGiven(m map[string]string, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// validateSearchFlags reproduces the click.Choice validations of cli.py.
func validateSearchFlags(category string, f *searchFlags) error {
	for _, b := range f.backends {
		// click validates each occurrence of a multiple=True Choice, so an
		// explicitly empty -b is a usage error, not "option not given".
		if b == "" {
			return fmt.Errorf("invalid value for -b/--backend: %q", b)
		}
		if err := checkChoice("-b/--backend", b, backendChoices[category]); err != nil {
			return err
		}
	}
	if category != "books" {
		if err := checkChoice("-s/--safesearch", f.safesearch, safesearchChoices); err != nil {
			return err
		}
		allowed := timelimitChoices
		if category == "videos" {
			allowed = videoTimeChoices
		}
		if err := checkChoice("-t/--timelimit", f.timelimit, allowed); err != nil {
			return err
		}
	}
	switch category {
	case "images":
		for _, c := range []struct {
			name, value string
			allowed     []string
		}{
			{"-size/--size", f.size, sizeChoices},
			{"-c/--color", f.color, colorChoices},
			{"-type/--type_image", f.typeImage, typeImageChoices},
			{"-l/--layout", f.layout, layoutChoices},
			{"-lic/--license_image", f.licenseImg, licenseImageChoices},
		} {
			if err := checkChoice(c.name, c.value, c.allowed); err != nil {
				return err
			}
		}
	case "videos":
		for _, c := range []struct {
			name, value string
			allowed     []string
		}{
			{"-res/--resolution", f.resolution, resolutionChoices},
			{"-d/--duration", f.duration, durationChoices},
			{"-lic/--license_videos", f.licenseVideos, licenseVideoChoices},
		} {
			if err := checkChoice(c.name, c.value, c.allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyOf maps the -v/--verify flag to ddgs.Verify (Python passes the bool
// straight to DDGS(verify=...)).
func verifyOf(verify bool) ddgs.Verify {
	if !verify {
		return ddgs.Verify{SkipVerify: true}
	}
	return ddgs.Verify{}
}
