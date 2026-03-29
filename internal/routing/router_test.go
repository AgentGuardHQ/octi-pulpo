package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// --- Task-type-aware routing tests ---

func TestRecommend_CodingTaskSkipsLocalTier(t *testing.T) {
	dir := t.TempDir()
	// Local driver available, but coding task requires CLI minimum
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected a recommendation, got Skip")
	}
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (coding task requires CLI tier), got %s", dec.Driver)
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected tier cli, got %s", dec.Tier)
	}
	// ollama should NOT appear — it's below the coding task's min tier
	for _, fb := range dec.Fallbacks {
		if fb == "ollama" {
			t.Fatal("ollama should not be a fallback for a coding task")
		}
	}
}

func TestRecommend_CodingTaskBudgetConflict(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	// code-review requires CLI, but budget "low" caps at local — conflict
	dec := r.Recommend("code-review", "low")

	if !dec.Skip {
		t.Fatalf("expected Skip=true for budget conflict (coding task + low budget), got driver=%s", dec.Driver)
	}
	if dec.Reason == "" {
		t.Fatal("expected a reason describing the budget conflict")
	}
}

func TestRecommend_CodingTaskFallsBackToAPI(t *testing.T) {
	dir := t.TempDir()
	// All CLI drivers exhausted — should cascade to API
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 10})
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 8})
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("coding", "high")

	if dec.Skip {
		t.Fatal("expected anthropic-api fallback when CLI exhausted, got Skip")
	}
	if dec.Driver != "anthropic-api" {
		t.Fatalf("expected anthropic-api (CLI OPEN, API available), got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected tier api, got %s", dec.Tier)
	}
}

func TestRecommend_ArtifactTaskStartsAtSubscription(t *testing.T) {
	dir := t.TempDir()
	// Local and subscription drivers both available
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "notebooklm", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("briefing", "high")

	if dec.Skip {
		t.Fatal("expected recommendation for briefing task, got Skip")
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier for briefing task, got %s", dec.Tier)
	}
	// ollama must not be chosen — briefings require subscription minimum
	if dec.Driver == "ollama" {
		t.Fatal("ollama should not be chosen for briefing task (below min tier)")
	}
}

func TestRecommend_UnknownTaskTypeDefaultsLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	// Unknown task type → no keyword match → min tier = local
	dec := r.Recommend("some-unrecognized-task-xyz", "high")

	if dec.Skip {
		t.Fatal("expected recommendation for unknown task type, got Skip")
	}
	// Should pick ollama (local, cheapest) since min tier defaults to local
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama (local, default min tier), got %s", dec.Driver)
	}
}

func TestRecommend_APITierDriver(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "openai-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("batch processing", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// "batch" keyword → min tier = API; both api drivers healthy
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected api tier for batch task, got %s", dec.Tier)
	}
}

func TestRecommend_SubscriptionDriverChatGPT(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "chatgpt", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("document summary", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// "document" → min tier = subscription; chatgpt is cheaper than claude-code
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier, got %s", dec.Tier)
	}
	if dec.Driver != "chatgpt" {
		t.Fatalf("expected chatgpt (subscription tier, cheaper than CLI), got %s", dec.Driver)
	}
}

func TestMinTierForTaskType(t *testing.T) {
	cases := []struct {
		taskType string
		wantTier CostTier
	}{
		{"code-review", TierCLI},
		{"write a commit message", TierCLI},
		{"refactor the auth module", TierCLI},
		{"generate a briefing document", TierSubscription},
		{"create artifact", TierSubscription},
		{"triage this issue", TierLocal},
		{"some unknown task", TierLocal},
		{"burst load test", TierAPI},
		{"batch process 10k items", TierAPI},
		// compound: both coding and document keywords → max wins = CLI
		{"code-review for the report", TierCLI},
	}

	for _, tc := range cases {
		got := minTierForTaskType(tc.taskType)
		if got != tc.wantTier {
			t.Errorf("minTierForTaskType(%q) = %s, want %s", tc.taskType, got, tc.wantTier)
		}
	}
}
