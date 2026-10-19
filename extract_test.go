package ddgs

// Tests for the extract formats, incl. the SPEC §2 A4 / §9.7 divergence:
// fmt="text_rich" (Python: primp's Response.text_rich) has no pure-Go
// equivalent and must return ErrUnsupportedFormat instead of silently
// falling back to markdown like unknown format values do.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const extractTestPage = `<html><head><title>T</title></head><body>
<h1>Title</h1><p>Body <a href="https://x.com">link</a>.</p></body></html>`

func TestExtractTextRichUnsupported(t *testing.T) {
	if FormatTextRich != "text_rich" {
		t.Fatalf("FormatTextRich = %q, want \"text_rich\"", FormatTextRich)
	}
	// ErrUnsupportedFormat must wrap ErrDDGS (library convention, SPEC §7.3).
	if !errors.Is(ErrUnsupportedFormat, ErrDDGS) {
		t.Errorf("ErrUnsupportedFormat does not wrap ErrDDGS: %v", ErrUnsupportedFormat)
	}

	d, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A URL on a closed port: if the code ever tried to fetch before checking
	// the format, the error would be a network/timeout error, not
	// ErrUnsupportedFormat. Rejected up front (documented divergence).
	got, err := d.Extract(context.Background(), "http://127.0.0.1:1/", FormatTextRich)
	if got != nil {
		t.Errorf("expected nil Extracted, got %+v", got)
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
	if !errors.Is(err, ErrDDGS) {
		t.Errorf("error %v does not chain to ErrDDGS", err)
	}
	for _, want := range []string{"text_rich", "primp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q should mention %q", err, want)
		}
	}
}

func TestExtractFormatsServed(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		fmt.Fprint(w, extractTestPage)
	}))
	defer server.Close()

	d, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Supported formats still behave.
	res, err := d.Extract(ctx, server.URL, FormatText)
	if err != nil || res.Content != extractTestPage {
		t.Fatalf("FormatText: err=%v content=%q", err, truncate(res))
	}
	res, err = d.Extract(ctx, server.URL, FormatTextMarkdown)
	if err != nil || !strings.Contains(res.Content, "[link](https://x.com)") {
		t.Errorf("FormatTextMarkdown: err=%v content=%q", err, res.Content)
	}
	res, err = d.Extract(ctx, server.URL, FormatTextPlain)
	if err != nil || strings.Contains(res.Content, "[link](") {
		t.Errorf("FormatTextPlain: err=%v content=%q", err, res.Content)
	}
	raw, err := d.Extract(ctx, server.URL, FormatContent)
	if err != nil || string(raw.Bytes) != extractTestPage {
		t.Errorf("FormatContent: err=%v", err)
	}
	// Unknown formats fall back to markdown, like Python's else-branch.
	res, err = d.Extract(ctx, server.URL, ExtractFormat("bogus"))
	if err != nil || !strings.Contains(res.Content, "[link](https://x.com)") {
		t.Errorf("unknown format fallback: err=%v content=%q", err, res.Content)
	}

	// text_rich is rejected before the request: the server must not have
	// been hit for that format.
	hitsBefore := hits
	if _, err := d.Extract(ctx, server.URL, FormatTextRich); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("FormatTextRich: err = %v, want ErrUnsupportedFormat", err)
	}
	if hits != hitsBefore {
		t.Errorf("text_rich must not issue an HTTP request (%d extra hits)", hits-hitsBefore)
	}
}

func truncate(e *Extracted) string {
	if e == nil {
		return "<nil>"
	}
	return e.Content
}
