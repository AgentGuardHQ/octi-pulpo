package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func TestNotifier_Enabled(t *testing.T) {
	if NewNotifier("").Enabled() {
		t.Fatal("empty URL should not be enabled")
	}
	if !NewNotifier("http://example.com/hook").Enabled() {
		t.Fatal("non-empty URL should be enabled")
	}
}

func TestNotifier_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNotifier("")

	// All Post* calls must return nil without making any HTTP requests.
	if err := n.PostBudgetDashboard(ctx, nil, 0, 0); err != nil {
		t.Fatalf("PostBudgetDashboard: %v", err)
	}
	if err := n.PostDriversDown(ctx, "desc"); err != nil {
		t.Fatalf("PostDriversDown: %v", err)
	}
	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("PostDriversRecovered: %v", err)
	}
}

func TestNotifier_PostBudgetDashboard(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", Failures: 0},
		{Name: "copilot", CircuitState: "OPEN", Failures: 12},
	}

	if err := n.PostBudgetDashboard(ctx, drivers, 80, 20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "claude-code") {
		t.Error("expected claude-code in dashboard text")
	}
	if !strings.Contains(text, "copilot") {
		t.Error("expected copilot in dashboard text")
	}
	if !strings.Contains(text, "80.0%") {
		t.Errorf("expected 80.0%% pass rate, got: %s", text)
	}
}

func TestNotifier_PostDriversDown(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversDown(ctx, "all circuit breakers OPEN"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "All Drivers Exhausted") {
		t.Errorf("expected 'All Drivers Exhausted' in text, got: %s", text)
	}
	if !strings.Contains(text, "all circuit breakers OPEN") {
		t.Errorf("expected description in text, got: %s", text)
	}
}

func TestNotifier_PostDriversRecovered(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	if err := n.PostDriversRecovered(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON payload: %v", err)
	}

	text, _ := payload["text"].(string)
	if !strings.Contains(text, "Drivers Recovered") {
		t.Errorf("expected 'Drivers Recovered' in text, got: %s", text)
	}
}

func TestNotifier_WebhookError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNotifier(srv.URL)

	err := n.PostDriversRecovered(ctx)
	if err == nil {
		t.Fatal("expected error on non-200 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 in error, got: %v", err)
	}
}

func TestBrain_SetNotifier(t *testing.T) {
	d, _ := testSetup(t)
	brain := NewBrain(d, DefaultChains())

	n := NewNotifier("") // disabled
	brain.SetNotifier(n)

	if brain.notifier != n {
		t.Fatal("SetNotifier did not set the notifier")
	}
}

func TestBrain_MaybePostDashboard_NoopWhenDisabled(t *testing.T) {
	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier("")) // disabled

	// Should not panic or error even with no-op notifier
	brain.maybePostDashboard(ctx)
}

func TestBrain_MaybeNotifyConstraintChange_EdgeTriggered(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, ctx := testSetup(t)
	brain := NewBrain(d, DefaultChains())
	brain.SetNotifier(NewNotifier(srv.URL))

	downConstraint := Constraint{Type: "all_drivers_down", Description: "all down", Severity: 0}
	noneConstraint := Constraint{Type: "none", Description: "healthy", Severity: 2}

	// First down transition: should fire PostDriversDown
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected 1 Slack call on first down transition, got %d", callCount)
	}

	// Still down: should NOT fire again (edge-triggered)
	brain.maybeNotifyConstraintChange(ctx, downConstraint)
	if callCount != 1 {
		t.Fatalf("expected no additional Slack call when still down, got %d", callCount)
	}

	// Recovery transition: should fire PostDriversRecovered
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected 1 additional Slack call on recovery, got %d", callCount)
	}

	// Still healthy: no additional calls
	brain.maybeNotifyConstraintChange(ctx, noneConstraint)
	if callCount != 2 {
		t.Fatalf("expected no additional Slack call when still healthy, got %d", callCount)
	}
}
