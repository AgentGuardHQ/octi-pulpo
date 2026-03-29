package crosssquad

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testStore creates a Store backed by real Redis. Skips if Redis is unavailable.
func testStore(t *testing.T) (*Store, context.Context) {
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

	ns := "octi-crosssquad-test-" + t.Name()
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		rdb.Close()
	})

	return New(rdb, ns), ctx
}

func TestSubmitAndForSquad(t *testing.T) {
	s, ctx := testStore(t)

	id, err := s.Submit(ctx, Request{
		FromAgent:   "marketing-em",
		ToSquad:     "analytics",
		Type:        TypeReport,
		Description: "Need weekly PR velocity report",
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id == "" {
		t.Fatal("Submit returned empty ID")
	}

	requests, err := s.ForSquad(ctx, "analytics")
	if err != nil {
		t.Fatalf("ForSquad: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("ForSquad: got %d requests, want 1", len(requests))
	}
	req := requests[0]
	if req.ID != id {
		t.Errorf("ID mismatch: got %q, want %q", req.ID, id)
	}
	if req.FromAgent != "marketing-em" {
		t.Errorf("FromAgent: got %q, want %q", req.FromAgent, "marketing-em")
	}
	if req.Status != StatusPending {
		t.Errorf("Status: got %q, want %q", req.Status, StatusPending)
	}
}

func TestForSquad_EmptyForOtherSquad(t *testing.T) {
	s, ctx := testStore(t)

	_, err := s.Submit(ctx, Request{
		FromAgent:   "kernel-em",
		ToSquad:     "shellforge",
		Type:        TypeFix,
		Description: "Fix governance bypass",
		Priority:    0,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// cloud has no requests
	requests, err := s.ForSquad(ctx, "cloud")
	if err != nil {
		t.Fatalf("ForSquad cloud: %v", err)
	}
	if len(requests) != 0 {
		t.Errorf("expected 0 requests for cloud, got %d", len(requests))
	}
}

func TestFulfill(t *testing.T) {
	s, ctx := testStore(t)

	id, err := s.Submit(ctx, Request{
		FromAgent:   "kernel-sr",
		ToSquad:     "analytics",
		Type:        TypeQuery,
		Description: "How many PRs merged this week?",
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := s.Fulfill(ctx, id, "23 PRs merged (2026-W13)", 0); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}

	// Should be removed from pending set
	requests, err := s.ForSquad(ctx, "analytics")
	if err != nil {
		t.Fatalf("ForSquad after fulfill: %v", err)
	}
	if len(requests) != 0 {
		t.Errorf("expected 0 pending requests after fulfill, got %d", len(requests))
	}

	// But Get should still return it (kept 24h for observability)
	req, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after fulfill: %v", err)
	}
	if req.Status != StatusFulfilled {
		t.Errorf("Status: got %q, want %q", req.Status, StatusFulfilled)
	}
	if req.Result != "23 PRs merged (2026-W13)" {
		t.Errorf("Result: got %q", req.Result)
	}
}

func TestFulfill_WithPR(t *testing.T) {
	s, ctx := testStore(t)

	id, err := s.Submit(ctx, Request{
		FromAgent:   "kernel-em",
		ToSquad:     "cloud",
		Type:        TypeDeploy,
		Description: "Deploy hotfix to staging",
		Priority:    0,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := s.Fulfill(ctx, id, "deployed to staging", 1234); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}

	req, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req.PRNumber != 1234 {
		t.Errorf("PRNumber: got %d, want 1234", req.PRNumber)
	}
}

func TestPendingSquads(t *testing.T) {
	s, ctx := testStore(t)

	// No requests yet
	squads, err := s.PendingSquads(ctx)
	if err != nil {
		t.Fatalf("PendingSquads (empty): %v", err)
	}
	if len(squads) != 0 {
		t.Errorf("expected 0 squads, got %d", len(squads))
	}

	// Add requests for two different squads
	_, err = s.Submit(ctx, Request{FromAgent: "a", ToSquad: "analytics", Type: TypeReport, Description: "x", Priority: 1})
	if err != nil {
		t.Fatalf("Submit analytics: %v", err)
	}
	_, err = s.Submit(ctx, Request{FromAgent: "b", ToSquad: "shellforge", Type: TypeFix, Description: "y", Priority: 0})
	if err != nil {
		t.Fatalf("Submit shellforge: %v", err)
	}

	squads, err = s.PendingSquads(ctx)
	if err != nil {
		t.Fatalf("PendingSquads: %v", err)
	}
	if len(squads) != 2 {
		t.Errorf("expected 2 pending squads, got %d: %v", len(squads), squads)
	}

	// Fulfill the analytics request — should drop from pending
	reqs, _ := s.ForSquad(ctx, "analytics")
	if len(reqs) > 0 {
		_ = s.Fulfill(ctx, reqs[0].ID, "done", 0)
	}

	squads, err = s.PendingSquads(ctx)
	if err != nil {
		t.Fatalf("PendingSquads after fulfill: %v", err)
	}
	if len(squads) != 1 {
		t.Errorf("expected 1 pending squad after fulfill, got %d: %v", len(squads), squads)
	}
	if squads[0] != "shellforge" {
		t.Errorf("expected shellforge, got %q", squads[0])
	}
}

func TestPriorityOrdering(t *testing.T) {
	s, ctx := testStore(t)

	// Submit in reverse priority order
	_, _ = s.Submit(ctx, Request{FromAgent: "a", ToSquad: "kernel", Type: TypeFix, Description: "normal", Priority: 2})
	_, _ = s.Submit(ctx, Request{FromAgent: "b", ToSquad: "kernel", Type: TypeFix, Description: "urgent", Priority: 0})
	_, _ = s.Submit(ctx, Request{FromAgent: "c", ToSquad: "kernel", Type: TypeFix, Description: "high", Priority: 1})

	requests, err := s.ForSquad(ctx, "kernel")
	if err != nil {
		t.Fatalf("ForSquad: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requests))
	}
	// First should be urgent (priority 0), then high (1), then normal (2)
	if requests[0].Description != "urgent" {
		t.Errorf("first request should be urgent, got %q", requests[0].Description)
	}
}

func TestFulfill_NotFound(t *testing.T) {
	s, ctx := testStore(t)

	err := s.Fulfill(ctx, "req-nonexistent-999", "done", 0)
	if err == nil {
		t.Error("expected error for nonexistent request ID, got nil")
	}
}
