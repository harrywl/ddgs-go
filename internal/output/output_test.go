package output

// Tests for the CLI output helpers. Every expected value in this file was
// derived by running the Python source of truth (ddgs/cli.py from
// py-ddgs @ v9.16.0) — see the comment above each table.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
)

// TestSanitizeQueryMatchesPython pins _sanitize_query byte-for-byte.
//
// Reference: python3 -c "from ddgs.cli import _sanitize_query; ..."
// (ordered replaces: filetype, ":", '"'→"'", site, " "→"_", "/"→"_",
// "\\"→"_", " "→"" — note the case-sensitive "filetype"/"site").
func TestSanitizeQueryMatchesPython(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		// filetype removed, ":" removed, "site" removed, "/" and "\" → "_"
		{"filetype:pdf site:x a b/c\\d", "pdf_x_a_b_c_d"},
		// double quotes become single quotes, ":" dropped
		{`a:b "q"`, "ab_'q'"},
		{"plain query", "plain_query"},
		{"site:example.com filetype:pdf \"hello world\" a/b\\c", "example.com_pdf_'hello_world'_a_b_c"},
		{"", ""},
		// spaces become "_", then the final replace(" ", "") is a no-op
		{"  ", "__"},
		{"a:b:c", "abc"},
		// replacements are case-sensitive in Python
		{"FILEtype:PDF", "FILEtypePDF"},
	}
	for _, c := range cases {
		if got := SanitizeQuery(c.in); got != c.want {
			t.Errorf("SanitizeQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResultFieldOrderMatchesPython verifies that ddgs.ResultFields mirrors
// the Python dataclass field order, which drives JSON key order, CSV headers
// and the printed column order.
//
// Reference: python3 -c "from ddgs.results import TextResult; import
// dataclasses; print([f.name for f in dataclasses.fields(TextResult)])"
func TestResultFieldOrderMatchesPython(t *testing.T) {
	t.Parallel()

	want := map[string][]string{
		"text":   {"title", "href", "body"},
		"images": {"title", "image", "thumbnail", "url", "height", "width", "source"},
		"news":   {"date", "title", "body", "url", "image", "source"},
		"videos": {
			"title", "content", "description", "duration", "embed_html", "embed_url",
			"image_token", "images", "provider", "published", "publisher", "statistics", "uploader",
		},
		"books": {"title", "author", "publisher", "info", "url", "thumbnail"},
	}
	for category, fields := range want {
		got := ddgs.ResultFields(category)
		if len(got) != len(fields) {
			t.Fatalf("%s: got %d fields %v, want %d", category, len(got), got, len(fields))
		}
		for i := range fields {
			if got[i] != fields[i] {
				t.Errorf("%s field %d = %q, want %q", category, i, got[i], fields[i])
			}
		}
	}
}

// TestSaveJSONMatchesPython checks the exact bytes of _save_json:
// json.dumps(data, ensure_ascii=False, indent=2) — 2-space indent, no
// trailing newline, keys in dataclass order, no HTML escaping.
func TestSaveJSONMatchesPython(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	results := []ddgs.Result{{
		"title": "T,1",
		"href":  "http://x/a?b=1",
		"body":  "line1\nline2 <&>",
	}}

	if err := SaveJSON(path, results, "text"); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Reference (Python):
	//   json.dumps([{"title":"T,1","href":"http://x/a?b=1",
	//                "body":"line1\nline2 <&>"}], ensure_ascii=False, indent=2)
	want := "[\n" +
		"  {\n" +
		"    \"title\": \"T,1\",\n" +
		"    \"href\": \"http://x/a?b=1\",\n" +
		"    \"body\": \"line1\\nline2 <&>\"\n" +
		"  }\n" +
		"]"
	if string(got) != want {
		t.Errorf("JSON mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestSaveJSONPreservesFieldOrderOnDisk asserts key order independently of
// the byte comparison above, by decoding into an ordered key slice.
func TestSaveJSONPreservesFieldOrderOnDisk(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "order.json")
	// A Go map iterates in random order, so the writer must impose the
	// dataclass order rather than marshal the map as-is.
	r := ddgs.Result{"body": "b", "href": "h", "title": "t"}
	if err := SaveJSON(path, []ddgs.Result{r}, "text"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := keyOrder(t, raw)
	want := []string{"title", "href", "body"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("key order = %v, want %v (raw: %s)", got, want, raw)
	}
}

// keyOrder returns the top-level keys of the first object in a JSON array,
// in document order.
func keyOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		t.Fatalf("expected JSON array, got %v", tok)
	}
	tok, err = dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("expected JSON object, got %v", tok)
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, kt.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// TestSaveJSONEmptyList checks the empty-result case: Python writes "[]".
func TestSaveJSONEmptyList(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.json")
	if err := SaveJSON(path, nil, "text"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[]" {
		t.Errorf("empty JSON = %q, want %q", got, "[]")
	}
}

// TestSaveCSVMatchesPython checks _save_csv: header from the dataclass fields,
// QUOTE_MINIMAL quoting, and the excel dialect's CRLF line terminator.
func TestSaveCSVMatchesPython(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "out.csv")
	results := []ddgs.Result{{
		"title": "T,1",
		"href":  "http://x/a?b=1",
		"body":  "line1\nline2 <&>",
	}}
	if err := SaveCSV(path, results, "text"); err != nil {
		t.Fatalf("SaveCSV: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Reference (Python): open(newline=""); csv.DictWriter(writer).writeheader()
	// + writerows([{"title":"T,1","href":"http://x/a?b=1",
	//               "body":"line1\nline2 <&>"}])
	want := "title,href,body\r\n" +
		"\"T,1\",http://x/a?b=1,\"line1\nline2 <&>\"\r\n"
	if string(got) != want {
		t.Errorf("CSV mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestSaveCSVHeaderUsesResultFields checks that the header is exactly
// ddgs.ResultFields(category) for every known category.
func TestSaveCSVHeaderUsesResultFields(t *testing.T) {
	t.Parallel()

	for _, category := range []string{"text", "images", "news", "videos", "books"} {
		path := filepath.Join(t.TempDir(), category+".csv")
		if err := SaveCSV(path, []ddgs.Result{{}}, category); err != nil {
			t.Fatalf("%s: SaveCSV: %v", category, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Python writes the header, then one (all-empty) row for the single
		// empty result supplied above.
		want := strings.Join(ddgs.ResultFields(category), ",") + "\r\n" +
			strings.Repeat(",", len(ddgs.ResultFields(category))-1) + "\r\n"
		if string(got) != want {
			t.Errorf("%s header = %q, want %q", category, got, want)
		}
	}
}

// TestSaveCSVQuotesLikePython pins QUOTE_MINIMAL against the cases where it
// differs from encoding/csv: leading/trailing spaces stay unquoted in Python,
// and CRLF conversion inside quoted fields.
func TestSaveCSVQuotesLikePython(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"with,comma", `"with,comma"`},
		{`with"quote`, `"with""quote"`},
		{"line\nbreak", "\"line\nbreak\""},
		{"line\rbreak", "\"line\rbreak\""},
		{" leading space", " leading space"},
	}
	for _, c := range cases {
		if got := csvQuote(c.in); got != c.want {
			t.Errorf("csvQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSaveDataFilenames mirrors cli.py::_save_data's rsplit logic:
//
//	filename, ext = filename.rsplit(".", 1) if filename.endswith((".csv",".json"))
//	                                    else (None, filename)
//	filename = filename or f"{function_name}_{query}_{utc:%Y%m%d_%H%M%S}"
func TestSaveDataFilenames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	results := []ddgs.Result{{"title": "t", "href": "h", "body": "b"}}

	// "-o json" → auto-named <func>_<query>_<UTC timestamp>.json
	path, err := SaveData("my_query", results, "text", "text", "json")
	if err != nil {
		t.Fatalf("SaveData(json): %v", err)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "text_my_query_") || !strings.HasSuffix(base, ".json") {
		t.Errorf("auto JSON name = %q, want text_my_query_<ts>.json", base)
	}
	// text_my_query_YYYYmmdd_HHMMSS.json → 2 underscores + 15-char stamp
	if len(base) != len("text_my_query_")+15+len(".json") {
		t.Errorf("auto JSON name %q has unexpected shape", base)
	}
	if _, err := os.Stat(filepath.Join(dir, base)); err == nil {
		t.Errorf("auto-named file should be written to the CWD, not %s", dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("auto-named file %q not created: %v", path, err)
	}
	autoJSON := path // capture before `path` is reused below
	t.Cleanup(func() { _ = os.Remove(autoJSON) })

	// "-o csv" → auto-named .csv
	pathCSV, err := SaveData("q", results, "images", "images", "csv")
	if err != nil {
		t.Fatalf("SaveData(csv): %v", err)
	}
	if !strings.HasPrefix(filepath.Base(pathCSV), "images_q_") || !strings.HasSuffix(pathCSV, ".csv") {
		t.Errorf("auto CSV name = %q, want images_q_<ts>.csv", pathCSV)
	}
	t.Cleanup(func() { _ = os.Remove(pathCSV) })

	// "-o out.json" → exactly out.json, no timestamp
	explicit := filepath.Join(dir, "out.json")
	path, err = SaveData("q", results, "text", "text", explicit)
	if err != nil {
		t.Fatalf("SaveData(out.json): %v", err)
	}
	if path != explicit {
		t.Errorf("explicit path = %q, want %q", path, explicit)
	}
	if _, err := os.Stat(explicit); err != nil {
		t.Errorf("explicit file not created: %v", err)
	}

	// "-o out.txt" → Python's _save_data has no else branch: nothing is
	// written and no error is raised.
	unsupported := filepath.Join(dir, "out.txt")
	path, err = SaveData("q", results, "text", "text", unsupported)
	if err != nil {
		t.Fatalf("SaveData(out.txt) should be a silent no-op, got error %v", err)
	}
	if path != "" {
		t.Errorf("SaveData(out.txt) returned path %q, want empty", path)
	}
	if _, err := os.Stat(unsupported); !os.IsNotExist(err) {
		t.Errorf("out.txt should not be written, stat err = %v", err)
	}
}

// TestSplitOutputFilename pins the rsplit branch selection directly.
func TestSplitOutputFilename(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, base, ext string }{
		{"", "", ""},
		{"json", "", "json"},
		{"csv", "", "csv"},
		{"out.json", "out", "json"},
		{"out.csv", "out", "csv"},
		{"dir/sub/out.json", "dir/sub/out", "json"},
		{"name.with.dots.json", "name.with.dots", "json"},
		{"out.txt", "", "out.txt"},
		// Python only treats a trailing .csv/.json as an extension
		{"out.json.bak", "", "out.json.bak"},
	}
	for _, c := range cases {
		base, ext := splitOutputFilename(c.in)
		if base != c.base || ext != c.ext {
			t.Errorf("splitOutputFilename(%q) = (%q,%q), want (%q,%q)", c.in, base, ext, c.base, c.ext)
		}
	}
}

// TestPrintDataNonTTY pins _print_data's non-interactive output: no colours,
// no "=" separator, no input() pause.
//
// Reference: io.StringIO capture of python's _print_data on the same data.
func TestPrintDataNonTTY(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		results []ddgs.Result
		want    string
	}{
		{
			// cli.py prints every field, including empty ones: the `if v:`
			// branch only controls wrapping, not printing.
			name:    "empty fields still print",
			results: []ddgs.Result{{"title": "Title", "href": "", "body": "Body text"}},
			want: "1.\n" +
				"title       Title\n" +
				"href        \n" +
				"body        Body text\n",
		},
		{
			name: "two results separated by N.",
			results: []ddgs.Result{
				{"title": "A", "href": "h", "body": "b"},
				{"title": "C", "href": "d", "body": "e"},
			},
			want: "1.\n" +
				"title       A\n" +
				"href        h\n" +
				"body        b\n" +
				"2.\n" +
				"title       C\n" +
				"href        d\n" +
				"body        e\n",
		},
		{
			name:    "no results prints nothing",
			results: nil,
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintDataTo(&buf, strings.NewReader(""), c.results, "text", false)
			if got := buf.String(); got != c.want {
				t.Errorf("print mismatch\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestPrintDataWrapping pins the click.wrap_text call:
// width=78 (300 for href/image/source/thumbnail/url/content),
// subsequent_indent=" "*12, preserve_paragraphs=True.
func TestPrintDataWrapping(t *testing.T) {
	t.Parallel()

	body := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho"
	para := "first para line one\nline two here\n\nsecond para"
	longWord := strings.Repeat("a", 90)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// Reference (Python): the continuation line starts with exactly
			// 12 spaces and holds width-12 = 66 characters.
			name: "continuation indent",
			body: body,
			want: "1.\n" +
				"title       Title\n" +
				"href        \n" +
				"body        alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi\n" +
				"            omicron pi rho\n",
		},
		{
			// preserve_paragraphs: lines inside a paragraph are re-flowed,
			// paragraphs are separated by exactly one blank line.
			name: "paragraphs preserved",
			body: para,
			want: "1.\n" +
				"title       Title\n" +
				"href        \n" +
				"body        first para line one line two here\n" +
				"\n" +
				"second para\n",
		},
		{
			// break_long_words=True: 78 chars on the first line, the rest
			// indented by 12.
			name: "long word broken",
			body: longWord,
			want: "1.\n" +
				"title       Title\n" +
				"href        \n" +
				"body        " + strings.Repeat("a", 78) + "\n" +
				"            " + strings.Repeat("a", 12) + "\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintDataTo(&buf, nil, []ddgs.Result{{"title": "Title", "href": "", "body": c.body}}, "text", false)
			if got := buf.String(); got != c.want {
				t.Errorf("print mismatch\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestPrintDataFieldWidths checks the 300-column branch: href wraps at 300,
// body at 78.
func TestPrintDataFieldWidths(t *testing.T) {
	t.Parallel()

	// 200 chars of "x": one line at width 300, three lines at width 78.
	long := strings.Repeat("x", 200)

	var buf bytes.Buffer
	PrintDataTo(&buf, nil, []ddgs.Result{{"title": "T", "href": long, "body": ""}}, "text", false)
	out := buf.String()
	hrefLine := strings.SplitN(out, "\n", 4)[2]
	if hrefLine != "href        "+long {
		t.Errorf("href should wrap at 300 columns, got %q", hrefLine)
	}

	// body uses width 78 → first line 78 chars, then 66-char chunks.
	var buf2 bytes.Buffer
	PrintDataTo(&buf2, nil, []ddgs.Result{{"title": "T", "href": "", "body": long}}, "text", false)
	lines := strings.Split(strings.TrimSuffix(buf2.String(), "\n"), "\n")
	// "1.", title, href, then 200 = 78 + 66 + 56 characters of body.
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d: %q", len(lines), buf2.String())
	}
	if got := strings.TrimPrefix(lines[3], "body        "); len(got) != 78 {
		t.Errorf("first body line = %d chars, want 78", len(got))
	}
	if got := strings.TrimPrefix(lines[4], strings.Repeat(" ", 12)); len(got) != 66 {
		t.Errorf("second body line = %d chars, want 66", len(got))
	}
	if got := strings.TrimPrefix(lines[5], strings.Repeat(" ", 12)); len(got) != 56 {
		t.Errorf("third body line = %d chars, want 56", len(got))
	}
}

// TestPrintDataDetectedLanguageRename mirrors cli.py's
// `title = "language" if k == "detected_language" else k`.
func TestPrintDataDetectedLanguageRename(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	PrintDataTo(&buf, nil, []ddgs.Result{{"detected_language": "en", "title": "T"}}, "", false)
	if !strings.Contains(buf.String(), "language    en") {
		t.Errorf("detected_language should print as 'language', got %q", buf.String())
	}
	if strings.Contains(buf.String(), "detected_language") {
		t.Errorf("raw field name leaked into output: %q", buf.String())
	}
}

// TestPrintDataNoPauseWhenNotTTY asserts the non-TTY path never reads stdin.
func TestPrintDataNoPauseWhenNotTTY(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// A reader that panics if read from.
	PrintDataTo(&buf, panicReader{}, []ddgs.Result{{"title": "A", "href": "h", "body": "b"}}, "text", true)
	if buf.Len() == 0 {
		t.Fatal("no output written")
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("stdin read on non-TTY path") }

// TestPrintDataPythonValueFormatting covers f"{v}" formatting for non-string
// values: Python prints None/True/False and dict/list reprs.
func TestPrintDataPythonValueFormatting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, "body        None\n"},
		{"bool true", true, "body        True\n"},
		{"bool false", false, "body        False\n"},
		{"empty dict", map[string]any{}, "body        {}\n"},
		{"dict", map[string]any{"a": "b"}, "body        {'a': 'b'}\n"},
		{"nested dict", map[string]any{"a": map[string]any{"b": "c"}}, "body        {'a': {'b': 'c'}}\n"},
		{"list", []any{"a", "b"}, "body        ['a', 'b']\n"},
		{"int", 42, "body        42\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintDataTo(&buf, nil, []ddgs.Result{{"body": c.val}}, "text", false)
			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("got %q, want it to contain %q", buf.String(), c.want)
			}
		})
	}
}

// TestPadRight covers Python's f"{title:<12}" — pad, never truncate.
func TestPadRight(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"title", "title       "},
		{"detected_language", "detected_language"},
		{"abcdefghijkl", "abcdefghijkl"},
		{"", "            "},
	}
	for _, c := range cases {
		if got := padRight(c.in, 12); got != c.want {
			t.Errorf("padRight(%q, 12) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDownloadFilename mirrors cli.py's
// `unquote(url.split("/")[-1].split("?")[0])`, plus the traversal guard.
func TestDownloadFilename(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		// query strings are cut after the last "/" split (Python order)
		{"https://example.com/path/img.jpg?x=1", "img.jpg"},
		{"https://example.com/a?b=/c/d", "d"},
		// percent escapes are decoded with unquote semantics ("+" kept)
		{"https://example.com/a%20b.png", "a b.png"},
		{"https://example.com/a%2Fb.png", "a/b.png"},
		{"https://example.com/a+b.png", "a+b.png"},
		// malformed escapes stay as-is (unquote is lenient)
		{"https://example.com/x%ZZ.png", "x%ZZ.png"},
		{"img.jpg", "img.jpg"},
		// a trailing slash yields an empty name, matching Python
		{"https://example.com/dir/", ""},
		{"https://example.com/../x", "x"},
		// decoded traversal is neutralised (deliberate safety deviation)
		// ".." -> "_" (the "{i}_" prefix keeps the joined path inside the
		// target directory)
		{"https://example.com/%2e%2e%2fx", "_/x"},
		{"https://example.com/..%2f..%2fetc%2fpasswd", "_/_/etc/passwd"},
	}
	for _, c := range cases {
		if got := downloadFilename(c.in); got != c.want {
			t.Errorf("downloadFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDownloadFilenameTruncation mirrors cli.py's `filename[:200]`, which is
// applied to the final "{i}_{filename}" at write time, not to the raw name.
func TestDownloadFilenameTruncation(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("z", 250)
	if got := downloadFilename("https://example.com/" + long); len(got) != 250 {
		t.Errorf("downloadFilename length = %d, want 250 (truncation happens at write time)", len(got))
	}
	// Python: Path(dir) / f"{1}_{name}"[:200]
	if got := truncateFilename("1_" + long); len(got) != maxFilenameLen {
		t.Errorf("truncateFilename length = %d, want %d", len(got), maxFilenameLen)
	}
	short := truncateFilename("1_img.png")
	if short != "1_img.png" {
		t.Errorf("truncateFilename(short) = %q", short)
	}
}

// TestDownloadResultsTargetDir checks the directory naming and the "image"
// vs "href" URL-field choice, without any network traffic: results carry no
// URL of the selected kind, so no job is scheduled.
func TestDownloadResultsTargetDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	resolved, n, err := DownloadResults("q", []ddgs.Result{{"title": "t", "href": ""}}, "text", "text", "", dir, 2, true)
	if err != nil {
		t.Fatalf("DownloadResults: %v", err)
	}
	if resolved != dir || n != 0 {
		t.Errorf("got dir=%q n=%d, want dir=%q n=0", resolved, n, dir)
	}

	// No results at all: cli.py still mkdir()s, then returns.
	emptyDir := filepath.Join(dir, "empty")
	auto, n, err := DownloadResults("my_query", nil, "text", "text", "", emptyDir, 0, true)
	if err != nil {
		t.Fatalf("DownloadResults(empty): %v", err)
	}
	if auto != emptyDir || n != 0 {
		t.Errorf("empty results: got dir=%q n=%d, want dir=%q n=0", auto, n, emptyDir)
	}
	if fi, err := os.Stat(emptyDir); err != nil || !fi.IsDir() {
		t.Errorf("empty-results directory %q not created: %v", emptyDir, err)
	}
}

// TestAutoNames checks the auto-generated file stem and download directory
// (both f"{function_name}_{query}_{utc:%Y%m%d_%H%M%S}" in cli.py) without
// writing to the process working directory.
func TestAutoNames(t *testing.T) {
	t.Parallel()

	for _, got := range []string{
		autoBaseName("text", "my_query"),
		autoDownloadDir("text", "my_query"),
	} {
		if !strings.HasPrefix(got, "text_my_query_") {
			t.Fatalf("auto name = %q, want prefix text_my_query_", got)
		}
		stamp := strings.TrimPrefix(got, "text_my_query_")
		// %Y%m%d_%H%M%S → 8 digits, "_", 6 digits
		if len(stamp) != 15 || stamp[8] != '_' {
			t.Errorf("timestamp %q is not YYYYmmdd_HHMMSS", stamp)
		}
		if _, err := time.Parse("20060102_150405", stamp); err != nil {
			t.Errorf("timestamp %q does not parse: %v", stamp, err)
		}
	}
}
