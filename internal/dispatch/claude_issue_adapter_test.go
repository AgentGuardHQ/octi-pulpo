package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClaudeIssueAdapterName(t *testing.T) {
	a := NewClaudeIssueAdapter("token")
	if a.Name() != "claude-issue" {
		t.Errorf("Name: got %q", a.Name())
	}
}

func TestClaudeIssueAdapterCanAccept(t *testing.T) {
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")
	a := NewClaudeIssueAdapter("mytoken")

	for _, taskType := range []string{"code-gen", "bugfix", "config", "test", "evolve", "prompt_config", "tool_addition", "config_change"} {
		task := &Task{ID: "t1", Repo: "chitinhq/octi", Type: taskType}
		if !a.CanAccept(task) {
			t.Errorf("expected CanAccept true for type %q", taskType)
		}
	}
}

func TestClaudeIssueAdapterCanAccept_UnsuitableType(t *testing.T) {
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")
	a := NewClaudeIssueAdapter("mytoken")
	task := &Task{ID: "t1", Repo: "chitinhq/octi", Type: "triage"}
	if a.CanAccept(task) {
		t.Error("expected CanAccept false for type triage")
	}
}

func TestClaudeIssueAdapterCanAccept_NoToken(t *testing.T) {
	os.Unsetenv("GITHUB_TOKEN")
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")
	a := NewClaudeIssueAdapter("")
	task := &Task{ID: "t1", Repo: "chitinhq/octi", Type: "bugfix"}
	if a.CanAccept(task) {
		t.Error("expected CanAccept false when token is empty")
	}
}

func TestClaudeIssueAdapterCanAccept_NoRepo(t *testing.T) {
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")
	a := NewClaudeIssueAdapter("mytoken")
	task := &Task{ID: "t1", Repo: "", Type: "bugfix"}
	if a.CanAccept(task) {
		t.Error("expected CanAccept false when repo is empty")
	}
}

func TestClaudeIssueAdapterCanAccept_Disabled(t *testing.T) {
	t.Setenv("OCTI_DISABLE_CLAUDE_ISSUE", "1")
	a := NewClaudeIssueAdapter("mytoken")
	task := &Task{ID: "t1", Repo: "chitinhq/octi", Type: "bugfix"}
	if a.CanAccept(task) {
		t.Error("expected CanAccept false when OCTI_DISABLE_CLAUDE_ISSUE=1")
	}
}

func TestClaudeIssueAdapterDispatch(t *testing.T) {
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")

	var received ghIssueCreateRequest
	var requestPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			t.Errorf("auth header: got %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number":42,"html_url":"https://github.com/chitinhq/octi/issues/42"}`))
	}))
	defer srv.Close()

	a := NewClaudeIssueAdapter("testtoken")
	a.baseURL = srv.URL

	task := &Task{
		ID:       "task-77",
		Type:     "bugfix",
		Repo:     "chitinhq/octi",
		Prompt:   "Fix the memory leak in the dispatch loop",
		Toolset:  []string{"read_file", "write_file"},
		Priority: "high",
		Context:  "The leak was introduced in commit abc123",
	}

	res, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "queued" {
		t.Errorf("Status: got %q", res.Status)
	}
	if res.Adapter != "claude-issue" {
		t.Errorf("Adapter: got %q", res.Adapter)
	}
	if !strings.Contains(res.Output, "42") {
		t.Errorf("Output should contain issue number 42, got %q", res.Output)
	}

	// Validate issue title
	if !strings.Contains(received.Title, "Fix the memory leak") {
		t.Errorf("Title missing prompt text: got %q", received.Title)
	}

	// Validate body has @claude trigger
	if !strings.HasPrefix(received.Body, "@claude") {
		t.Errorf("Body should start with @claude, got: %q", received.Body[:min(30, len(received.Body))])
	}

	// Validate body contains full prompt
	if !strings.Contains(received.Body, "Fix the memory leak in the dispatch loop") {
		t.Errorf("Body missing full prompt")
	}

	// Validate body contains context
	if !strings.Contains(received.Body, "abc123") {
		t.Errorf("Body missing context")
	}

	// Validate body contains task metadata
	if !strings.Contains(received.Body, "task-77") {
		t.Errorf("Body missing task ID")
	}

	// Validate labels
	if len(received.Labels) == 0 || received.Labels[0] != LabelClaimed {
		t.Errorf("Labels: got %v", received.Labels)
	}

	// Validate URL path
	if requestPath != "/repos/chitinhq/octi/issues" {
		t.Errorf("URL path: got %q", requestPath)
	}
}

func TestClaudeIssueAdapterDispatch_HTTPError(t *testing.T) {
	os.Unsetenv("OCTI_DISABLE_CLAUDE_ISSUE")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()

	a := NewClaudeIssueAdapter("testtoken")
	a.baseURL = srv.URL

	task := &Task{ID: "t-err", Type: "bugfix", Repo: "chitinhq/octi", Prompt: "do something"}
	res, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed", res.Status)
	}
	if !strings.Contains(res.Error, "422") {
		t.Errorf("Error should mention status 422, got %q", res.Error)
	}
}

func TestBuildIssueTitle_Truncation(t *testing.T) {
	task := &Task{Prompt: strings.Repeat("x", 200)}
	title := buildIssueTitle(task)
	if len([]rune(title)) > 80 {
		t.Errorf("title too long: %d chars", len(title))
	}
}

func TestBuildIssueTitle_FirstLine(t *testing.T) {
	task := &Task{Prompt: "First line\nSecond line\nThird line"}
	title := buildIssueTitle(task)
	if title != "First line" {
		t.Errorf("expected first line only, got %q", title)
	}
}

func TestBuildIssueBody_ContainsClaudeMention(t *testing.T) {
	task := &Task{ID: "t1", Type: "bugfix", Priority: "high", Prompt: "Do something"}
	body := buildIssueBody(task)
	if !strings.HasPrefix(body, "@claude") {
		t.Errorf("body must start with @claude")
	}
}

func TestBuildIssueBody_NoSystemOrContext(t *testing.T) {
	task := &Task{ID: "t1", Type: "bugfix", Priority: "normal", Prompt: "Fix it"}
	body := buildIssueBody(task)
	if strings.Contains(body, "## Context") {
		t.Error("body should not include Context section when task.Context is empty")
	}
	if strings.Contains(body, "## System Guidance") {
		t.Error("body should not include System Guidance section when task.System is empty")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
