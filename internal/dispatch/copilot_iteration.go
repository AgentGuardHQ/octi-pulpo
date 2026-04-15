package dispatch

// T2 internal-iteration loop (bridge).
//
// Copilot's auto-reviewer (`copilot-pull-request-reviewer`) leaves
// COMMENTED reviews on PRs the Copilot coding agent (`copilot-swe-agent`)
// authors, but NOTHING automatic happens after that — the PR stalls. To
// continue iterating we must @-mention @copilot to re-trigger the coding
// agent. This loop does that mechanically, with safety caps.
//
// Scope is deliberately narrow:
//   - trigger only on pull_request_review.submitted
//   - reviewer == copilot-pull-request-reviewer (auto-review bot)
//   - review state == COMMENTED
//   - PR author == copilot-swe-agent (NOT human-authored)
//   - PR is not draft
// Caps:
//   - max 3 iterations per PR (Redis counter, 7d TTL)
//   - 60s debounce between fires (SetNX)
//   - skip when review body has zero `Suggested change:` blocks AND zero
//     `## ` section headings (best-effort "looks good" heuristic; when
//     in doubt, iterate).
//
// On cap-hit we label the PR `tier:human` and emit a dispatch-log record
// with event=iteration_max_reached. On successful trigger we emit
// event=iteration_triggered.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// CopilotIterationMention is the comment body that re-triggers the
	// Copilot coding agent on the PR.
	CopilotIterationMention = "@copilot please address the review feedback above"

	// CopilotAutoReviewerBot is the login of the auto-review bot that
	// leaves COMMENTED reviews on Copilot-authored PRs.
	CopilotAutoReviewerBot = "copilot-pull-request-reviewer"

	// CopilotIterationMaxAttempts caps the number of @-mention triggers
	// per PR before escalation to a human.
	CopilotIterationMaxAttempts = 3

	// CopilotIterationCounterTTL bounds stale counters when a PR is
	// abandoned without a close event.
	CopilotIterationCounterTTL = 7 * 24 * time.Hour

	// CopilotIterationDebounce collapses bursts of quick-fire review
	// events from the auto-reviewer into a single @-mention.
	CopilotIterationDebounce = 60 * time.Second
)

// iterationRedis is the narrow Redis surface used by CopilotIterationLoop.
// Kept separate from copilotRedis so tests can evolve independently.
type iterationRedis interface {
	Incr(ctx context.Context, key string) *redis.IntCmd
	Expire(ctx context.Context, key string, ttl time.Duration) *redis.BoolCmd
	SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// CopilotIterationLoop auto-mentions @copilot on COMMENTED auto-reviews.
type CopilotIterationLoop struct {
	ghToken     string
	rdb         iterationRedis
	baseURL     string
	maxAttempts int
	dispatcher  *Dispatcher // optional — for dispatch-log telemetry
}

// NewCopilotIterationLoop wires a new loop using the shared Redis client.
func NewCopilotIterationLoop(ghToken string, rdb redis.Cmdable, dispatcher *Dispatcher) *CopilotIterationLoop {
	return &CopilotIterationLoop{
		ghToken:     ghToken,
		rdb:         rdb,
		baseURL:     "https://api.github.com",
		maxAttempts: CopilotIterationMaxAttempts,
		dispatcher:  dispatcher,
	}
}

// ReviewInput is the minimal slice of webhook fields the loop needs. The
// webhook handler extracts these from `pull_request_review.submitted`
// payloads and passes them in; keeping the signature typed (not a raw
// map) makes the contract explicit and test-friendly.
type ReviewInput struct {
	Repo         string
	PRNumber     int
	ReviewerBot  string // review.user.login
	ReviewState  string // review.state — "commented", "approved", ...
	PRAuthor     string // pull_request.user.login
	IsDraft      bool   // pull_request.draft
	ReviewBody   string // review.body
}

// HandleReview is the single entry point. It returns (triggered, err).
// `triggered` is true iff a new @-mention was posted this call (useful in
// tests and for caller-side logging). All "skip" paths return
// (false, nil) — they are routine, not errors.
func (c *CopilotIterationLoop) HandleReview(ctx context.Context, in ReviewInput) (bool, error) {
	if !c.shouldIterate(in) {
		return false, nil
	}

	// Debounce: swallow a 2nd fire within the window.
	debounceKey := c.debounceKey(in.Repo, in.PRNumber)
	ok, err := c.rdb.SetNX(ctx, debounceKey, "1", CopilotIterationDebounce).Result()
	if err != nil {
		return false, fmt.Errorf("copilot-iter: debounce setnx: %w", err)
	}
	if !ok {
		return false, nil
	}

	// Increment iteration counter atomically and refresh TTL.
	counterKey := c.counterKey(in.Repo, in.PRNumber)
	count, err := c.rdb.Incr(ctx, counterKey).Result()
	if err != nil {
		return false, fmt.Errorf("copilot-iter: incr counter: %w", err)
	}
	_ = c.rdb.Expire(ctx, counterKey, CopilotIterationCounterTTL).Err()

	if count > int64(c.maxAttempts) {
		// Cap exceeded — escalate to a human.
		if labelErr := c.addLabel(ctx, in.Repo, in.PRNumber, "tier:human"); labelErr != nil {
			return false, fmt.Errorf("copilot-iter: add tier:human: %w", labelErr)
		}
		c.emitDispatchLog(ctx, in.Repo, in.PRNumber, "iteration_max_reached", int(count))
		return false, nil
	}

	if err := c.postComment(ctx, in.Repo, in.PRNumber, CopilotIterationMention); err != nil {
		return false, err
	}
	c.emitDispatchLog(ctx, in.Repo, in.PRNumber, "iteration_triggered", int(count))
	return true, nil
}

// shouldIterate gates on author / reviewer / state / draft / body heuristics.
// Conservative: when in doubt, iterate.
func (c *CopilotIterationLoop) shouldIterate(in ReviewInput) bool {
	if !strings.EqualFold(in.ReviewState, "commented") {
		return false
	}
	if !isCopilotAutoReviewer(in.ReviewerBot) {
		return false
	}
	if !isCopilotSWEBot(in.PRAuthor) {
		return false
	}
	if in.IsDraft {
		return false
	}
	if looksLikeNoActionableFindings(in.ReviewBody) {
		return false
	}
	return true
}

// isCopilotAutoReviewer matches the auto-review bot login. GitHub sometimes
// suffixes `[bot]` and the handle is occasionally seen lowercased, so
// compare case-insensitively against both forms.
func isCopilotAutoReviewer(login string) bool {
	l := strings.ToLower(strings.TrimSuffix(login, "[bot]"))
	return l == CopilotAutoReviewerBot
}

// looksLikeNoActionableFindings is a conservative heuristic: return true
// only when the review body has ZERO `Suggested change:` blocks AND
// ZERO `## ` headings. Any structure => iterate.
func looksLikeNoActionableFindings(body string) bool {
	if body == "" {
		return true
	}
	if strings.Contains(body, "Suggested change:") {
		return false
	}
	// A `## ` section heading must start a line. Split and check each.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			return false
		}
	}
	return true
}

// emitDispatchLog synthesizes a DispatchRecord so swarm_today and
// Sentinel can observe iteration events. Best-effort; errors are logged
// but not returned.
func (c *CopilotIterationLoop) emitDispatchLog(ctx context.Context, repo string, prNumber int, eventName string, count int) {
	if c.dispatcher == nil {
		return
	}
	rec := DispatchRecord{
		Agent: "copilot-iteration",
		Event: Event{
			Type:   EventSignal,
			Source: "github",
			Repo:   repo,
			Payload: map[string]string{
				"event":     eventName,
				"pr":        strconv.Itoa(prNumber),
				"iteration": strconv.Itoa(count),
			},
		},
		Result:     eventName,
		Reason:     fmt.Sprintf("copilot-iteration %s: %s#%d (iter=%d)", eventName, repo, prNumber, count),
		Driver:     CopilotAgentDriver,
		Tier:       "copilot",
		DispatchID: newDispatchID(),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	pipe := c.dispatcher.rdb.Pipeline()
	pipe.LPush(ctx, c.dispatcher.key("dispatch-log"), data)
	pipe.LTrim(ctx, c.dispatcher.key("dispatch-log"), 0, 499)
	_, _ = pipe.Exec(ctx)
}

func (c *CopilotIterationLoop) counterKey(repo string, prNumber int) string {
	return "octi:copilot_iterations:" + repo + ":" + strconv.Itoa(prNumber)
}

func (c *CopilotIterationLoop) debounceKey(repo string, prNumber int) string {
	return "octi:copilot_iter_debounce:" + repo + ":" + strconv.Itoa(prNumber)
}

// postComment posts a comment on the PR via the GitHub issues-comments API.
func (c *CopilotIterationLoop) postComment(ctx context.Context, repo string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.baseURL, repo, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("copilot-iter: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot-iter: post comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot-iter: GitHub API %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// addLabel attaches a single label to the PR/issue.
func (c *CopilotIterationLoop) addLabel(ctx context.Context, repo string, prNumber int, label string) error {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", c.baseURL, repo, prNumber)
	payload, _ := json.Marshal(map[string][]string{"labels": {label}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("copilot-iter: build label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot-iter: label request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot-iter: GitHub API (label) %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
