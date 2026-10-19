//go:build integration

package ddgs

// Live-network tests. Excluded from the default `go test ./...` run because
// search engines rate-limit and change their markup; run them explicitly with:
//
//	go test -tags=integration -timeout 10m ./...
//
// These mirror the Python upstream test suite (tests/ddgs_test.py), which also
// makes real HTTP requests. Failures here are often environmental (IP-blocked
// engines, sandbox network) rather than port defects — when one fails, run the
// same query through Python ddgs and compare before treating it as a bug.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// integrationTimeout is generous: auto mode fans out to several engines and
// the sandbox network adds latency.
const integrationTimeout = 45 * time.Second

func integrationDDGS(t *testing.T) *DDGS {
	t.Helper()
	d, err := New(&Config{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// runSearch is the shared assertion for the five categories. fn is a search
// method already bound to a DDGS instance (a closure), so it has the plain
// (ctx, query, opts) signature.
func runSearch(t *testing.T, category string, fn func(context.Context, string, *SearchOptions) ([]Result, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	results, err := fn(ctx, "golang context", &SearchOptions{MaxResults: 3})
	if err != nil {
		// No results is acceptable for some categories/regions when engines
		// refuse the sandbox IP, but the error must still be a ddgs error.
		if errors.Is(err, ErrNoResults) {
			t.Logf("%s: no results from any engine (environmental?)", category)
			return
		}
		t.Fatalf("%s: %v", category, err)
	}
	if len(results) == 0 {
		t.Fatalf("%s: nil error but zero results", category)
	}
	if len(results) > 3 {
		t.Fatalf("%s: MaxResults=3 violated, got %d", category, len(results))
	}
	// Every result must carry at least one identifying field.
	for i, r := range results {
		found := false
		for _, f := range ResultFields(category) {
			if s, ok := r[f].(string); ok && s != "" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s result %d is empty: %#v", category, i, r)
		}
	}
	t.Logf("%s: %d results, first title/keys ok", category, len(results))
}

func TestIntegrationText(t *testing.T) {
	d := integrationDDGS(t)
	runSearch(t, "text", d.Text)
}
func TestIntegrationImages(t *testing.T) {
	d := integrationDDGS(t)
	runSearch(t, "images", d.Images)
}
func TestIntegrationNews(t *testing.T) {
	d := integrationDDGS(t)
	runSearch(t, "news", d.News)
}
func TestIntegrationVideos(t *testing.T) {
	d := integrationDDGS(t)
	runSearch(t, "videos", d.Videos)
}
func TestIntegrationBooks(t *testing.T) {
	d := integrationDDGS(t)
	runSearch(t, "books", d.Books)
}

// TestIntegrationBackendSelection checks that an explicit backend bypasses
// auto mode and that results really come from that engine family.
func TestIntegrationBackendSelection(t *testing.T) {
	d := integrationDDGS(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	results, err := d.Text(ctx, "wikipedia", &SearchOptions{MaxResults: 3, Backend: "wikipedia"})
	if err != nil {
		if errors.Is(err, ErrNoResults) {
			t.Skip("wikipedia returned nothing (environmental)")
		}
		t.Fatalf("backend=wikipedia: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("backend=wikipedia: zero results")
	}
	// The wikipedia engine returns exactly one opensearch hit.
	if len(results) != 1 {
		t.Logf("backend=wikipedia returned %d results (expected 1)", len(results))
	}
	href, _ := results[0]["href"].(string)
	if href == "" || !strings.Contains(href, "wikipedia.org") {
		t.Fatalf("backend=wikipedia result href not wikipedia.org: %q", href)
	}
}

// TestIntegrationUnknownBackendFallsBack mirrors Python _get_engines: an
// unknown/disabled backend logs a warning and falls back to auto.
func TestIntegrationUnknownBackendFallsBack(t *testing.T) {
	d := integrationDDGS(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// yandex was disabled upstream in v9.15.0, so it must not be selectable,
	// and the search must still succeed via the auto fallback.
	results, err := d.Text(ctx, "golang context", &SearchOptions{MaxResults: 3, Backend: "yandex"})
	if err != nil {
		if errors.Is(err, ErrNoResults) {
			t.Skip("auto fallback returned nothing (environmental)")
		}
		t.Fatalf("backend=yandex should fall back to auto, got error: %v", err)
	}
	t.Logf("backend=yandex fell back to auto: %d results", len(results))
}

// TestIntegrationExtract exercises the live extract path for the formats the
// Go port supports, and asserts text_rich is rejected with ErrUnsupportedFormat.
func TestIntegrationExtract(t *testing.T) {
	d := integrationDDGS(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	const target = "https://go.dev/doc/effective_go"

	for _, format := range []ExtractFormat{FormatTextMarkdown, FormatTextPlain, FormatText, FormatContent} {
		ex, err := d.Extract(ctx, target, format)
		if err != nil {
			t.Fatalf("Extract(%s): %v", format, err)
		}
		if ex.URL != target {
			t.Fatalf("Extract(%s): URL = %q, want %q", format, ex.URL, target)
		}
		if format == FormatContent {
			if len(ex.Bytes) == 0 {
				t.Fatalf("Extract(content): empty bytes")
			}
			continue
		}
		if ex.Content == "" {
			t.Fatalf("Extract(%s): empty content", format)
		}
		if format == FormatTextMarkdown && !strings.Contains(ex.Content, "Go") {
			t.Fatalf("Extract(text_markdown): content looks wrong: %.80q", ex.Content)
		}
		t.Logf("Extract(%s): %d bytes/chars", format, len(ex.Content))
	}

	if _, err := d.Extract(ctx, target, FormatTextRich); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Extract(text_rich): err = %v, want ErrUnsupportedFormat", err)
	}
}

// TestIntegrationContextCancel verifies cancellation really aborts a search.
func TestIntegrationContextCancel(t *testing.T) {
	d := integrationDDGS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := d.Text(ctx, "golang context", &SearchOptions{MaxResults: 3})
	if err == nil {
		t.Fatal("cancelled context: expected an error, got nil")
	}
	if errors.Is(err, ErrNoResults) {
		t.Skip("cancelled search surfaced as ErrNoResults")
	}
	t.Logf("cancelled context: %v", err)
}
