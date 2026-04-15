package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDispatchID_MintedAndRecorded asserts that every recordDispatch call
// produces a DispatchRecord with a non-empty dispatch_id, and that the id
// on the DispatchResult matches the id persisted to Redis. This is the
// join key Sentinel's DetectDispatchOrphans pass (sentinel#70) relies on.
// See octi#257.
func TestDispatchID_MintedAndRecorded(t *testing.T) {
	d, ctx := testSetup(t)

	event := Event{Type: EventManual, Source: "test"}
	result, err := d.Dispatch(ctx, event, "test-agent-id", 2)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if result.DispatchID == "" {
		t.Fatal("expected DispatchResult.DispatchID to be minted, got empty")
	}
	if len(result.DispatchID) < 16 {
		t.Fatalf("expected DispatchID to have >=16 chars of entropy, got %q", result.DispatchID)
	}

	recent, err := d.RecentDispatches(ctx, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) == 0 {
		t.Fatal("expected at least one DispatchRecord in redis")
	}
	var match bool
	for _, r := range recent {
		if r.DispatchID == "" {
			t.Errorf("DispatchRecord persisted with empty dispatch_id (agent=%s result=%s)", r.Agent, r.Result)
		}
		if r.DispatchID == result.DispatchID {
			match = true
		}
	}
	if !match {
		t.Fatalf("DispatchResult.DispatchID=%q not found in persisted records", result.DispatchID)
	}
}

// TestDispatchID_UniquePerDispatch guards against any accidental
// deterministic/reused id (e.g. fallback time-based path firing under load).
func TestDispatchID_UniquePerDispatch(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		id := newDispatchID()
		if id == "" {
			t.Fatal("newDispatchID returned empty")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newDispatchID collision at iteration %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestGHActionsAdapter_PropagatesDispatchID asserts the gh-actions adapter
// serialises Task.DispatchID into client_payload.dispatch_id on the
// repository_dispatch POST body. Without this, downstream `gh run` events
// carry no join key and Sentinel falls back to brittle (agent, repo, ts)
// joins — the exact regression tracked by octi#257 / workspace#408.
func TestGHActionsAdapter_PropagatesDispatchID(t *testing.T) {
	var captured ghDispatchPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	adapter := &GHActionsAdapter{token: "fake", baseURL: srv.URL}
	task := &Task{
		ID:         "test-task-1",
		Type:       "code-gen",
		Repo:       "chitinhq/octi",
		Priority:   "NORMAL",
		DispatchID: "abc123def456abc123def456abc12345",
	}

	res, err := adapter.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected status=queued, got %s", res.Status)
	}
	if captured.ClientPayload.DispatchID != task.DispatchID {
		t.Fatalf("client_payload.dispatch_id mismatch: got %q want %q",
			captured.ClientPayload.DispatchID, task.DispatchID)
	}
	if captured.ClientPayload.TaskID != task.ID {
		t.Fatalf("client_payload.task_id mismatch: got %q want %q",
			captured.ClientPayload.TaskID, task.ID)
	}
}
