package ddgs

// Engine registry, mirroring ddgs/engines/__init__.py's ENGINES dict.
// Used by ddgs.go (DDGS search methods) to resolve category+backend to
// engine factories. The "bing" text engine is disabled upstream
// (disabled=True) and is therefore not registered here either.
//
// The "yandex" text engine is likewise disabled upstream since Python commit
// f4ba66c (2026-08-16, ddgs v9.15.0): engines/yandex.py sets
// `disabled = True`, so the auto-discovery in engines/__init__.py no longer
// registers it. The newYandex factory code is kept in engine_text.go (mirroring
// Python, which keeps the disabled class) but is NOT registered here.
// See TestRegistryMatchesPython for the authoritative key set.
// User instruction: "帮我写一个golang的library，主要就是把
// https://github.com/deedy5/ddgs 的功能复刻出来，然后提供和python版本一致的接口".

import "time"

// engineFactory constructs an engine instance.
type engineFactory func(proxy string, timeout time.Duration, verify Verify) (searchEngine, error)

// engineRegistry maps category -> backend name -> factory.
// Mirrors ENGINES[category][name] = class in the Python implementation
// (ddgs/engines/__init__.py, auto-discovery skips classes with disabled=True).
// yandex is disabled upstream since commit f4ba66c (2026-08-16) and is
// therefore not registered here; its factory code remains in engine_text.go.
var engineRegistry = map[string]map[string]engineFactory{
	"text": {
		"brave":      newBrave,
		"duckduckgo": newDuckduckgoText,
		"google":     newGoogle,
		"grokipedia": newGrokipedia,
		"mojeek":     newMojeek,
		"startpage":  newStartpage,
		"wikipedia":  newWikipedia,
		"yahoo":      newYahoo,
	},
	"images": {
		"bing":       newBingImages,
		"duckduckgo": newDuckduckgoImages,
	},
	"news": {
		"bing":       newBingNews,
		"duckduckgo": newDuckduckgoNews,
		"yahoo":      newYahooNews,
	},
	"videos": {
		"duckduckgo": newDuckduckgoVideos,
	},
	"books": {
		"annasarchive": newAnnasArchive,
	},
}
