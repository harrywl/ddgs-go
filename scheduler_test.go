package ddgs

// Tests for the streaming scheduler in ddgs.go (DDGS.search): early
// cancellation, no barrier on slow engines, in-memory failure cooldown,
// and provider retry after empty results. Run by `go test ./...`.
// User instruction: scheduler optimizations "1,2,3 我觉得可以，
// 我觉得ddgs启动后可以在内存记录".

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEngine is a scriptable searchEngine for scheduler tests.
type fakeEngine struct {
	name     string
	provider string
	priority float64
	delay    time.Duration
	results  []Result
	err      error
	calls    atomic.Int32
}

func (f *fakeEngine) Name() string      { return f.name }
func (f *fakeEngine) Category() string  { return "text" }
func (f *fakeEngine) Provider() string  { return f.provider }
func (f *fakeEngine) Priority() float64 { return f.priority }
func (f *fakeEngine) Search(ctx context.Context, _ searchParams) ([]Result, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.results, f.err
}

func fakeResults(prefix string, n int) []Result {
	out := make([]Result, n)
	for i := range out {
		out[i] = Result{
			"title": prefix + " title",
			"href":  "https://" + prefix + ".com/" + string(rune('a'+i)),
			"body":  prefix + " body",
		}
	}
	return out
}

// withFakeRegistry swaps the text registry for the given engines.
func withFakeRegistry(t *testing.T, engines ...*fakeEngine) {
	t.Helper()
	saved := engineRegistry["text"]
	reg := map[string]engineFactory{}
	for _, e := range engines {
		e := e
		reg[e.name] = func(string, time.Duration, Verify) (searchEngine, error) { return e, nil }
	}
	engineRegistry["text"] = reg
	t.Cleanup(func() { engineRegistry["text"] = saved })
}

func TestSchedulerSlowEngineDoesNotBlock(t *testing.T) {
	fast := &fakeEngine{name: "fast", provider: "p1", results: fakeResults("fast", 10)}
	slow := &fakeEngine{name: "slow", provider: "p2", delay: 5 * time.Second, results: fakeResults("slow", 10)}
	withFakeRegistry(t, fast, slow)

	d, _ := New(nil)
	start := time.Now()
	results, err := d.Text(context.Background(), "fast title", &SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	// fast alone satisfies max_results; the 5s engine must not gate the return.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("search blocked on slow engine: took %v", elapsed)
	}
}

func TestSchedulerFailureCooldown(t *testing.T) {
	failing := &fakeEngine{name: "failing", provider: "p1", err: errors.New("blocked")}
	good := &fakeEngine{name: "good", provider: "p2", results: fakeResults("good", 10)}
	withFakeRegistry(t, failing, good)

	d, _ := New(nil)
	// Enough failing searches to put "failing" on cooldown.
	for i := 0; i < failuresBeforeCooldown; i++ {
		if _, err := d.Text(context.Background(), "good title", &SearchOptions{MaxResults: 5}); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	callsBefore := failing.calls.Load()
	if callsBefore < failuresBeforeCooldown {
		t.Fatalf("failing engine called %d times, want >= %d", callsBefore, failuresBeforeCooldown)
	}

	// Next auto search must skip the cooled-down engine.
	if _, err := d.Text(context.Background(), "good title", &SearchOptions{MaxResults: 5}); err != nil {
		t.Fatalf("post-cooldown search: %v", err)
	}
	if got := failing.calls.Load(); got != callsBefore {
		t.Errorf("cooled-down engine was called again: %d -> %d", callsBefore, got)
	}

	// Direct backend selection bypasses the cooldown.
	_, _ = d.Text(context.Background(), "x", &SearchOptions{Backend: "failing"})
	if got := failing.calls.Load(); got != callsBefore+1 {
		t.Errorf("direct backend should bypass cooldown: %d -> %d", callsBefore, got)
	}
}

func TestSchedulerCooldownExpires(t *testing.T) {
	d, _ := New(nil)
	key := "text/x"
	now := time.Now()
	for i := 0; i < failuresBeforeCooldown; i++ {
		d.recordEngineResult(key, false, now)
	}
	if !d.engineOnCooldown(key, now) {
		t.Fatal("engine should be on cooldown after repeated failures")
	}
	if d.engineOnCooldown(key, now.Add(failureCooldown+time.Second)) {
		t.Fatal("cooldown should expire")
	}
	// Success resets state entirely.
	d.recordEngineResult(key, true, now)
	if d.engineOnCooldown(key, now) {
		t.Fatal("success should clear cooldown state")
	}
}

func TestSchedulerProviderRetryAfterEmpty(t *testing.T) {
	// Two engines share a provider; the first returns empty, the second
	// must still be tried (launchedProviders released on empty).
	empty := &fakeEngine{name: "empty", provider: "shared", priority: 2, results: nil}
	full := &fakeEngine{name: "full", provider: "shared", priority: 1, results: fakeResults("full", 5)}
	withFakeRegistry(t, empty, full)

	d, _ := New(&Config{Threads: 1}) // serialize so priority order is deterministic
	results, err := d.Text(context.Background(), "full title", &SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d, want 5", len(results))
	}
	if empty.calls.Load() != 1 || full.calls.Load() != 1 {
		t.Errorf("calls: empty=%d full=%d, want 1/1", empty.calls.Load(), full.calls.Load())
	}
}
