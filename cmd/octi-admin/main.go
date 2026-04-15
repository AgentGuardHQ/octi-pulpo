// Command octi-admin provides one-shot admin utilities for Octi's Redis state.
//
// Subcommands:
//
//	backfill-agent-stats  — replay octi:dispatch-log into octi:agent_stats:{agent}
//	                        hashes so the leaderboard reflects the population that
//	                        existed before PR #233 wired the sink. Idempotent.
//
// Usage:
//
//	octi-admin backfill-agent-stats
//	OCTI_REDIS_URL=redis://... OCTI_NAMESPACE=octi octi-admin backfill-agent-stats
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "backfill-agent-stats":
		if err := runBackfill(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "backfill: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: octi-admin <subcommand>")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  backfill-agent-stats  replay dispatch-log into agent_stats (idempotent)")
}

func runBackfill(ctx context.Context) error {
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	namespace := os.Getenv("OCTI_NAMESPACE")
	if namespace == "" {
		namespace = "octi"
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}

	coord, err := coordination.New(redisURL, namespace)
	if err != nil {
		return fmt.Errorf("coordination: %w", err)
	}
	defer coord.Close()

	router := routing.NewRouter(os.Getenv("CHITIN_HEALTH_DIR"))
	events := dispatch.NewEventRouter(nil)
	d := dispatch.NewDispatcher(rdb, router, coord, events, "", namespace)
	ps := dispatch.NewProfileStore(rdb, namespace, events.CooldownFor)
	d.SetProfiles(ps)

	start := time.Now()
	rep, err := d.BackfillAgentStats(ctx)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	fmt.Printf("backfill complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  entries scanned:         %d\n", rep.Entries)
	fmt.Printf("  dispatches added:        %d\n", rep.DispatchesAdded)
	fmt.Printf("  agents updated:          %d\n", rep.AgentsUpdated)
	fmt.Printf("  skipped (already done):  %d\n", rep.Skipped)
	fmt.Printf("  non-dispatched entries:  %d\n", rep.NonDispatched)
	fmt.Printf("  missing agent field:     %d\n", rep.MissingAgent)

	if len(rep.PerAgent) > 0 {
		fmt.Println("per-agent dispatches added:")
		out, _ := json.MarshalIndent(rep.PerAgent, "  ", "  ")
		fmt.Printf("  %s\n", string(out))
	}
	return nil
}
