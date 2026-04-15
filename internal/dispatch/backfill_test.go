package dispatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBackfillAgentStats_HappyPath(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)
	d.SetProfiles(ps)

	seed := []DispatchRecord{
		{Agent: "alpha", Result: "dispatched", Timestamp: time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)},
		{Agent: "alpha", Result: "dispatched", Timestamp: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)},
		{Agent: "beta", Result: "dispatched", Timestamp: time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)},
		{Agent: "gamma", Result: "skipped", Reason: "cooldown", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		{Agent: "", Result: "dispatched", Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}
	for _, rec := range seed {
		data, _ := json.Marshal(rec)
		d.rdb.LPush(ctx, d.key("dispatch-log"), data)
	}

	rep, err := d.BackfillAgentStats(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if rep.Entries != 5 {
		t.Fatalf("want 5 entries scanned, got %d", rep.Entries)
	}
	if rep.DispatchesAdded != 3 {
		t.Fatalf("want 3 dispatches added, got %d", rep.DispatchesAdded)
	}
	if rep.AgentsUpdated != 2 {
		t.Fatalf("want 2 agents updated, got %d", rep.AgentsUpdated)
	}
	if rep.NonDispatched != 1 {
		t.Fatalf("want 1 non-dispatched, got %d", rep.NonDispatched)
	}
	if rep.MissingAgent != 1 {
		t.Fatalf("want 1 missing agent, got %d", rep.MissingAgent)
	}

	alpha, _ := ps.GetStats(ctx, "alpha")
	if alpha.DispatchesTotal != 2 {
		t.Fatalf("alpha want 2, got %d", alpha.DispatchesTotal)
	}
	beta, _ := ps.GetStats(ctx, "beta")
	if beta.DispatchesTotal != 1 {
		t.Fatalf("beta want 1, got %d", beta.DispatchesTotal)
	}

	// Idempotency: re-running must not double-count.
	rep2, err := d.BackfillAgentStats(ctx)
	if err != nil {
		t.Fatalf("backfill re-run: %v", err)
	}
	if rep2.DispatchesAdded != 0 {
		t.Fatalf("want 0 new dispatches on re-run, got %d", rep2.DispatchesAdded)
	}
	if rep2.Skipped != 3 {
		t.Fatalf("want 3 skipped on re-run, got %d", rep2.Skipped)
	}

	alpha2, _ := ps.GetStats(ctx, "alpha")
	if alpha2.DispatchesTotal != 2 {
		t.Fatalf("alpha after re-run want 2, got %d", alpha2.DispatchesTotal)
	}
}

func TestBackfillAgentStats_RequiresProfiles(t *testing.T) {
	d, ctx := testSetup(t)
	if _, err := d.BackfillAgentStats(ctx); err == nil {
		t.Fatalf("want error when profiles unset")
	}
}
