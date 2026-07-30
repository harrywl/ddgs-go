package ddgs

// DDGS orchestrator, mirroring ddgs/ddgs.py. This is the public API of the
// library: New(...) + Text/Images/News/Videos/Books, matching the Python
// DDGS class (proxy/timeout/verify constructor args; region, safesearch,
// timelimit, max_results, page, backend search args).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Verify controls TLS verification, mirroring the Python
// `verify: bool | str` parameter (True / False / path-to-PEM).
type Verify struct {
	// SkipVerify disables TLS certificate verification (Python verify=False).
	SkipVerify bool
	// CACertFile is a path to a PEM file with custom CA certs (Python verify="path").
	CACertFile string
}

// Config configures a DDGS instance, mirroring DDGS.__init__ kwargs.
type Config struct {
	// Proxy for the HTTP clients; supports http/https/socks5 URLs.
	// The alias "tb" expands to the Tor Browser proxy. Falls back to the
	// DDGS_PROXY environment variable when empty.
	Proxy string
	// Timeout per engine request. Defaults to 5s (Python default timeout=5).
	Timeout time.Duration
	// Verify controls TLS verification.
	Verify Verify
	// Threads caps the number of concurrent engine requests per search.
	// Zero means automatic (based on max_results), mirroring DDGS.threads.
	Threads int
}

// SearchOptions are the per-search keyword arguments, mirroring
// DDGS._search_sync kwargs. The zero value gives Python's defaults.
type SearchOptions struct {
	// Region, e.g. "us-en", "uk-en", "ru-ru". Default "us-en".
	Region string
	// SafeSearch: "on", "moderate", "off". Default "moderate".
	SafeSearch string
	// TimeLimit: "d", "w", "m", "y". Default none.
	TimeLimit string
	// MaxResults is the maximum number of results to return. Default 10.
	// Set to -1 to return everything found (Python max_results=None).
	MaxResults int
	// Page of results, 1-based. Default 1.
	Page int
	// Backend is a single or comma-delimited list of backends, or
	// "auto"/"all". Default "auto".
	Backend string
	// Extra engine-specific parameters, mirroring Python **kwargs
	// (e.g. images: size, color, type_image, layout, license_image;
	// videos: resolution, duration, license_videos).
	Extra map[string]string
}

// DefaultMaxResults mirrors the Python default max_results=10.
const DefaultMaxResults = 10

// resultsPerWorkerEstimate mirrors ceil(max_results / 10) + 1 in Python.
const resultsPerWorkerEstimate = 10

// minWorkersWithGeneral guarantees a general SERP engine is in flight from
// the start: encyclopedia engines (wikipedia, grokipedia) return at most one
// result each, so without this floor small max_results values would spend the
// whole first round on encyclopedias and always need a second round.
const minWorkersWithGeneral = 3

// failureCooldown is how long an engine is skipped in auto mode after
// consecutive failures (e.g. IP-blocked engines). In-memory, per DDGS
// instance; direct backend selection bypasses it.
const failureCooldown = 5 * time.Minute

// failuresBeforeCooldown is how many consecutive failures trigger a cooldown.
const failuresBeforeCooldown = 2

// DDGS | Dux Distributed Global Search.
// A metasearch client that aggregates results from diverse web search services.
//
//	d, err := ddgs.New(nil)
//	results, err := d.Text(ctx, "python", nil)
//
// DDGS is safe for concurrent use: the engine cache is mutex-protected and
// engines only mutate goroutine-safe state (net/http cookie jars) after
// construction.
type DDGS struct {
	proxy   string
	timeout time.Duration
	verify  Verify
	threads int

	mu           sync.Mutex
	enginesCache map[string]searchEngine // key: category+"/"+name
	engineHealth map[string]*engineHealth // key: category+"/"+name
}

// engineHealth tracks consecutive failures per engine for the in-memory
// cooldown. Guarded by DDGS.mu.
type engineHealth struct {
	consecutiveFailures int
	cooldownUntil       time.Time
}

// engineOnCooldown reports whether an engine should be skipped in auto mode.
func (d *DDGS) engineOnCooldown(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	h, ok := d.engineHealth[key]
	return ok && now.Before(h.cooldownUntil)
}

// recordEngineResult updates failure counters; success clears the state,
// repeated failures start a cooldown window.
func (d *DDGS) recordEngineResult(key string, ok bool, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ok {
		delete(d.engineHealth, key)
		return
	}
	h := d.engineHealth[key]
	if h == nil {
		h = &engineHealth{}
		d.engineHealth[key] = h
	}
	h.consecutiveFailures++
	if h.consecutiveFailures >= failuresBeforeCooldown {
		h.cooldownUntil = now.Add(failureCooldown)
	}
}

// New creates a DDGS instance. A nil config uses defaults
// (no proxy — or DDGS_PROXY env var, 5s timeout, TLS verification on).
func New(cfg *Config) (*DDGS, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	proxy := expandProxyTBAlias(cfg.Proxy)
	if proxy == "" {
		proxy = os.Getenv("DDGS_PROXY")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &DDGS{
		proxy:        proxy,
		timeout:      timeout,
		verify:       cfg.Verify,
		threads:      cfg.Threads,
		enginesCache: map[string]searchEngine{},
		engineHealth: map[string]*engineHealth{},
	}, nil
}

// Text performs a text search. Mirrors DDGS.text().
func (d *DDGS) Text(ctx context.Context, query string, opts *SearchOptions) ([]Result, error) {
	return d.search(ctx, "text", query, opts)
}

// Images performs an image search. Mirrors DDGS.images().
func (d *DDGS) Images(ctx context.Context, query string, opts *SearchOptions) ([]Result, error) {
	return d.search(ctx, "images", query, opts)
}

// News performs a news search. Mirrors DDGS.news().
func (d *DDGS) News(ctx context.Context, query string, opts *SearchOptions) ([]Result, error) {
	return d.search(ctx, "news", query, opts)
}

// Videos performs a video search. Mirrors DDGS.videos().
func (d *DDGS) Videos(ctx context.Context, query string, opts *SearchOptions) ([]Result, error) {
	return d.search(ctx, "videos", query, opts)
}

// Books performs a book search. Mirrors DDGS.books().
func (d *DDGS) Books(ctx context.Context, query string, opts *SearchOptions) ([]Result, error) {
	return d.search(ctx, "books", query, opts)
}

// getEngines retrieves engine instances for a category and backend list,
// mirroring DDGS._get_engines: shuffle, prepend wikipedia/grokipedia for
// text "auto", cache instances, sort by priority descending.
func (d *DDGS) getEngines(category, backend string) ([]searchEngine, error) {
	registry, ok := engineRegistry[category]
	if !ok {
		return nil, fmt.Errorf("%w: unknown category %q", ErrDDGS, category)
	}

	backendList := []string{}
	for _, b := range strings.Split(backend, ",") {
		if b = strings.TrimSpace(b); b != "" {
			backendList = append(backendList, b)
		}
	}

	engineKeys := make([]string, 0, len(registry))
	for k := range registry {
		engineKeys = append(engineKeys, k)
	}
	sort.Strings(engineKeys) // deterministic base order before shuffle
	rand.Shuffle(len(engineKeys), func(i, j int) {
		engineKeys[i], engineKeys[j] = engineKeys[j], engineKeys[i]
	})

	isAuto := len(backendList) == 0
	for _, b := range backendList {
		if b == "auto" || b == "all" {
			isAuto = true
			break
		}
	}

	var keys []string
	if isAuto {
		keys = engineKeys
		if category == "text" {
			head := []string{"wikipedia", "grokipedia"}
			rest := make([]string, 0, len(keys))
			for _, k := range keys {
				if k != "wikipedia" && k != "grokipedia" {
					rest = append(rest, k)
				}
			}
			keys = append(head, rest...)
		}
	} else {
		keys = backendList
	}

	var invalid []string
	instances := make([]searchEngine, 0, len(keys))
	d.mu.Lock()
	for _, key := range keys {
		factory, ok := registry[key]
		if !ok {
			invalid = append(invalid, key)
			continue
		}
		cacheKey := category + "/" + key
		if inst, ok := d.enginesCache[cacheKey]; ok {
			instances = append(instances, inst)
			continue
		}
		inst, err := factory(d.proxy, d.timeout, d.verify)
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.enginesCache[cacheKey] = inst
		instances = append(instances, inst)
	}
	d.mu.Unlock()

	if len(invalid) > 0 && len(instances) == 0 {
		// Same fallback as Python: fall back to "auto".
		return d.getEngines(category, "auto")
	}

	// Sort by priority descending (stable keeps the shuffled order for ties).
	sort.SliceStable(instances, func(i, j int) bool {
		return instances[i].Priority() > instances[j].Priority()
	})
	return instances, nil
}

// engineResult carries one engine's outcome across the fan-out channel.
type engineResult struct {
	key      string // category+"/"+name, for health tracking
	provider string
	results  []Result
	err      error
}

// search performs a search across engines in a category, mirroring
// DDGS._search_sync: fan out to one engine per unique provider, dedup and
// count results, stop when max_results is reached, then rank.
func (d *DDGS) search(ctx context.Context, category, query string, opts *SearchOptions) ([]Result, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query is mandatory", ErrDDGS)
	}
	if opts == nil {
		opts = &SearchOptions{}
	}
	region := opts.Region
	if region == "" {
		region = "us-en"
	}
	safesearch := opts.SafeSearch
	if safesearch == "" {
		safesearch = "moderate"
	}
	page := opts.Page
	if page < 1 {
		page = 1
	}
	backend := opts.Backend
	if backend == "" {
		backend = "auto"
	}
	maxResults := opts.MaxResults
	if maxResults == 0 {
		maxResults = DefaultMaxResults
	}
	unlimited := maxResults < 0

	engines, err := d.getEngines(category, backend)
	if err != nil {
		return nil, err
	}

	isAuto := backend == "auto" || backend == "all" || strings.Contains(backend, ",")
	now := time.Now()
	if isAuto {
		// Skip engines on failure cooldown (in-memory, per instance),
		// unless that would leave nothing to query.
		healthy := make([]searchEngine, 0, len(engines))
		for _, e := range engines {
			if !d.engineOnCooldown(category+"/"+e.Name(), now) {
				healthy = append(healthy, e)
			}
		}
		if len(healthy) > 0 {
			engines = healthy
		}
	}

	uniqueProviders := map[string]bool{}
	for _, e := range engines {
		uniqueProviders[e.Provider()] = true
	}
	maxWorkers := len(uniqueProviders)
	if !unlimited {
		estimate := int(math.Ceil(float64(maxResults)/resultsPerWorkerEstimate)) + 1
		// Floor keeps a general SERP engine in flight alongside the
		// single-result encyclopedia engines (wikipedia/grokipedia).
		if estimate < minWorkersWithGeneral {
			estimate = minWorkersWithGeneral
		}
		if estimate < maxWorkers {
			maxWorkers = estimate
		}
	}
	if d.threads > 0 && d.threads < maxWorkers {
		maxWorkers = d.threads
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	params := searchParams{
		Query:      query,
		Region:     region,
		SafeSearch: safesearch,
		TimeLimit:  opts.TimeLimit,
		Page:       page,
		Extra:      opts.Extra,
	}

	aggregator := newResultsAggregator(category)
	var engineErrs []error

	// Streaming fan-out: keep up to maxWorkers engines in flight, consume
	// results as each engine finishes (no barrier), launch replacements as
	// slots free up, and cancel everything once max_results is reached.
	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan engineResult)
	seenProviders := map[string]bool{}
	launchedProviders := map[string]bool{}
	idx, inFlight := 0, 0

	launch := func() {
		for idx < len(engines) && inFlight < maxWorkers {
			e := engines[idx]
			idx++
			// One request per provider at a time; retry a provider only
			// if its earlier engine returned nothing.
			if seenProviders[e.Provider()] || launchedProviders[e.Provider()] {
				continue
			}
			launchedProviders[e.Provider()] = true
			inFlight++
			go func(e searchEngine) {
				results, err := e.Search(searchCtx, params)
				ch <- engineResult{
					key:      category + "/" + e.Name(),
					provider: e.Provider(),
					results:  results,
					err:      err,
				}
			}(e)
		}
	}

	launch()
	for inFlight > 0 {
		r := <-ch
		inFlight--
		cancelled := errors.Is(r.err, context.Canceled)
		ok := r.err == nil && len(r.results) > 0
		if !cancelled {
			// Health accounting: an error or an empty result set counts
			// as a failure (IP-blocked engines often return 200 with an
			// unparseable challenge page, i.e. zero results).
			d.recordEngineResult(r.key, ok, time.Now())
		}
		switch {
		case ok:
			aggregator.extend(r.results)
			seenProviders[r.provider] = true
		case r.err != nil && !cancelled:
			engineErrs = append(engineErrs, r.err)
			delete(launchedProviders, r.provider) // let another engine of this provider try
		default:
			delete(launchedProviders, r.provider)
		}

		if !unlimited && aggregator.len() >= maxResults {
			cancel() // enough results: stop in-flight engines
			// Drain remaining goroutines so none block on send; results
			// that already completed still count toward engine health.
			for inFlight > 0 {
				lr := <-ch
				inFlight--
				if !errors.Is(lr.err, context.Canceled) {
					d.recordEngineResult(lr.key, lr.err == nil && len(lr.results) > 0, time.Now())
				}
			}
			break
		}
		launch()
	}

	results := aggregator.extract()
	results = simpleFilterRanker{}.rank(results, query)

	if len(results) > 0 {
		if !unlimited && len(results) > maxResults {
			results = results[:maxResults]
		}
		return results, nil
	}

	if len(engineErrs) > 0 {
		return nil, errors.Join(engineErrs...)
	}
	return nil, ErrNoResults
}
