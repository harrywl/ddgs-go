package ddgs

// Google WML (ddgs v9.16.0, SPEC §2 A2) parity tests: the rewritten
// ddgs/engines/google.py — WML endpoint, Nokia/UC User-Agent, new items and
// elements xpaths, sca_esv payload param, raw (non-lowercased) region split,
// pre_process_html prolog strip with its Python -1 quirk, and the /url?q=
// post_extract rewrite.
//
// The fixture testdata/google_wml.html is hand-authored to match the WML
// response shape (<?xml ...?> prolog + one <div> per result with
// <div><a><span>title</span></a></div> and <div><table>body</table></div>),
// including two noise items: a table-less ad div (must not match items_xpath)
// and a /search?... link (must be dropped by post_extract_results).
//
// User instruction: "那做一套测试，确认ddgs 和我们的是1:1 parity".

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// googleWMLUARe mirrors SPEC A2 item 8 exactly.
var googleWMLUARe = regexp.MustCompile(
	`^NokiaN72/(2\.0617\.1\.0\.3|2\.0625\.2\.0\.2|2\.0635\.2\.0\.2|5\.0706\.4\.0\.1|5\.0819\.4\.0\.1) Series60/2\.8 Profile/MIDP-2\.0 Configuration/CLDC-1\.1(/UC Browser7\.9\.1\.(120|121|122)/27/351/UCWEB)?$`)

func newGoogleForTest(t *testing.T) *baseEngine {
	t.Helper()
	eng, err := newGoogle("", 5*time.Second, Verify{})
	if err != nil {
		t.Fatalf("newGoogle: %v", err)
	}
	return eng.(*baseEngine)
}

func TestGoogleWMLUserAgent(t *testing.T) {
	// Instance UA (fixed once per engine instance, like Python's class
	// attribute) and 200 fresh samples from the generator both match.
	if ua := newGoogleForTest(t).http.headers.Get("User-Agent"); !googleWMLUARe.MatchString(ua) {
		t.Errorf("engine UA %q does not match the Nokia WML pattern", ua)
	}
	suffixes, plain := 0, 0
	for i := 0; i < 200; i++ {
		ua := googleWMLUA()
		if !googleWMLUARe.MatchString(ua) {
			t.Fatalf("googleWMLUA() %q does not match the pattern", ua)
		}
		if strings.Contains(ua, "/UC Browser") {
			suffixes++
		} else {
			plain++
		}
	}
	// p=0.5 coin flip: both branches must be hit (binomial tail beyond
	// 200 samples is astronomically small, so this is a robust check).
	if plain == 0 || suffixes == 0 {
		t.Errorf("UA suffix coin flip degenerate: plain=%d uc=%d", plain, suffixes)
	}
}

func TestGoogleWMLSpecFields(t *testing.T) {
	e := newGoogleForTest(t)
	if got, want := e.spec.searchURL, "https://www.google.com/wml/search"; got != want {
		t.Errorf("searchURL = %q, want %q", got, want)
	}
	if got, want := e.spec.searchMethod, "GET"; got != want {
		t.Errorf("searchMethod = %q, want %q", got, want)
	}
	if got, want := e.spec.provider, "google"; got != want {
		t.Errorf("provider = %q, want %q", got, want)
	}
	if got, want := e.spec.itemsXPath, "//div[./div[1]/a and ./div[2][table]]"; got != want {
		t.Errorf("itemsXPath = %q, want %q", got, want)
	}
	want := map[string]string{
		"title": "./div[a]/a/span[1]/text()",
		"href":  "./div[a]/a/@href",
		"body":  "./div[2][table]//text()",
	}
	for f, w := range want {
		if got := e.spec.elementsXPath[f]; got != w {
			t.Errorf("elementsXPath[%q] = %q, want %q", f, got, w)
		}
	}
}

func TestGoogleWMLPayload(t *testing.T) {
	e := newGoogleForTest(t)
	got, err := e.buildPayload(context.Background(), searchParams{
		Query: "golang test", Region: "us-en", SafeSearch: "moderate", Page: 1,
	})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	// Exact key set (sca_esv is part of it) — a missing/extra key is drift.
	wantKeys := []string{"q", "sca_esv", "filter", "start", "hl", "lr", "cr"}
	if len(got) != len(wantKeys) {
		t.Errorf("payload = %v, want exactly %v", got, wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("payload missing key %q (got %v)", k, got)
		}
	}
	for k, w := range map[string]string{
		"q": "golang test", "sca_esv": "1", "filter": "1", "start": "0",
		"hl": "en-US", "lr": "lang_en", "cr": "countryUS",
	} {
		if got[k] != w {
			t.Errorf("payload[%q] = %q, want %q", k, got[k], w)
		}
	}

	// timelimit present -> tbs appended; page 2 -> start 10.
	got, err = e.buildPayload(context.Background(), searchParams{
		Query: "q", Region: "de-de", SafeSearch: "on", TimeLimit: "w", Page: 2,
	})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	for k, w := range map[string]string{
		"sca_esv": "1", "filter": "2", "start": "10", "tbs": "qdr:w",
		"hl": "de-DE", "lr": "lang_de", "cr": "countryDE",
	} {
		if got[k] != w {
			t.Errorf("case 2: payload[%q] = %q, want %q", k, got[k], w)
		}
	}

	// safesearch lookup is lowercase-key based (Python safesearch_base[...lower()]).
	got, err = e.buildPayload(context.Background(), searchParams{
		Query: "q", Region: "us-en", SafeSearch: "OFF", Page: 1,
	})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if got["filter"] != "0" {
		t.Errorf(`SafeSearch "OFF": filter = %q, want "0"`, got["filter"])
	}

	// Raw region split: NO lowercasing (Python `country, lang = region.split("-")`).
	got, err = e.buildPayload(context.Background(), searchParams{
		Query: "q", Region: "US-EN", SafeSearch: "moderate", Page: 1,
	})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	for k, w := range map[string]string{"hl": "EN-US", "lr": "lang_EN", "cr": "countryUS"} {
		if got[k] != w {
			t.Errorf("region \"US-EN\": payload[%q] = %q, want %q (raw split, lang case preserved)",
				k, got[k], w)
		}
	}

	// A region that Python cannot unpack must raise, not silently truncate.
	if _, err := e.buildPayload(context.Background(), searchParams{
		Query: "q", Region: "us-en-insecure", SafeSearch: "moderate", Page: 1,
	}); err == nil {
		t.Error("region with 3 parts: expected error (Python ValueError equivalent)")
	}
}

func TestGooglePreProcessHTML(t *testing.T) {
	e := newGoogleForTest(t)
	if e.preProcessHTML == nil {
		t.Fatal("google engine must set preProcessHTML")
	}
	// Normal: slice from the FIRST "?>" (the WML prolog close) onwards.
	in := `<?xml version="1.0" encoding="UTF-8"?><html>a?>b</html>`
	if got := e.preProcessHTML(in); got != `?><html>a?>b</html>` {
		t.Errorf("prolog strip = %q", got)
	}
	// Python quirk: html_text[find("?>"):] with find() == -1 slices [-1:],
	// i.e. only the LAST CHARACTER. Reproduced deliberately.
	if got := e.preProcessHTML("<html>abcdef</html>"); got != ">" {
		t.Errorf(`no-prolog quirk: got %q, want ">" (Python html_text[-1:])`, got)
	}
	// Empty string: Python ""[-1:] == "".
	if got := e.preProcessHTML(""); got != "" {
		t.Errorf("empty input: got %q, want %q", got, "")
	}
}

func TestGoogleWMLFixtureExtract(t *testing.T) {
	raw, err := os.ReadFile("testdata/google_wml.html")
	if err != nil {
		t.Skipf("WML fixture missing: %v", err)
	}
	e := newGoogleForTest(t)

	text := e.preProcessHTML(string(raw))
	if !strings.HasPrefix(text, "?>") {
		t.Fatalf("preProcessHTML must start the document at the first \"?>\", got %q", text[:min(20, len(text))])
	}
	results, err := e.extractByXPath(text)
	if err != nil {
		t.Fatalf("extractByXPath: %v", err)
	}
	// 5 <div> in the fixture, 2 are noise (no table in div[2]; the
	// "sponsored" one) -> 4 items matched, one of which has a non-http href.
	if len(results) != 4 {
		t.Fatalf("items_xpath matched %d results, want 4\n%v", len(results), results)
	}
	final := e.postExtract(results)
	if len(final) != 3 {
		t.Fatalf("post_extract kept %d results, want 3 (the /search?q= link is dropped)", len(final))
	}
	want := []map[string]any{
		{
			"title": "Effective Go - The Go Programming Language",
			"href":  "https://go.dev/doc/effective_go",
			"body":  "Effective Go documents style: Write idiomatic code, not only copy a working snippet. go.dev/doc/effective_go",
		},
		{
			"title": "sync package - pkg.go.dev",
			"href":  "https://pkg.go.dev/sync#WaitGroup",
			"body":  "Type WaitGroup - func New - func With - type Pool - type Once.",
		},
		{
			"title": "Goroutines are functions launched to run concurrently",
			"href":  "https://example.com/redirected",
			"body":  "Goroutines are functions that are launched to run concurrently with other functions.",
		},
	}
	parityCompareResults(t, "google_wml", final, want)

	// The /url?q= rewrite specifically: up to first & (then first ?).
	rewrite := func(href string) string {
		rs := []Result{newResult("text")}
		rs[0].Set("title", "t")
		rs[0].Set("href", href)
		out := e.postExtract(rs)
		if len(out) == 0 {
			return ""
		}
		return out[0].getString("href")
	}
	for _, tc := range []struct{ in, out string }{
		{"/url?q=https://a.example/x&sa=U&ved=2", "https://a.example/x"},
		{"/url?q=https://a.example/x?y=1&sa=U", "https://a.example/x"},
		{"/url?q=https://a.example/x", "https://a.example/x"},
		{"https://direct.example/y", "https://direct.example/y"},
		{"/url?q=/relative/nope", ""}, // rewritten but not http -> dropped
	} {
		if got := rewrite(tc.in); got != tc.out {
			t.Errorf("rewrite(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}
