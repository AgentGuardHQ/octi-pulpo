package org

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/redis/go-redis/v9"
)

func testSetup(t *testing.T) (*OrgStore, context.Context) {
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

	return NewOrgStore(rdb, ns), ctx
}

func TestPutAndGet(t *testing.T) {
	store, ctx := testSetup(t)

	agent := Agent{
		Name:      "kernel-sr",
		Squad:     "kernel",
		Role:      "SR",
		ReportsTo: "kernel-em",
		Box:       "jared",
		Driver:    "claude-code",
	}

	if err := store.Put(ctx, agent); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "kernel-sr")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Squad != "kernel" {
		t.Errorf("expected squad=kernel, got %q", got.Squad)
	}
	if got.ReportsTo != "kernel-em" {
		t.Errorf("expected reports_to=kernel-em, got %q", got.ReportsTo)
	}
	if got.Name != "kernel-sr" {
		t.Errorf("expected name=kernel-sr, got %q", got.Name)
	}
}

func TestDirectReports(t *testing.T) {
	store, ctx := testSetup(t)

	em := Agent{Name: "kernel-em", Squad: "kernel", Role: "EM", ReportsTo: "director"}
	reports := []Agent{
		{Name: "kernel-sr", Squad: "kernel", Role: "SR", ReportsTo: "kernel-em"},
		{Name: "kernel-jr", Squad: "kernel", Role: "JR", ReportsTo: "kernel-em"},
		{Name: "kernel-qa", Squad: "kernel", Role: "QA", ReportsTo: "kernel-em"},
	}

	if err := store.Put(ctx, em); err != nil {
		t.Fatalf("Put EM: %v", err)
	}
	for _, r := range reports {
		if err := store.Put(ctx, r); err != nil {
			t.Fatalf("Put %s: %v", r.Name, err)
		}
	}

	names, err := store.DirectReports(ctx, "kernel-em")
	if err != nil {
		t.Fatalf("DirectReports: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 direct reports, got %d: %v", len(names), names)
	}

	// Verify sorted
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	for i := range names {
		if names[i] != sorted[i] {
			t.Fatalf("expected sorted result, got %v", names)
		}
	}
}

func TestChainOfCommand(t *testing.T) {
	store, ctx := testSetup(t)

	agents := []Agent{
		{Name: "jared", Role: "Board"},
		{Name: "director", Role: "Director", ReportsTo: "jared"},
		{Name: "kernel-em", Squad: "kernel", Role: "EM", ReportsTo: "director"},
		{Name: "kernel-sr", Squad: "kernel", Role: "SR", ReportsTo: "kernel-em"},
	}
	for _, a := range agents {
		if err := store.Put(ctx, a); err != nil {
			t.Fatalf("Put %s: %v", a.Name, err)
		}
	}

	chain, err := store.ChainOfCommand(ctx, "kernel-sr")
	if err != nil {
		t.Fatalf("ChainOfCommand: %v", err)
	}

	expected := []string{"kernel-sr", "kernel-em", "director", "jared"}
	if len(chain) != len(expected) {
		t.Fatalf("expected chain length %d, got %d: %v", len(expected), len(chain), chain)
	}
	for i, name := range expected {
		if chain[i] != name {
			t.Errorf("chain[%d]: expected %q, got %q", i, name, chain[i])
		}
	}
}

func TestAllAgents(t *testing.T) {
	store, ctx := testSetup(t)

	agents := []Agent{
		{Name: "kernel-sr", Squad: "kernel", Role: "SR"},
		{Name: "kernel-em", Squad: "kernel", Role: "EM"},
		{Name: "cloud-jr", Squad: "cloud", Role: "JR"},
	}
	for _, a := range agents {
		if err := store.Put(ctx, a); err != nil {
			t.Fatalf("Put %s: %v", a.Name, err)
		}
	}

	all, err := store.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(all))
	}
}
