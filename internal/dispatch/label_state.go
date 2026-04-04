package dispatch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Label constants for the dispatch state machine on GitHub issues.
const (
	LabelClaimed = "agent:claimed"
	LabelReview  = "agent:review"
	LabelDone    = "agent:done"
	LabelBlocked = "agent:blocked"
)

// addIssueLabel adds a label to a GitHub issue using the GitHub API.
func (b *Brain) addIssueLabel(ctx context.Context, repo string, issueNum int, label string) error {
	if b.ghToken == "" {
		return fmt.Errorf("no GitHub token configured")
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, issueNum)
	body := fmt.Sprintf(`{"labels":[%q]}`, label)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.ghToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	return nil
}
