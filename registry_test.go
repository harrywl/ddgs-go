package ddgs

// Registry parity test for SPEC §2 A1: the Go engineRegistry must expose
// exactly the backends that Python ddgs v9.16.0 auto-discovers from
// ddgs/engines/*.py — i.e. every BaseSearchEngine subclass with a `name` +
// `category` and `disabled` not True.
//
// Bing (text) and Yandex (text) are disabled upstream:
//   - bing:   engines/bing.py `disabled = True`
//   - yandex: engines/yandex.py `disabled = True` since commit f4ba66c
//     (2026-08-16, ddgs v9.15.0)
//
// User instruction: "那做一套测试，确认ddgs 和我们的是1:1 parity".

import "testing"

// pythonEngines is the authoritative registry as of deedy5/ddgs @ 70a5635
// (v9.16.0). Verified against the installed package:
//
//	python3 -c "from ddgs.engines import ENGINES; \
//	  print({k: sorted(v) for k, v in ENGINES.items()})"
var pythonEngines = map[string][]string{
	"text":   {"brave", "duckduckgo", "google", "grokipedia", "mojeek", "startpage", "wikipedia", "yahoo"},
	"images": {"bing", "duckduckgo"},
	"news":   {"bing", "duckduckgo", "yahoo"},
	"videos": {"duckduckgo"},
	"books":  {"annasarchive"},
}

func TestRegistryMatchesPython(t *testing.T) {
	// Same category set, no extra/missing categories.
	if len(engineRegistry) != len(pythonEngines) {
		t.Errorf("registry has %d categories, python %d", len(engineRegistry), len(pythonEngines))
	}
	for category, want := range pythonEngines {
		got, ok := engineRegistry[category]
		if !ok {
			t.Errorf("category %q missing from registry", category)
			continue
		}
		if len(got) != len(want) {
			names := make([]string, 0, len(got))
			for k := range got {
				names = append(names, k)
			}
			t.Errorf("category %q: %d backends %v, python %d %v",
				category, len(got), names, len(want), want)
		}
		for _, name := range want {
			if _, ok := got[name]; !ok {
				t.Errorf("category %q: backend %q missing from registry", category, name)
			}
		}
		for name := range got {
			found := false
			for _, w := range want {
				if w == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("category %q: backend %q registered but disabled/unknown upstream", category, name)
			}
		}
	}

	// Explicit regression for D1: yandex must NOT be registered (disabled
	// upstream since f4ba66c), while its factory stays available in the code.
	if _, ok := engineRegistry["text"]["yandex"]; ok {
		t.Error(`registry["text"]["yandex"] present; upstream disabled=True since f4ba66c`)
	}
	if _, err := newYandex("", defaultTimeout, Verify{}); err != nil {
		t.Errorf("newYandex factory code must be kept (mirrors the disabled Python class): %v", err)
	}
	// ...and selecting it as a backend resolves through the same
	// invalid-backend path as Python (_get_engines falls back to "auto" when
	// every requested backend is unknown), so yandex can never be queried.
	d, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	engines, err := d.getEngines("text", "yandex")
	if err != nil {
		t.Fatalf("getEngines(text, yandex): %v", err)
	}
	if len(engines) == 0 {
		t.Fatal("getEngines(text, yandex) fell back to auto but returned no engines")
	}
	for _, e := range engines {
		if e.Name() == "yandex" {
			t.Error("engine list still contains yandex after fallback to auto")
		}
	}
}
