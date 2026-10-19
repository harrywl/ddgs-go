package ddgs

// Text engines: google, brave, mojeek, yahoo, yandex, startpage.
// Each mirrors the corresponding module in ddgs/engines/.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/antchfx/htmlquery"
)

func randInt(min, max int64) int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return min
	}
	return min + n.Int64()
}

// googleFirmwares and googleUCVersions mirror the tuples in
// ddgs.engines.google.get_ua. The firmware/UC-version pools are exact —
// Google's WML endpoint only serves results to this short list of legacy
// Nokia/Symbian + UC Browser identifiers.
var (
	googleFirmwares = []string{
		"2.0617.1.0.3",
		"2.0625.2.0.2",
		"2.0635.2.0.2",
		"5.0706.4.0.1",
		"5.0819.4.0.1",
	}
	googleUCVersions = []string{"7.9.1.120", "7.9.1.121", "7.9.1.122"}
)

// googleWMLUA returns one Nokia/UC WML User-Agent string, mirroring
// ddgs.engines.google.get_ua:
//
//	NokiaN72/{firmware} Series60/2.8 Profile/MIDP-2.0 Configuration/CLDC-1.1
//	[.../UC Browser{uc_version}/27/351/UCWEB]   (appended with p=0.5)
//
// Python fixes the UA once at class-import time (headers_update is evaluated
// at class definition). In Go the UA is generated once per engine instance in
// newGoogle — equivalent, because DDGS caches engine instances (see
// DDGS.enginesCache), so a given instance keeps one UA for its lifetime, just
// like the Python class attribute.
func googleWMLUA() string {
	firmware := googleFirmwares[randInt(0, int64(len(googleFirmwares)-1))]
	ua := "NokiaN72/" + firmware + " Series60/2.8 Profile/MIDP-2.0 Configuration/CLDC-1.1"
	if randInt(0, 1) == 1 { // random.choice((True, False))
		ua += "/UC Browser" + googleUCVersions[randInt(0, int64(len(googleUCVersions)-1))] + "/27/351/UCWEB"
	}
	return ua
}

// newGoogle mirrors ddgs.engines.google.Google (rewritten upstream in commit
// 3ea6cbc, 2026-08-26 for ddgs v9.16.0): the legacy WML (Wireless Markup
// Language, i.e. feature-phone) endpoint, which serves a static XHTML
// document — no JS, no TLS fingerprinting gate.
func newGoogle(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:          "google",
		category:      "text",
		provider:      "google",
		searchURL:     "https://www.google.com/wml/search",
		searchMethod:  "GET",
		headersUpdate: map[string]string{"User-Agent": googleWMLUA()},
		itemsXPath:    "//div[./div[1]/a and ./div[2][table]]",
		elementsXPath: map[string]string{
			"title": "./div[a]/a/span[1]/text()",
			"href":  "./div[a]/a/@href",
			"body":  "./div[2][table]//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.buildPayload = func(_ context.Context, p searchParams) (map[string]string, error) {
		e.http.setCookies("https://google.com", map[string]string{"CONSENT": "YES+"})
		safesearch := map[string]string{"on": "2", "moderate": "1", "off": "0"}
		start := (p.Page - 1) * 10
		payload := map[string]string{
			"q":       p.Query,
			"sca_esv": "1",
			"filter":  safesearch[strings.ToLower(p.SafeSearch)],
			"start":   strconv.Itoa(start),
		}
		// Python: `country, lang = region.split("-")` — a RAW split with no
		// lowercasing (unlike splitRegion, which other engines use and which
		// does ToLower). hl/lr preserve the lang casing of the region string
		// as-is; only the country gets .upper(). Mirrored byte-exactly here,
		// so region "US-EN" yields hl "EN-US", lr "lang_EN", cr "countryUS".
		country, lang, err := splitRawRegion(p.Region)
		if err != nil {
			return nil, err
		}
		payload["hl"] = lang + "-" + strings.ToUpper(country) // interface language
		payload["lr"] = "lang_" + lang                        // results in this language
		payload["cr"] = "country" + strings.ToUpper(country)  // results from this country
		if p.TimeLimit != "" {
			payload["tbs"] = "qdr:" + p.TimeLimit
		}
		return payload, nil
	}
	// preProcessHTML mirrors Google.pre_process_html exactly:
	// `return html_text[html_text.find("?>"):]`. The WML response starts with
	// an `<?xml ... ?>` declaration; slicing from the first "?>" drops
	// everything up to and including the prolog's closing marker.
	//
	// Python quirk (preserved deliberately): str.find() returns -1 when "?>
	// is absent, and text[-1:] is the LAST CHARACTER ONLY — not the whole
	// string. So a response with no "?>" collapses to a single trailing
	// character and yields zero results. Go's strings.Index returns -1 too,
	// so the same slice semantics are reproduced explicitly here.
	e.preProcessHTML = func(text string) string {
		if i := strings.Index(text, "?>"); i >= 0 {
			return text[i:]
		}
		// Python: html_text[-1:]
		if len(text) > 0 {
			return text[len(text)-1:]
		}
		return text
	}
	e.postExtract = func(results []Result) []Result {
		out := make([]Result, 0, len(results))
		for _, r := range results {
			href := r.getString("href")
			if strings.HasPrefix(href, "/url?q=") {
				// Python: result.href.split("?q=")[1].split("&")[0].split("?")[0]
				href = strings.SplitN(strings.SplitN(strings.SplitN(href, "?q=", 2)[1], "&", 2)[0], "?", 2)[0]
				r.Set("href", href)
			}
			if r.getString("title") != "" && strings.HasPrefix(r.getString("href"), "http") {
				out = append(out, r)
			}
		}
		return out
	}
	return e, nil
}

// splitRawRegion mirrors Python `country, lang = region.split("-")` — no
// case folding and no tolerance for extra hyphen-separated parts (Python
// raises ValueError while unpacking). splitRegion stays for the engines that
// already pass against the older golden data.
func splitRawRegion(region string) (country, lang string, err error) {
	parts := strings.Split(region, "-")
	if len(parts) != 2 {
		return "", "", fmt.Errorf(
			"%w: invalid region %q: expected \"{country}-{lang}\" (e.g. \"us-en\")",
			ErrDDGS, region)
	}
	return parts[0], parts[1], nil
}

// newBrave mirrors ddgs.engines.brave.Brave.
func newBrave(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:         "brave",
		category:     "text",
		provider:     "brave",
		searchURL:    "https://search.brave.com/search",
		searchMethod: "GET",
		itemsXPath:   "//div[@data-type='web']",
		elementsXPath: map[string]string{
			"title": ".//div[(contains(@class,'title') or contains(@class,'sitename-container')) and position()=last()]//text()",
			"href":  ".//a[div[contains(@class, 'title')]]/@href",
			"body":  ".//div[contains(@class, 'snippet')]//div[contains(@class, 'content')]//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.buildPayload = func(_ context.Context, p searchParams) (map[string]string, error) {
		payload := map[string]string{"q": p.Query, "source": "web"}
		country, _ := splitRegion(p.Region)
		cookies := map[string]string{country: country, "useLocation": "0"}
		if p.SafeSearch != "moderate" {
			if p.SafeSearch == "on" {
				cookies["safesearch"] = "strict"
			} else {
				cookies["safesearch"] = "off"
			}
		}
		e.http.setCookies("https://search.brave.com", cookies)
		if p.TimeLimit != "" {
			payload["tf"] = map[string]string{"d": "pd", "w": "pw", "m": "pm", "y": "py"}[p.TimeLimit]
		}
		if p.Page > 1 {
			payload["offset"] = strconv.Itoa(p.Page - 1)
		}
		return payload, nil
	}
	return e, nil
}

// newMojeek mirrors ddgs.engines.mojeek.Mojeek.
func newMojeek(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:         "mojeek",
		category:     "text",
		provider:     "mojeek",
		searchURL:    "https://www.mojeek.com/search",
		searchMethod: "GET",
		itemsXPath:   "//ul[contains(@class, 'results')]/li",
		elementsXPath: map[string]string{
			"title": ".//h2//text()",
			"href":  ".//h2/a/@href",
			"body":  ".//p[@class='s']//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.buildPayload = func(_ context.Context, p searchParams) (map[string]string, error) {
		country, lang := splitRegion(p.Region)
		e.http.setCookies("https://www.mojeek.com", map[string]string{"arc": country, "lb": lang})
		payload := map[string]string{"q": p.Query}
		if p.SafeSearch == "on" {
			payload["safe"] = "1"
		}
		if p.Page > 1 {
			payload["s"] = strconv.Itoa((p.Page-1)*10 + 1)
		}
		return payload, nil
	}
	return e, nil
}

// yahooExtractURL sanitizes a Yahoo redirect URL, mirroring
// ddgs.engines.yahoo.extract_url.
func yahooExtractURL(u string) string {
	parts := strings.SplitN(u, "/RU=", 2)
	if len(parts) < 2 {
		return u
	}
	t := parts[1]
	t = strings.SplitN(t, "/RK=", 2)[0]
	t = strings.SplitN(t, "/RS=", 2)[0]
	decoded, err := url.QueryUnescape(strings.ReplaceAll(t, "+", " "))
	if err != nil {
		return t
	}
	return decoded
}

// tokenURLSafe generates a URL-safe random token of n bytes, mirroring
// Python secrets.token_urlsafe.
func tokenURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("A", n)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// newYahoo mirrors ddgs.engines.yahoo.Yahoo.
func newYahoo(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:         "yahoo",
		category:     "text",
		provider:     "bing",
		searchURL:    "https://search.yahoo.com/search",
		searchMethod: "GET",
		itemsXPath:   "//div[contains(@class, 'relsrch')]",
		elementsXPath: map[string]string{
			"title": ".//div[contains(@class, 'Title')]//h3//text()",
			"href":  ".//div[contains(@class, 'Title')]//a/@href",
			"body":  ".//div[contains(@class, 'Text')]//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.searchURLFn = func(_ searchParams) string {
		return fmt.Sprintf("https://search.yahoo.com/search;_ylt=%s;_ylu=%s", tokenURLSafe(18), tokenURLSafe(35))
	}
	e.buildPayload = func(_ context.Context, p searchParams) (map[string]string, error) {
		payload := map[string]string{"p": p.Query}
		if p.Page > 1 {
			payload["b"] = strconv.Itoa((p.Page-1)*7 + 1)
		}
		if p.TimeLimit != "" {
			payload["btf"] = p.TimeLimit
		}
		return payload, nil
	}
	e.postExtract = func(results []Result) []Result {
		out := make([]Result, 0, len(results))
		for _, r := range results {
			href := r.getString("href")
			if strings.HasPrefix(href, "https://www.bing.com/aclick?") {
				continue
			}
			if strings.Contains(href, "/RU=") {
				r.Set("href", yahooExtractURL(href))
			}
			out = append(out, r)
		}
		return out
	}
	return e, nil
}

// newYandex mirrors ddgs.engines.yandex.Yandex. The upstream class sets
// `disabled = True` (commit f4ba66c, 2026-08-16), so this factory is kept for
// reference — exactly like the disabled Python class — but is NOT registered
// in engineRegistry and can never be selected as a backend.
func newYandex(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:         "yandex",
		category:     "text",
		provider:     "yandex",
		searchURL:    "https://yandex.com/search/site/",
		searchMethod: "GET",
		itemsXPath:   "//li[contains(@class, 'serp-item')]",
		elementsXPath: map[string]string{
			"title": ".//h3//text()",
			"href":  ".//h3//a/@href",
			"body":  ".//div[contains(@class, 'text')]//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.buildPayload = func(_ context.Context, p searchParams) (map[string]string, error) {
		payload := map[string]string{
			"text":     p.Query,
			"web":      "1",
			"searchid": strconv.FormatInt(randInt(1000000, 9999999), 10),
		}
		if p.Page > 1 {
			payload["p"] = strconv.Itoa(p.Page - 1)
		}
		return payload, nil
	}
	return e, nil
}

// newStartpage mirrors ddgs.engines.startpage.Startpage.
func newStartpage(proxy string, timeout time.Duration, verify Verify) (searchEngine, error) {
	e, err := newBaseEngine(engineSpec{
		name:          "startpage",
		category:      "text",
		provider:      "google",
		searchURL:     "https://www.startpage.com/sp/search",
		searchMethod:  "POST",
		headersUpdate: map[string]string{"Referer": "https://www.startpage.com/"},
		itemsXPath:    "//div[contains(@class, 'result')][./a]",
		elementsXPath: map[string]string{
			"title": ".//h2//text()",
			"href":  "./a/@href",
			"body":  ".//p//text()",
		},
	}, proxy, timeout, verify)
	if err != nil {
		return nil, err
	}
	e.buildPayload = func(ctx context.Context, p searchParams) (map[string]string, error) {
		// Fetch the "sc" anti-bot token from the home page.
		resp, err := e.http.get(ctx, "https://www.startpage.com/", nil)
		if err != nil {
			return nil, err
		}
		sc := ""
		if doc, err := htmlquery.Parse(strings.NewReader(resp.Text())); err == nil {
			if node, err := htmlquery.Query(doc, `//form[@id="search"]//input[@name="sc"]/@value`); err == nil && node != nil {
				sc = nodeText(node)
			}
		}
		country, lang := splitRegion(p.Region)
		safesearch := map[string]string{"on": "heavy", "moderate": "moderate", "off": "none"}
		payload := map[string]string{
			"query":    p.Query,
			"cat":      "web",
			"t":        "device",
			"sc":       sc,
			"lui":      "english",
			"language": "english",
			"abp":      "1",
			"abd":      "0",
			"abe":      "0",
			"qsr":      fmt.Sprintf("%s_%s", lang, strings.ToUpper(country)),
			"qadf":     safesearch[strings.ToLower(p.SafeSearch)],
			"segment":  "organic",
		}
		if p.Page > 1 {
			payload["page"] = strconv.Itoa(p.Page)
		}
		if p.TimeLimit != "" {
			payload["with_date"] = p.TimeLimit
		}
		return payload, nil
	}
	return e, nil
}
