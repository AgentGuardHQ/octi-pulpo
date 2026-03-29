package routing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteHealthFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	hf := HealthFile{
		State:       "OPEN",
		Failures:    3,
		LastFailure: "2026-03-29T10:00:00Z",
		LastSuccess: "2026-03-28T08:00:00Z",
		OpenedAt:    "2026-03-29T10:00:00Z",
		Updated:     "2026-03-29T10:00:00Z",
	}

	if err := WriteHealthFile(dir, "claude-code", hf); err != nil {
		t.Fatalf("WriteHealthFile: %v", err)
	}

	got := ReadDriverHealth(dir, "claude-code")
	if got.CircuitState != "OPEN" {
		t.Errorf("state: got %q, want OPEN", got.CircuitState)
	}
	if got.Failures != 3 {
		t.Errorf("failures: got %d, want 3", got.Failures)
	}
	if got.LastSuccess != "2026-03-28T08:00:00Z" {
		t.Errorf("last_success: got %q", got.LastSuccess)
	}
}

func TestWriteHealthFile_CreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "health")

	hf := HealthFile{State: "CLOSED"}
	if err := WriteHealthFile(dir, "copilot", hf); err != nil {
		t.Fatalf("WriteHealthFile on missing dir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "copilot.json")); err != nil {
		t.Fatalf("health file not created: %v", err)
	}
}

func TestOpenCircuit(t *testing.T) {
	dir := t.TempDir()
	// Seed with an existing health file so we can verify failure count increments
	writeHealth(t, dir, "claude-code", HealthFile{State: "CLOSED", Failures: 2, LastSuccess: "2026-03-28T08:00:00Z"})

	if err := OpenCircuit(dir, "claude-code"); err != nil {
		t.Fatalf("OpenCircuit: %v", err)
	}

	got := ReadDriverHealth(dir, "claude-code")
	if got.CircuitState != "OPEN" {
		t.Errorf("state: got %q, want OPEN", got.CircuitState)
	}
	if got.Failures != 3 {
		t.Errorf("failures: got %d, want 3 (incremented from 2)", got.Failures)
	}
	// LastSuccess must be preserved
	if got.LastSuccess != "2026-03-28T08:00:00Z" {
		t.Errorf("last_success changed unexpectedly: %q", got.LastSuccess)
	}
	// LastFailure must be set to roughly now
	if got.LastFailure == "" {
		t.Error("last_failure not set after OpenCircuit")
	}
}

func TestCloseCircuit(t *testing.T) {
	dir := t.TempDir()
	writeHealth(t, dir, "copilot", HealthFile{State: "OPEN", Failures: 5, LastFailure: "2026-03-29T10:00:00Z"})

	if err := CloseCircuit(dir, "copilot"); err != nil {
		t.Fatalf("CloseCircuit: %v", err)
	}

	got := ReadDriverHealth(dir, "copilot")
	if got.CircuitState != "CLOSED" {
		t.Errorf("state: got %q, want CLOSED", got.CircuitState)
	}
	// Failure count is preserved (not reset — avoids hiding history)
	if got.Failures != 5 {
		t.Errorf("failures: got %d, want 5", got.Failures)
	}
	// LastSuccess must be set to a recent timestamp
	if got.LastSuccess == "" {
		t.Error("last_success not set after CloseCircuit")
	}
	ts, err := time.Parse(time.RFC3339, got.LastSuccess)
	if err != nil {
		t.Fatalf("last_success not valid RFC3339: %v", err)
	}
	if time.Since(ts) > 5*time.Second {
		t.Errorf("last_success is too old: %s", got.LastSuccess)
	}
}

func TestDetectExhaustedDriver_ClaudeCredit(t *testing.T) {
	output := "Error: Credit balance is too low. Visit claude.ai to top up."
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected credit error to be detected")
	}
	if driver != "claude-code" {
		t.Errorf("driver: got %q, want claude-code", driver)
	}
}

func TestDetectExhaustedDriver_QuotaExceeded(t *testing.T) {
	output := "openai.com: You have exceeded your current quota, please check your plan."
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected quota error to be detected")
	}
	if driver != "codex" {
		t.Errorf("driver: got %q, want codex", driver)
	}
}

func TestDetectExhaustedDriver_429(t *testing.T) {
	output := "HTTP/1.1 429 Too Many Requests\nRetry-After: 60"
	driver, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected 429 error to be detected")
	}
	// Driver can't be inferred from a bare 429 without more context
	if driver == "" {
		t.Error("driver should be 'unknown', not empty")
	}
}

func TestDetectExhaustedDriver_NoError(t *testing.T) {
	output := "Successfully completed review. 3 comments posted."
	driver, found := DetectExhaustedDriver(output)
	if found {
		t.Errorf("false positive: detected driver %q on clean output", driver)
	}
}

func TestDetectExhaustedDriver_CaseInsensitive(t *testing.T) {
	output := strings.ToUpper("credit balance is too low — please top up your account")
	_, found := DetectExhaustedDriver(output)
	if !found {
		t.Fatal("expected case-insensitive match for credit balance error")
	}
}
