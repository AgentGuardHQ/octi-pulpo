package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// claudeTaskTypes lists the task types suitable for @claude via GitHub issues.
var claudeTaskTypes = map[string]bool{
	"code-gen":      true,
	"bugfix":        true,
	"config":        true,
	"test":          true,
	"evolve":        true,
	"prompt_config": true,
	"tool_addition": true,
	"config_change": true,
}

// ClaudeIssueAdapter dispatches tasks by creating GitHub issues with an @claude
// mention in the body. The Claude Code GitHub Action picks up these issues and
// opens a PR implementing the requested work.
//
// This adapter is designed as a fallback when direct agent dispatch (Clawta,
// clawta-dispatch.yml) is unavailable. It requires only GITHUB_TOKEN.
//
// Priority in the adapter list: after ClawtaAdapter, before GHActionsAdapter.
type ClaudeIssueAdapter struct {
	token   string
	baseURL string
}

// NewClaudeIssueAdapter creates a ClaudeIssueAdapter. If token is empty the
// value of GITHUB_TOKEN is used.
func NewClaudeIssueAdapter(token string) *ClaudeIssueAdapter {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return &ClaudeIssueAdapter{
		token:   token,
		baseURL: defaultGHBaseURL, // reuse constant from ghactions_adapter.go
	}
}

// Name returns the adapter identifier.
func (c *ClaudeIssueAdapter) Name() string { return "claude-issue" }

// CanAccept returns true when:
//   - GITHUB_TOKEN is available
//   - task.Repo is non-empty (needed to know which repo to open the issue in)
//   - the task type is suitable for @claude (code work, not triage/pr-review)
//   - OCTI_DISABLE_CLAUDE_ISSUE is not set to "1"
func (c *ClaudeIssueAdapter) CanAccept(task *Task) bool {
	if os.Getenv("OCTI_DISABLE_CLAUDE_ISSUE") == "1" {
		return false
	}
	if c.token == "" {
		return false
	}
	if task == nil || task.Repo == "" {
		return false
	}
	return claudeTaskTypes[task.Type]
}

// ghIssueCreateRequest is the JSON body for POST /repos/{owner}/{repo}/issues.
type ghIssueCreateRequest struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// ghIssueResponse is the minimal subset of the GitHub issue API response we need.
type ghIssueResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// Dispatch creates a GitHub issue in task.Repo with an @claude mention so the
// Claude Code GitHub Action picks it up. Returns a "queued" result containing
// the issue URL in Output.
func (c *ClaudeIssueAdapter) Dispatch(ctx context.Context, task *Task) (*AdapterResult, error) {
	title := buildIssueTitle(task)
	body := buildIssueBody(task)

	payload := ghIssueCreateRequest{
		Title:  title,
		Body:   body,
		Labels: []string{LabelClaimed},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("claude-issue: marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues", c.baseURL, task.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("claude-issue: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude-issue: http: %w", err)
	}
	defer resp.Body.Close()

	result := &AdapterResult{
		TaskID:  task.ID,
		Adapter: c.Name(),
	}

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		result.Status = "failed"
		result.Error = fmt.Sprintf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return result, nil
	}

	var issue ghIssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		// Issue was created; we just can't parse the URL. Still a success.
		result.Status = "queued"
		result.Output = fmt.Sprintf("issue created in %s (url unknown)", task.Repo)
		return result, nil
	}

	result.Status = "queued"
	result.Output = fmt.Sprintf("issue #%d created: %s", issue.Number, issue.HTMLURL)
	return result, nil
}

// buildIssueTitle returns a title capped at ~80 chars.
func buildIssueTitle(task *Task) string {
	const maxLen = 80
	prompt := strings.TrimSpace(task.Prompt)
	// Use first line only for the title.
	if nl := strings.IndexByte(prompt, '\n'); nl != -1 {
		prompt = strings.TrimSpace(prompt[:nl])
	}
	if len(prompt) > maxLen {
		prompt = prompt[:maxLen-1] + "…"
	}
	return prompt
}

// buildIssueBody produces a well-structured issue body for @claude.
// It includes the @claude trigger, the full prompt, and task metadata so
// Claude Code has enough context to implement and open a PR.
func buildIssueBody(task *Task) string {
	var sb strings.Builder

	sb.WriteString("@claude\n\n")
	sb.WriteString("## Task\n\n")
	sb.WriteString(strings.TrimSpace(task.Prompt))
	sb.WriteString("\n\n")

	if task.Context != "" {
		sb.WriteString("## Context\n\n")
		sb.WriteString(strings.TrimSpace(task.Context))
		sb.WriteString("\n\n")
	}

	if task.System != "" {
		sb.WriteString("## System Guidance\n\n")
		sb.WriteString(strings.TrimSpace(task.System))
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Metadata\n\n")
	sb.WriteString(fmt.Sprintf("- **Task ID**: `%s`\n", task.ID))
	sb.WriteString(fmt.Sprintf("- **Type**: `%s`\n", task.Type))
	sb.WriteString(fmt.Sprintf("- **Priority**: `%s`\n", task.Priority))
	if len(task.Toolset) > 0 {
		sb.WriteString(fmt.Sprintf("- **Toolset**: `%s`\n", strings.Join(task.Toolset, ", ")))
	}

	sb.WriteString("\n---\n")
	sb.WriteString("*Dispatched by octi-pulpo via `claude-issue` adapter. ")
	sb.WriteString("Please implement the task above and open a PR.*\n")

	return sb.String()
}
