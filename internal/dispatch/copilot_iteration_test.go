package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// --- minimal Redis mock implementing iterationRedis ---
//
// The iteration loop's surface is narrower than the fix loop's — Incr /
// Expire / SetNX / Del. We keep a separate mock so the two tests evolve
// independently (the fix-loop mock cannot grow SetNX without tangling).

type mockIterRedis struct {
	mu       sync.Mutex
	counters map[string]int64
	setnx    map[string]bool // simulates existence of debounce keys
}

func newMockIterRedis() *mockIterRedis {
	return &mockIterRedis{
		counters: make(map[string]int64),
		setnx:    make(map[string]bool),
	}
}

func (m *mockIterRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	m.mu.Lock()
	m.counters[key]++
	val := m.counters[key]
	m.mu.Unlock()
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(val)
	return cmd
}

func (m *mockIterRedis) Expire(ctx context.Context, key string, ttl time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}

// SetNX returns false on the second call for the same key within process
// lifetime — simulates the debounce window being honored. A test that
// wants to "expire" the window calls clearDebounce.
func (m *mockIterRedis) SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) *redis.BoolCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewBoolCmd(ctx)
	if m.setnx[key] {
		cmd.SetVal(false)
		return cmd
	}
	m.setnx[key] = true
	cmd.SetVal(true)
	return cmd
}

func (m *mockIterRedis) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	var n int64
	for _, k := range keys {
		if _, ok := m.counters[k]; ok {
			delete(m.counters, k)
			n++
		}
		if _, ok := m.setnx[k]; ok {
			delete(m.setnx, k)
			n++
		}
	}
	m.mu.Unlock()
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(n)
	return cmd
}

func (m *mockIterRedis) clearDebounce() {
	m.mu.Lock()
	m.setnx = make(map[string]bool)
	m.mu.Unlock()
}

// --- GH mock that records both comments AND label calls ---

type ghCall struct {
	Path string
	Body string
}

func newIterGHMock(t *testing.T) (*httptest.Server, *[]ghCall) {
	t.Helper()
	var calls []ghCall
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, ghCall{Path: r.URL.Path, Body: string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newIterLoopForTest(rdb *mockIterRedis, baseURL string) *CopilotIterationLoop {
	return &CopilotIterationLoop{
		ghToken:     "tok",
		rdb:         rdb,
		baseURL:     baseURL,
		maxAttempts: CopilotIterationMaxAttempts,
	}
}

// An actionable review body — has a `## ` heading so the heuristic treats
// it as worth iterating on.
const actionableReview = "## Findings\nsome feedback"

func validInput() ReviewInput {
	return ReviewInput{
		Repo:        "chitinhq/demo",
		PRNumber:    7,
		ReviewerBot: "copilot-pull-request-reviewer",
		ReviewState: "commented",
		PRAuthor:    "copilot-swe-agent[bot]",
		IsDraft:     false,
		ReviewBody:  actionableReview,
	}
}

func ctx5s(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// --- tests ---

// TestCopilotIteration_TriggersOnCommentedReview confirms the happy path:
// a COMMENTED review from the auto-reviewer on a Copilot-authored PR
// produces exactly one @copilot mention.
func TestCopilotIteration_TriggersOnCommentedReview(t *testing.T) {
	srv, calls := newIterGHMock(t)
	rdb := newMockIterRedis()
	loop := newIterLoopForTest(rdb, srv.URL)

	ctx, cancel := ctx5s(t)
	defer cancel()

	triggered, err := loop.HandleReview(ctx, validInput())
	if err != nil {
		t.Fatalf("HandleReview: %v", err)
	}
	if !triggered {
		t.Fatalf("expected triggered=true")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 GH call, got %d", len(*calls))
	}
	if !strings.Contains((*calls)[0].Body, "@copilot please address") {
		t.Errorf("expected @copilot mention, got body=%q", (*calls)[0].Body)
	}
	if !strings.HasSuffix((*calls)[0].Path, "/issues/7/comments") {
		t.Errorf("expected comments endpoint, got %s", (*calls)[0].Path)
	}
}

// TestCopilotIteration_RespectsMaxCap confirms the 4th attempt labels
// the PR tier:human instead of posting another mention.
func TestCopilotIteration_RespectsMaxCap(t *testing.T) {
	srv, calls := newIterGHMock(t)
	rdb := newMockIterRedis()
	loop := newIterLoopForTest(rdb, srv.URL)

	ctx, cancel := ctx5s(t)
	defer cancel()

	// Fire 4 times, clearing the debounce between each so the cap — not
	// the debounce — gates the last call.
	for i := 0; i < 4; i++ {
		if _, err := loop.HandleReview(ctx, validInput()); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		rdb.clearDebounce()
	}

	// Expect 3 comment posts + 1 label call.
	var comments, labels int
	for _, c := range *calls {
		switch {
		case strings.HasSuffix(c.Path, "/issues/7/comments"):
			comments++
		case strings.HasSuffix(c.Path, "/issues/7/labels"):
			labels++
		}
	}
	if comments != 3 {
		t.Errorf("expected 3 comments, got %d", comments)
	}
	if labels != 1 {
		t.Errorf("expected 1 label call, got %d", labels)
	}
	// Check label body has tier:human.
	var sawTierHuman bool
	for _, c := range *calls {
		if strings.Contains(c.Body, `"tier:human"`) {
			sawTierHuman = true
		}
	}
	if !sawTierHuman {
		t.Errorf("expected tier:human label in one call; calls=%+v", *calls)
	}
}

// TestCopilotIteration_SkipsHumanAuthoredPR — author != copilot-swe-agent
// must not fire the loop even on a COMMENTED auto-review.
func TestCopilotIteration_SkipsHumanAuthoredPR(t *testing.T) {
	srv, calls := newIterGHMock(t)
	rdb := newMockIterRedis()
	loop := newIterLoopForTest(rdb, srv.URL)

	ctx, cancel := ctx5s(t)
	defer cancel()

	in := validInput()
	in.PRAuthor = "jared" // human

	triggered, err := loop.HandleReview(ctx, in)
	if err != nil {
		t.Fatalf("HandleReview: %v", err)
	}
	if triggered {
		t.Errorf("expected triggered=false for human-authored PR")
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 GH calls, got %d", len(*calls))
	}
}

// TestCopilotIteration_SkipsDraft — draft PRs are considered in-progress
// and must not be iterated.
func TestCopilotIteration_SkipsDraft(t *testing.T) {
	srv, calls := newIterGHMock(t)
	rdb := newMockIterRedis()
	loop := newIterLoopForTest(rdb, srv.URL)

	ctx, cancel := ctx5s(t)
	defer cancel()

	in := validInput()
	in.IsDraft = true

	triggered, err := loop.HandleReview(ctx, in)
	if err != nil {
		t.Fatalf("HandleReview: %v", err)
	}
	if triggered {
		t.Errorf("expected triggered=false for draft PR")
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 GH calls, got %d", len(*calls))
	}
}

// TestCopilotIteration_DebouncesQuickFires — two fires within the debounce
// window produce only a single @copilot mention.
func TestCopilotIteration_DebouncesQuickFires(t *testing.T) {
	srv, calls := newIterGHMock(t)
	rdb := newMockIterRedis()
	loop := newIterLoopForTest(rdb, srv.URL)

	ctx, cancel := ctx5s(t)
	defer cancel()

	if _, err := loop.HandleReview(ctx, validInput()); err != nil {
		t.Fatalf("first HandleReview: %v", err)
	}
	triggered, err := loop.HandleReview(ctx, validInput())
	if err != nil {
		t.Fatalf("second HandleReview: %v", err)
	}
	if triggered {
		t.Errorf("expected second fire to be debounced")
	}
	// Only one comment posted — debounce won.
	var comments int
	for _, c := range *calls {
		if strings.HasSuffix(c.Path, "/issues/7/comments") {
			comments++
		}
	}
	if comments != 1 {
		t.Errorf("expected 1 comment (debounced), got %d", comments)
	}
}

// silence unused-import warnings if json is later dropped
var _ = json.Marshal
