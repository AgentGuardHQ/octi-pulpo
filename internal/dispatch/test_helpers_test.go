package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

// testSetup creates a Dispatcher backed by real Redis for integration tests.
// Requires Redis on localhost:6379 (the standard dev setup). Skips when no
// Redis is available.
func testSetup(t *testing.T) (*Dispatcher, context.Context) {
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

	// Unique namespace per test to avoid cross-contamination.
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

	healthDir := t.TempDir()
	writeHealthFile(t, healthDir, "claude-code", "CLOSED")

	coord, err := coordination.New(redisURL, ns)
	if err != nil {
		t.Fatalf("coordination engine: %v", err)
	}
	t.Cleanup(func() { coord.Close() })

	router := routing.NewRouterWithTiers(healthDir, map[string]routing.CostTier{"claude-code": routing.TierCLI})
	eventRouter := NewEventRouter(DefaultRules())

	queueFile := filepath.Join(t.TempDir(), "queue.txt")

	d := NewDispatcher(rdb, router, coord, eventRouter, queueFile, ns)
	return d, ctx
}

func writeHealthFile(t *testing.T, dir, driver, state string) {
	t.Helper()
	hf := map[string]interface{}{"state": state, "failures": 0}
	data, _ := json.Marshal(hf)
	if err := os.WriteFile(filepath.Join(dir, driver+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}
