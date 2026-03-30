package sprint

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func goalTestSetup(t *testing.T) (*GoalStore, context.Context) {
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

	ns := "octi-test-goals-" + t.Name()

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

	return NewGoalStore(rdb, ns), ctx
}

func TestGoalPutAndGet(t *testing.T) {
	gs, ctx := goalTestSetup(t)

	goal := Goal{
		ID:       "project-kernel-v3",
		Name:     "Kernel v3",
		ParentID: "mission-1",
		Squad:    "kernel",
	}

	if err := gs.Put(ctx, goal); err != nil {
		t.Fatalf("put goal: %v", err)
	}

	got, err := gs.Get(ctx, "project-kernel-v3")
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}

	if got.ID != "project-kernel-v3" {
		t.Errorf("expected id project-kernel-v3, got %s", got.ID)
	}
	if got.Name != "Kernel v3" {
		t.Errorf("expected name 'Kernel v3', got %s", got.Name)
	}
	if got.ParentID != "mission-1" {
		t.Errorf("expected parent_id mission-1, got %s", got.ParentID)
	}
	if got.Squad != "kernel" {
		t.Errorf("expected squad kernel, got %s", got.Squad)
	}
}

func TestGoalAncestry(t *testing.T) {
	gs, ctx := goalTestSetup(t)

	// Build a 3-level hierarchy: mission-1 → project-kernel-v3 → epic-hitl
	goals := []Goal{
		{ID: "mission-1", Name: "Ship Platform"},
		{ID: "project-kernel-v3", Name: "Kernel v3", ParentID: "mission-1"},
		{ID: "epic-hitl", Name: "HITL Loop", ParentID: "project-kernel-v3"},
	}
	for _, g := range goals {
		if err := gs.Put(ctx, g); err != nil {
			t.Fatalf("put goal %s: %v", g.ID, err)
		}
	}

	ancestry, err := gs.Ancestry(ctx, "epic-hitl")
	if err != nil {
		t.Fatalf("ancestry: %v", err)
	}

	if len(ancestry) != 3 {
		t.Fatalf("expected 3-element chain, got %d: %v", len(ancestry), ancestry)
	}
	if ancestry[0] != "epic-hitl" {
		t.Errorf("ancestry[0]: expected epic-hitl, got %s", ancestry[0])
	}
	if ancestry[1] != "project-kernel-v3" {
		t.Errorf("ancestry[1]: expected project-kernel-v3, got %s", ancestry[1])
	}
	if ancestry[2] != "mission-1" {
		t.Errorf("ancestry[2]: expected mission-1, got %s", ancestry[2])
	}
}

func TestGoalAncestryText(t *testing.T) {
	gs, ctx := goalTestSetup(t)

	goals := []Goal{
		{ID: "mission-1", Name: "Ship Platform"},
		{ID: "project-kernel-v3", Name: "Kernel v3", ParentID: "mission-1"},
		{ID: "epic-hitl", Name: "HITL Loop", ParentID: "project-kernel-v3"},
	}
	for _, g := range goals {
		if err := gs.Put(ctx, g); err != nil {
			t.Fatalf("put goal %s: %v", g.ID, err)
		}
	}

	text, err := gs.AncestryText(ctx, "epic-hitl")
	if err != nil {
		t.Fatalf("ancestry text: %v", err)
	}

	if text == "" {
		t.Fatal("expected non-empty ancestry text")
	}

	// Should contain the arrow separator
	expected := "Ship Platform \u2192 Kernel v3 \u2192 HITL Loop"
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}
