package crosssquad

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

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

	ns := "octi-test-" + t.Name()
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

func TestSubmit_StoresPendingRequest(t *testing.T) {
	store, ctx := testStore(t)

	req, err := store.Submit(ctx, "marketing-em", "analytics", "report", "Need PR velocity report for LinkedIn", 1, 60)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if req.ID == "" {
		t.Fatal("expected non-empty request ID")
	}
	if req.Status != "pending" {
		t.Fatalf("expected status=pending, got %s", req.Status)
	}
	if req.FromAgent != "marketing-em" {
		t.Fatalf("expected from_agent=marketing-em, got %s", req.FromAgent)
	}
	if req.ToSquad != "analytics" {
		t.Fatalf("expected to_squad=analytics, got %s", req.ToSquad)
	}
}

func TestPending_ReturnsPriorityOrdered(t *testing.T) {
	store, ctx := testStore(t)

	// Submit in reverse priority order: normal first, then urgent.
	_, err := store.Submit(ctx, "agent-a", "analytics", "report", "normal task", 2, 0)
	if err != nil {
		t.Fatalf("submit normal: %v", err)
	}
	_, err = store.Submit(ctx, "agent-b", "analytics", "query", "urgent task", 0, 10)
	if err != nil {
		t.Fatalf("submit urgent: %v", err)
	}

	requests, err := store.Pending(ctx, "analytics")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	// Urgent (priority 0) must sort before normal (priority 2).
	if requests[0].Priority != 0 {
		t.Fatalf("expected first request to be urgent (priority 0), got priority %d", requests[0].Priority)
	}
	if requests[1].Priority != 2 {
		t.Fatalf("expected second request to be normal (priority 2), got priority %d", requests[1].Priority)
	}
}

func TestPending_EmptySquad(t *testing.T) {
	store, ctx := testStore(t)

	requests, err := store.Pending(ctx, "no-such-squad")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestFulfill_MarksRequestFulfilled(t *testing.T) {
	store, ctx := testStore(t)

	req, err := store.Submit(ctx, "kernel-sr", "analytics", "query", "What is the P99 latency for /api/health?", 1, 0)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	fulfilled, err := store.Fulfill(ctx, req.ID, "analytics-sr", "P99 latency is 42ms. See report at reports/latency-2026-03-29.md", 1415)
	if err != nil {
		t.Fatalf("fulfill: %v", err)
	}

	if fulfilled.Status != "fulfilled" {
		t.Fatalf("expected status=fulfilled, got %s", fulfilled.Status)
	}
	if fulfilled.FulfilledBy != "analytics-sr" {
		t.Fatalf("expected fulfilled_by=analytics-sr, got %s", fulfilled.FulfilledBy)
	}
	if fulfilled.PRNumber != 1415 {
		t.Fatalf("expected pr_number=1415, got %d", fulfilled.PRNumber)
	}

	// Request should no longer appear in pending list.
	pending, err := store.Pending(ctx, "analytics")
	if err != nil {
		t.Fatalf("pending after fulfill: %v", err)
	}
	for _, r := range pending {
		if r.ID == req.ID {
			t.Fatalf("fulfilled request still appears in pending list")
		}
	}
}

func TestFulfill_UnknownRequestReturnsError(t *testing.T) {
	store, ctx := testStore(t)

	_, err := store.Fulfill(ctx, "req-does-not-exist", "some-agent", "done", 0)
	if err == nil {
		t.Fatal("expected error for unknown request ID, got nil")
	}
}
