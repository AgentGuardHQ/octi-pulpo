package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
	"github.com/redis/go-redis/v9"
)

// testRedis returns a Redis client and a unique namespace for the test.
// The test is skipped when Redis is not reachable.
func testRedis(t *testing.T) (*redis.Client, string, context.Context) {
	t.Helper()
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}
	ns := "octi-test-briefing-" + t.Name()
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() { cleanup(); rdb.Close() })
	return rdb, ns, ctx
}

func TestBuildDailyBriefing_PassRate(t *testing.T) {
	rdb, ns, ctx := testRedis(t)
	rdb.Set(ctx, ns+":worker-ok", "80", 0)
	rdb.Set(ctx, ns+":worker-fail", "20", 0)

	b := BuildDailyBriefing(ctx, rdb, ns, nil, nil)

	if b.WorkerOK != 80 || b.WorkerFail != 20 {
		t.Errorf("expected 80/20, got %d/%d", b.WorkerOK, b.WorkerFail)
	}
	if b.PassRate < 79 || b.PassRate > 81 {
		t.Errorf("expected pass rate ~80%%, got %.1f%%", b.PassRate)
	}
}

func TestBuildDailyBriefing_SprintFields(t *testing.T) {
	rdb, ns, ctx := testRedis(t)

	items := []sprint.SprintItem{
		{IssueNum: 1, Repo: "AgentGuardHQ/octi-pulpo", Title: "shipped", Status: "done", Priority: 2, PRNumber: 10},
		{IssueNum: 2, Repo: "AgentGuardHQ/octi-pulpo", Title: "P0 open", Status: "open", Priority: 0},
		{IssueNum: 3, Repo: "AgentGuardHQ/octi-pulpo", Title: "blocked", Status: "open", Priority: 2, DependsOn: []int{99}},
		{IssueNum: 4, Repo: "AgentGuardHQ/octi-pulpo", Title: "PR in flight", Status: "pr_open", Priority: 1, PRNumber: 42},
	}

	b := BuildDailyBriefing(ctx, rdb, ns, nil, items)

	if len(b.ShippedPRs) != 2 { // item 1 (done+PR) + item 4 (pr_open+PR)
		t.Errorf("expected 2 shipped PRs, got %d: %v", len(b.ShippedPRs), b.ShippedPRs)
	}
	if len(b.OpenP0s) != 1 || b.OpenP0s[0].IssueNum != 2 {
		t.Errorf("expected 1 P0 open (issue 2), got %v", b.OpenP0s)
	}
	if len(b.Blocked) != 1 || b.Blocked[0].IssueNum != 3 {
		t.Errorf("expected 1 blocked (issue 3), got %v", b.Blocked)
	}
}

func TestBuildDailyBriefing_DepSatisfied(t *testing.T) {
	rdb, ns, ctx := testRedis(t)

	items := []sprint.SprintItem{
		{IssueNum: 99, Repo: "AgentGuardHQ/octi-pulpo", Title: "dep done", Status: "done", Priority: 2},
		{IssueNum: 3, Repo: "AgentGuardHQ/octi-pulpo", Title: "depends on 99", Status: "open", Priority: 2, DependsOn: []int{99}},
	}

	b := BuildDailyBriefing(ctx, rdb, ns, nil, items)

	if len(b.Blocked) != 0 {
		t.Errorf("expected no blocked items when dep is done, got %v", b.Blocked)
	}
}

func TestBuildDailyBriefing_RecentFails(t *testing.T) {
	rdb, ns, ctx := testRedis(t)

	// Push a failed worker result.
	fail := map[string]interface{}{
		"agent":        "octi-pulpo-sr",
		"exit_code":    1,
		"duration_sec": 3.0,
		"had_commits":  false,
		"timestamp":    "2026-03-30T00:00:00Z",
	}
	data, _ := json.Marshal(fail)
	rdb.LPush(ctx, ns+":worker-results", string(data))

	b := BuildDailyBriefing(ctx, rdb, ns, nil, nil)

	if len(b.RecentFails) != 1 || b.RecentFails[0].Agent != "octi-pulpo-sr" {
		t.Errorf("expected 1 recent fail for octi-pulpo-sr, got %v", b.RecentFails)
	}
}

func TestPostDailyBriefing_NoopWhenDisabled(t *testing.T) {
	n := NewNotifier("")
	err := n.PostDailyBriefing(context.Background(), DailyBriefing{Date: "2026-03-30"})
	if err != nil {
		t.Fatalf("expected no error for disabled notifier, got %v", err)
	}
}

func TestPostDailyBriefing_Content(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(srv.URL)
	b := DailyBriefing{
		Date:       "2026-03-30",
		PassRate:   75.0,
		WorkerOK:   75,
		WorkerFail: 25,
		Drivers: []routing.DriverHealth{
			{Name: "claude-code", CircuitState: "CLOSED"},
			{Name: "codex", CircuitState: "OPEN", Failures: 3},
		},
		ShippedPRs: []sprint.SprintItem{
			{IssueNum: 5, Repo: "AgentGuardHQ/octi-pulpo", Title: "feat shipped", Status: "pr_open", PRNumber: 42},
		},
		OpenP0s: []sprint.SprintItem{
			{IssueNum: 2, Repo: "AgentGuardHQ/octi-pulpo", Title: "urgent bug"},
		},
		Blocked: []sprint.SprintItem{
			{IssueNum: 7, Repo: "AgentGuardHQ/octi-pulpo", Title: "blocked feat", DependsOn: []int{6}},
		},
	}

	if err := n.PostDailyBriefing(context.Background(), b); err != nil {
		t.Fatalf("PostDailyBriefing: %v", err)
	}

	body := string(received)
	for _, want := range []string{"Daily CTO Briefing", "75.0%", "claude-code", "PR #42", "urgent bug", "blocked feat"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in Slack body, got: %s", want, body)
		}
	}

	// Verify blocks array with action buttons is present.
	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	blocks, ok := payload["blocks"].([]interface{})
	if !ok || len(blocks) < 2 {
		t.Errorf("expected blocks with at least 2 elements (section + actions), got: %v", payload)
	}
}
