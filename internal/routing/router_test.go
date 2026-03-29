package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHealth writes a driver health JSON file into the temp directory.
func writeHealth(t *testing.T, dir, driver string, hf HealthFile) {
	t.Helper()
	data, err := json.Marshal(hf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, driver+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRecommend_HealthyDriver(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver == "" {
		t.Fatal("expected a driver name, got empty")
	}
	// Both are CLI tier — either is valid
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected tier cli, got %s", dec.Tier)
	}
}

func TestRecommend_SkipsOpenDrivers(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "copilot", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver != "copilot" {
		t.Fatalf("expected copilot (healthy), got %s", dec.Driver)
	}
}

func TestRecommend_AllDriversOpen(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 8})

	r := NewRouter(dir)
	dec := r.Recommend("anything", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip=true when all drivers OPEN, got driver=%s", dec.Driver)
	}
	if dec.Reason == "" {
		t.Fatal("expected a reason when skipping")
	}
}

func TestRecommend_CostTierOrdering(t *testing.T) {
	dir := t.TempDir()
	// Local driver should be chosen over CLI when both healthy
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("simple-task", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (cheapest tier), got %s", dec.Driver)
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected tier local, got %s", dec.Tier)
	}
	// claude-code should be a fallback
	if len(dec.Fallbacks) == 0 {
		t.Fatal("expected claude-code as fallback")
	}
}

func TestRecommend_MissingHealthFileDefaultsClosed(t *testing.T) {
	dir := t.TempDir()
	// Write a valid file for copilot (OPEN), but claude-code has no file
	// Manually create a file with missing/empty state
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 3})
	writeHealth(t, dir, "claude-code", HealthFile{}) // empty state defaults to CLOSED in ReadDriverHealth

	r := NewRouter(dir)
	dec := r.Recommend("any-task", "high")

	if dec.Skip {
		t.Fatal("expected a driver recommendation, got Skip")
	}
	// claude-code should be chosen since copilot is OPEN
	// and empty state in ReadDriverHealth becomes whatever the file says (empty string)
	// We need to check: empty state should be treated as healthy
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (copilot is OPEN), got %s", dec.Driver)
	}
}

func TestRecommend_NoDriversAvailable(t *testing.T) {
	dir := t.TempDir()
	// Empty directory — no drivers discovered

	r := NewRouter(dir)
	dec := r.Recommend("any-task", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip=true with no drivers, got driver=%s", dec.Driver)
	}
}

func TestRecommend_LowBudgetOnlyLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "low")

	// Low budget only allows local tier — should pick ollama, skip claude-code
	if dec.Skip {
		t.Fatal("expected ollama recommendation, got Skip")
	}
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (local tier), got %s", dec.Driver)
	}
	// claude-code should NOT be in fallbacks (it's CLI tier, above budget)
	for _, fb := range dec.Fallbacks {
		if fb == "claude-code" {
			t.Fatal("claude-code should not be a fallback for low budget")
		}
	}
}

func TestRecommend_LowBudgetAllLocalOpen(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "low")

	// ollama OPEN, and low budget prevents using CLI tier claude-code
	if !dec.Skip {
		t.Fatalf("expected Skip (local OPEN, can't use CLI at low budget), got driver=%s", dec.Driver)
	}
}

func TestRecommend_HalfOpenReducedConfidence(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "HALF", Failures: 2})

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected a recommendation for HALF-open driver")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code, got %s", dec.Driver)
	}
	if dec.Confidence != 0.5 {
		t.Fatalf("expected confidence 0.5 for HALF driver, got %f", dec.Confidence)
	}
}

func TestRecommend_SubscriptionTier(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "openclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// openclaw (subscription) is cheaper than claude-code (cli)
	if dec.Driver != "openclaw" {
		t.Fatalf("expected openclaw (subscription tier, cheaper), got %s", dec.Driver)
	}
}

func TestHealthReport(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5, LastFailure: "2026-03-29T10:00:00Z"})

	r := NewRouter(dir)
	report := r.HealthReport()

	if len(report) != 2 {
		t.Fatalf("expected 2 drivers in report, got %d", len(report))
	}

	found := make(map[string]DriverHealth)
	for _, dh := range report {
		found[dh.Name] = dh
	}

	if cc, ok := found["claude-code"]; !ok {
		t.Fatal("missing claude-code in report")
	} else if cc.CircuitState != "CLOSED" {
		t.Fatalf("expected CLOSED for claude-code, got %s", cc.CircuitState)
	}

	if cp, ok := found["copilot"]; !ok {
		t.Fatal("missing copilot in report")
	} else if cp.CircuitState != "OPEN" {
		t.Fatalf("expected OPEN for copilot, got %s", cp.CircuitState)
	} else if cp.Failures != 5 {
		t.Fatalf("expected 5 failures for copilot, got %d", cp.Failures)
	}
}

// --- Task-type capability floor tests ---

func TestRecommend_CodingTaskSkipsLocalTier(t *testing.T) {
	dir := t.TempDir()
	// Only a local driver available — coding tasks require CLI tier minimum
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("coding", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip: coding task needs CLI tier, only local available; got driver=%s tier=%s", dec.Driver, dec.Tier)
	}
	if dec.MinTier != string(TierCLI) {
		t.Fatalf("expected min_tier=cli, got %s", dec.MinTier)
	}
}

func TestRecommend_CodingTaskUsesCLIOverLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// Coding task floor is CLI; ollama (local) should be skipped
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier for coding task, got %s", dec.Tier)
	}
	if dec.MinTier != string(TierCLI) {
		t.Fatalf("expected min_tier=cli, got %s", dec.MinTier)
	}
}

func TestRecommend_BrowsingTaskSkipsLocalUsesSubscription(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "openclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("browse website and fill form", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// local (ollama) is below the floor; openclaw (subscription) is cheapest capable
	if dec.Driver != "openclaw" {
		t.Fatalf("expected openclaw (cheapest capable for browsing), got %s", dec.Driver)
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier, got %s", dec.Tier)
	}
}

// TestRecommend_BrowsingTaskCascadesToCLI verifies that when the subscription driver
// is exhausted, browsing tasks cascade to CLI (the next tier above the floor).
func TestRecommend_BrowsingTaskCascadesToCLI(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "openclaw", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("navigate to url", "high")

	if dec.Skip {
		t.Fatal("expected cascade from subscription to CLI, got Skip")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (cascade subscription→CLI), got %s", dec.Driver)
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier, got %s", dec.Tier)
	}
}

// TestRecommend_BrowsingTaskNoCapableDrivers ensures local-only setups skip for
// browser tasks (local is below the subscription capability floor).
func TestRecommend_BrowsingTaskNoCapableDrivers(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("screenshot a page", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip: only local available, browsing needs subscription+ tier; got driver=%s", dec.Driver)
	}
	if dec.MinTier != string(TierSubscription) {
		t.Fatalf("expected min_tier=subscription, got %s", dec.MinTier)
	}
}

func TestRecommend_APITierDriver(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "anthropic", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	// CLI tier should be preferred over API tier (cheaper)
	dec := r.Recommend("coding task", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected CLI tier (cheaper than API), got %s", dec.Tier)
	}
	// anthropic should appear as a fallback
	found := false
	for _, fb := range dec.Fallbacks {
		if fb == "anthropic" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected anthropic (api tier) as fallback")
	}
}

func TestRecommend_APITierDirectWhenCLIExhausted(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "anthropic", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("coding task", "high")

	if dec.Skip {
		t.Fatal("expected cascade to API tier, got Skip")
	}
	if dec.Driver != "anthropic" {
		t.Fatalf("expected anthropic (only healthy driver), got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected API tier, got %s", dec.Tier)
	}
}

func TestRecommend_NemoclawLocalTier(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "nemoclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("classify this text", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Driver != "nemoclaw" {
		t.Fatalf("expected nemoclaw (local, cheapest), got %s", dec.Driver)
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier, got %s", dec.Tier)
	}
}

func TestRecommend_MinTierInSkipReason(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "OPEN", Failures: 3})
	// No CLI drivers available at all

	r := NewRouter(dir)
	dec := r.Recommend("implement feature", "high")

	if !dec.Skip {
		t.Fatalf("expected Skip, got driver=%s", dec.Driver)
	}
	if !strings.Contains(dec.Reason, "cli") {
		t.Fatalf("expected reason to mention cli tier, got: %s", dec.Reason)
	}
}

func TestDiscoverDrivers_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	drivers := DiscoverDrivers(dir)
	if len(drivers) != 0 {
		t.Fatalf("expected 0 drivers in empty dir, got %d", len(drivers))
	}
}

func TestDiscoverDrivers_NonexistentDir(t *testing.T) {
	drivers := DiscoverDrivers("/nonexistent/path/that/does/not/exist")
	if drivers != nil {
		t.Fatalf("expected nil for nonexistent dir, got %v", drivers)
	}
}
