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

// ── Task-type minimum tier tests ──────────────────────────────────────────────

func TestRecommend_CodeTaskSkipsLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code-review", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// code-review requires CLI tier minimum — ollama must be skipped
	if dec.Driver != "claude-code" {
		t.Fatalf("expected claude-code (CLI tier, code-review min), got %s", dec.Driver)
	}
	if dec.Tier != string(TierCLI) {
		t.Fatalf("expected cli tier, got %s", dec.Tier)
	}
}

func TestRecommend_CodeTaskAllCLIOpenCascadesToAPI(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("code", "high")

	if dec.Skip {
		t.Fatal("expected fallback to API tier, got Skip")
	}
	// CLI exhausted — should escalate to API, not descend to local
	if dec.Driver != "anthropic-api" {
		t.Fatalf("expected anthropic-api (API fallback for code task), got %s", dec.Driver)
	}
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected api tier, got %s", dec.Tier)
	}
}

func TestRecommend_CodeTaskNoAPIDriversSkips(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "OPEN", Failures: 5})

	r := NewRouter(dir)
	dec := r.Recommend("code", "high")

	// CLI is OPEN, ollama is below min tier, no API driver — must skip
	if !dec.Skip {
		t.Fatalf("expected Skip (code task, CLI exhausted, no API), got driver=%s", dec.Driver)
	}
}

func TestRecommend_TriageTaskUsesLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("triage", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// triage is local-capable — should use ollama (cheapest)
	if dec.Driver != "ollama" {
		t.Fatalf("expected ollama for triage task, got %s", dec.Driver)
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier, got %s", dec.Tier)
	}
}

func TestRecommend_UnknownTaskTypeDefaultsToLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("some-future-task-type", "high")

	if dec.Skip {
		t.Fatal("expected recommendation for unknown task type, got Skip")
	}
	// Unknown task types have no floor — cheapest (local) wins
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier for unknown task type, got %s", dec.Tier)
	}
}

func TestRecommend_EmptyTaskTypeDefaultsToLocal(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "ollama", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("", "high")

	if dec.Skip {
		t.Fatal("expected recommendation for empty task type, got Skip")
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier for empty task type, got %s", dec.Tier)
	}
}

// ── New driver registration tests ─────────────────────────────────────────────

func TestRecommend_SubscriptionDriversChatGPTNotebookLM(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "chatgpt", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "notebooklm", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("briefing", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// briefing min tier is subscription — chatgpt/notebooklm should be chosen over claude-code
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier for briefing task, got %s", dec.Tier)
	}
}

func TestRecommend_APITierDrivers(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "anthropic-api", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "openai-api", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "gemini-api", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("burst", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// burst requires API tier
	if dec.Tier != string(TierAPI) {
		t.Fatalf("expected api tier for burst task, got %s", dec.Tier)
	}
	// All three are API tier — one chosen, two fallbacks
	if len(dec.Fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks for 3 API drivers, got %d", len(dec.Fallbacks))
	}
}

func TestRecommend_GeminiAppIsSubscriptionTier(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "gemini-app", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "gemini", HealthFile{State: "CLOSED", Failures: 0}) // CLI tier Gemini

	r := NewRouter(dir)
	dec := r.Recommend("task", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	// gemini-app (subscription) is cheaper than gemini CLI
	if dec.Driver != "gemini-app" {
		t.Fatalf("expected gemini-app (subscription, cheaper), got %s", dec.Driver)
	}
	if dec.Tier != string(TierSubscription) {
		t.Fatalf("expected subscription tier, got %s", dec.Tier)
	}
}

func TestRecommend_NemoclawIsLocalTier(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "nemoclaw", HealthFile{State: "CLOSED", Failures: 0})
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 0})

	r := NewRouter(dir)
	dec := r.Recommend("triage", "high")

	if dec.Skip {
		t.Fatal("expected recommendation, got Skip")
	}
	if dec.Tier != string(TierLocal) {
		t.Fatalf("expected local tier for nemoclaw, got %s", dec.Tier)
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
